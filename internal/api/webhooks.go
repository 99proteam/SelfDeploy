package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/99proteam/selfdeploy/internal/deploy"
	"github.com/99proteam/selfdeploy/internal/store"
)

// gitEvent is the provider-neutral meaning of a webhook delivery.
type gitEvent struct {
	kind    string // push | pr_open | pr_close | ignore
	branch  string
	sha     string
	message string
	pr      int
	prRef   string
	fork    bool // the PR comes from another repository
}

func hmacHex(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func secretEqual(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// webhook handles push and pull/merge request events from GitHub, GitLab,
// Gitea/Forgejo and Bitbucket, plus a generic deploy hook for any CI.
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "unknown app")
		return
	}
	app, err := s.Store.App(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "unknown app")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 25<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "body too large")
		return
	}
	provider := r.PathValue("provider")
	if strings.HasPrefix(r.URL.Path, "/webhooks/deploy/") {
		provider = "deploy"
	}

	var ev gitEvent
	switch provider {
	case "github":
		sig := strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
		if !secretEqual(sig, hmacHex(app.WebhookSecret, body)) {
			writeErr(w, http.StatusUnauthorized, "invalid signature")
			return
		}
		ev, err = parseGitHub(r.Header.Get("X-GitHub-Event"), body, "refs/pull/%d/head")
	case "gitea", "forgejo", "gogs":
		sig := r.Header.Get("X-Gitea-Signature")
		if sig == "" {
			sig = r.Header.Get("X-Gogs-Signature")
		}
		if !secretEqual(sig, hmacHex(app.WebhookSecret, body)) {
			writeErr(w, http.StatusUnauthorized, "invalid signature")
			return
		}
		event := r.Header.Get("X-Gitea-Event")
		if event == "" {
			event = r.Header.Get("X-Gogs-Event")
		}
		ev, err = parseGitHub(event, body, "refs/pull/%d/head")
	case "gitlab":
		if !secretEqual(r.Header.Get("X-Gitlab-Token"), app.WebhookSecret) {
			writeErr(w, http.StatusUnauthorized, "invalid token")
			return
		}
		ev, err = parseGitLab(r.Header.Get("X-Gitlab-Event"), body)
	case "bitbucket":
		if !secretEqual(r.URL.Query().Get("secret"), app.WebhookSecret) {
			writeErr(w, http.StatusUnauthorized, "invalid secret")
			return
		}
		ev, err = parseBitbucket(r.Header.Get("X-Event-Key"), body)
	case "deploy":
		if !secretEqual(r.URL.Query().Get("secret"), app.WebhookSecret) {
			writeErr(w, http.StatusUnauthorized, "invalid secret")
			return
		}
		d, err := s.Deploy.Enqueue(app, deploy.Options{Trigger: "webhook", CommitSHA: r.URL.Query().Get("commit")})
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"deployment_id": d.ID})
		return
	default:
		writeErr(w, http.StatusNotFound, "unknown provider")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.handleGitEvent(w, app, ev)
}

func (s *Server) handleGitEvent(w http.ResponseWriter, app *store.App, ev gitEvent) {
	reply := func(msg string) { writeJSON(w, http.StatusOK, map[string]string{"message": msg}) }
	switch ev.kind {
	case "push":
		if ev.branch != app.GitBranch {
			reply(fmt.Sprintf("ignored: push to %s, app tracks %s", ev.branch, app.GitBranch))
			return
		}
		if !app.AutoDeploy {
			reply("ignored: auto deploy is off")
			return
		}
		d, err := s.Deploy.Enqueue(app, deploy.Options{Trigger: "webhook", CommitSHA: ev.sha, CommitMessage: ev.message})
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"deployment_id": d.ID})
	case "pr_open":
		if !app.PreviewDeploys || ev.branch != app.GitBranch {
			reply("ignored: preview deployments are off or the PR targets another branch")
			return
		}
		// Anyone can open a PR from a fork of a public repo. Building it would run
		// a stranger's code on this server with the app's environment variables.
		if ev.fork {
			reply("ignored: preview deployments are not built for pull requests from forks")
			return
		}
		d, err := s.Deploy.Enqueue(app, deploy.Options{Trigger: "preview", PRNumber: ev.pr, GitRef: ev.prRef,
			CommitSHA: ev.sha, CommitMessage: ev.message})
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"deployment_id": d.ID})
	case "pr_close":
		if err := s.Deploy.RemovePreview(app, ev.pr); err != nil {
			log.Printf("webhook: remove preview: %v", err)
		}
		reply(fmt.Sprintf("preview for #%d removed", ev.pr))
	default:
		reply("ignored event")
	}
}

type ghRepo struct {
	FullName string `json:"full_name"`
}

// parseGitHub reads GitHub and Gitea/Forgejo payloads, which share a format.
func parseGitHub(event string, body []byte, prRef string) (gitEvent, error) {
	switch event {
	case "push":
		var p struct {
			Ref        string `json:"ref"`
			After      string `json:"after"`
			Deleted    bool   `json:"deleted"`
			HeadCommit struct {
				Message string `json:"message"`
			} `json:"head_commit"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return gitEvent{}, err
		}
		if p.Deleted || !strings.HasPrefix(p.Ref, "refs/heads/") {
			return gitEvent{kind: "ignore"}, nil
		}
		return gitEvent{kind: "push", branch: strings.TrimPrefix(p.Ref, "refs/heads/"), sha: p.After,
			message: firstLine(p.HeadCommit.Message)}, nil
	case "pull_request":
		var p struct {
			Action      string `json:"action"`
			Number      int    `json:"number"`
			PullRequest struct {
				Title string `json:"title"`
				Head  struct {
					SHA  string  `json:"sha"`
					Repo *ghRepo `json:"repo"`
				} `json:"head"`
				Base struct {
					Ref  string  `json:"ref"`
					Repo *ghRepo `json:"repo"`
				} `json:"base"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return gitEvent{}, err
		}
		ev := gitEvent{branch: p.PullRequest.Base.Ref, pr: p.Number, sha: p.PullRequest.Head.SHA,
			message: p.PullRequest.Title, prRef: fmt.Sprintf(prRef, p.Number)}
		// A deleted fork has no head repo; treat it as a fork too.
		head, base := p.PullRequest.Head.Repo, p.PullRequest.Base.Repo
		ev.fork = head == nil || base == nil || head.FullName != base.FullName
		switch p.Action {
		case "opened", "reopened", "synchronize", "synchronized":
			ev.kind = "pr_open"
		case "closed":
			ev.kind = "pr_close"
		default:
			ev.kind = "ignore"
		}
		return ev, nil
	}
	return gitEvent{kind: "ignore"}, nil
}

func parseGitLab(event string, body []byte) (gitEvent, error) {
	switch event {
	case "Push Hook":
		var p struct {
			Ref         string `json:"ref"`
			CheckoutSHA string `json:"checkout_sha"`
			Commits     []struct {
				Message string `json:"message"`
			} `json:"commits"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return gitEvent{}, err
		}
		if p.CheckoutSHA == "" || !strings.HasPrefix(p.Ref, "refs/heads/") {
			return gitEvent{kind: "ignore"}, nil
		}
		ev := gitEvent{kind: "push", branch: strings.TrimPrefix(p.Ref, "refs/heads/"), sha: p.CheckoutSHA}
		if n := len(p.Commits); n > 0 {
			ev.message = firstLine(p.Commits[n-1].Message)
		}
		return ev, nil
	case "Merge Request Hook":
		var p struct {
			Attrs struct {
				IID             int    `json:"iid"`
				SourceProjectID int    `json:"source_project_id"`
				TargetProjectID int    `json:"target_project_id"`
				Action          string `json:"action"`
				Title           string `json:"title"`
				TargetBranch    string `json:"target_branch"`
				LastCommit      struct {
					ID string `json:"id"`
				} `json:"last_commit"`
			} `json:"object_attributes"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return gitEvent{}, err
		}
		a := p.Attrs
		ev := gitEvent{branch: a.TargetBranch, pr: a.IID, sha: a.LastCommit.ID, message: a.Title,
			prRef: fmt.Sprintf("refs/merge-requests/%d/head", a.IID), fork: a.SourceProjectID != a.TargetProjectID}
		switch a.Action {
		case "open", "reopen", "update":
			ev.kind = "pr_open"
		case "close", "merge":
			ev.kind = "pr_close"
		default:
			ev.kind = "ignore"
		}
		return ev, nil
	}
	return gitEvent{kind: "ignore"}, nil
}

func parseBitbucket(event string, body []byte) (gitEvent, error) {
	if event != "repo:push" {
		return gitEvent{kind: "ignore"}, nil
	}
	var p struct {
		Push struct {
			Changes []struct {
				New *struct {
					Type   string `json:"type"`
					Name   string `json:"name"`
					Target struct {
						Hash    string `json:"hash"`
						Message string `json:"message"`
					} `json:"target"`
				} `json:"new"`
			} `json:"changes"`
		} `json:"push"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return gitEvent{}, err
	}
	for _, c := range p.Push.Changes {
		if c.New != nil && c.New.Type == "branch" {
			return gitEvent{kind: "push", branch: c.New.Name, sha: c.New.Target.Hash,
				message: firstLine(c.New.Target.Message)}, nil
		}
	}
	return gitEvent{kind: "ignore"}, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
