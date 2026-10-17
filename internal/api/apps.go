package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/99proteam/selfdeploy/internal/deploy"
	"github.com/99proteam/selfdeploy/internal/proxy"
	"github.com/99proteam/selfdeploy/internal/store"
)

var (
	envKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	branchRe  = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	limitRe   = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[bkmgBKMG]?$`)
	buildPack = map[string]bool{"auto": true, "dockerfile": true, "nixpacks": true, "static": true, "compose": true}
)

// normalizeApp validates an app and fills defaults.
func (s *Server) normalizeApp(a *store.App) error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return invalid("name is required")
	}
	if _, err := s.Store.Project(a.ProjectID); err != nil {
		return invalid("project not found")
	}
	if a.ServerID == 0 {
		srv, err := s.Store.LocalServer()
		if err != nil {
			return err
		}
		a.ServerID = srv.ID
	} else if _, err := s.Store.Server(a.ServerID); err != nil {
		return invalid("server not found")
	}
	switch a.SourceType {
	case "image":
		a.Image = strings.TrimSpace(a.Image)
		if a.Image == "" || strings.HasPrefix(a.Image, "-") || strings.ContainsAny(a.Image, " \t\n") {
			return invalid("a docker image is required, e.g. nginx:alpine or ghcr.io/user/app:latest")
		}
	case "git", "":
		a.SourceType = "git"
		a.GitURL = strings.TrimSpace(a.GitURL)
		if !(strings.HasPrefix(a.GitURL, "https://") || strings.HasPrefix(a.GitURL, "http://") ||
			strings.HasPrefix(a.GitURL, "git@") || strings.HasPrefix(a.GitURL, "ssh://")) {
			return invalid("git URL must start with https://, git@ or ssh://")
		}
		if a.GitBranch == "" {
			a.GitBranch = "main"
		}
		if !branchRe.MatchString(a.GitBranch) || strings.HasPrefix(a.GitBranch, "-") {
			return invalid("invalid branch name")
		}
	default:
		return invalid("source type must be git or image")
	}
	if a.BuildPack == "" {
		a.BuildPack = "auto"
	}
	if !buildPack[a.BuildPack] {
		return invalid("unknown build pack %q", a.BuildPack)
	}
	if a.BaseDir == "" {
		a.BaseDir = "."
	}
	if a.DockerfilePath == "" {
		a.DockerfilePath = "Dockerfile"
	}
	if a.ComposePath == "" {
		a.ComposePath = "docker-compose.yml"
	}
	if a.Port <= 0 || a.Port > 65535 {
		a.Port = 3000
	}
	if a.MemoryLimit != "" && !limitRe.MatchString(a.MemoryLimit) {
		return invalid("memory limit must look like 512m or 1g")
	}
	if a.CPULimit != "" && !regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`).MatchString(a.CPULimit) {
		return invalid("CPU limit must be a number like 0.5 or 2")
	}
	doms, err := proxy.ParseDomains(a.DomainList())
	if err != nil {
		return invalid("%v", err)
	}
	var clean []string
	for _, d := range doms {
		clean = append(clean, d.URL())
	}
	a.Domains = strings.Join(clean, ",")
	return nil
}

func randomSecret() string {
	b := make([]byte, 20)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// defaultDomain gives new resources a working URL immediately: a wildcard
// subdomain if configured, otherwise a free sslip.io temporary domain.
func (s *Server) defaultDomain(slug string, serverID int64) string {
	ip := ""
	if srv, err := s.Store.Server(serverID); err == nil {
		ip = srv.PublicIP
	}
	return proxy.GenerateDomain(slug, s.Store.Setting("wildcard_domain"), ip)
}

// appView hides the git token and adds the webhook URLs.
func (s *Server) appView(r *http.Request, a *store.App) map[string]any {
	hasToken := a.GitToken != ""
	a.GitToken = ""
	base := s.baseURL(r)
	return map[string]any{
		"app":           a,
		"has_git_token": hasToken,
		"webhooks": map[string]string{
			"github":    fmt.Sprintf("%s/webhooks/github/%d", base, a.ID),
			"gitlab":    fmt.Sprintf("%s/webhooks/gitlab/%d", base, a.ID),
			"gitea":     fmt.Sprintf("%s/webhooks/gitea/%d", base, a.ID),
			"bitbucket": fmt.Sprintf("%s/webhooks/bitbucket/%d?secret=%s", base, a.ID, a.WebhookSecret),
			"deploy":    fmt.Sprintf("%s/webhooks/deploy/%d?secret=%s", base, a.ID, a.WebhookSecret),
		},
	}
}

// baseURL is the public address of this dashboard, used in webhook URLs.
func (s *Server) baseURL(r *http.Request) string {
	if d := s.Store.Setting("instance_domain"); d != "" {
		if dom, err := proxy.ParseDomain(d); err == nil {
			return dom.URL()
		}
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.Store.Apps(int64(queryInt(r, "project", 0)))
	if err != nil {
		fail(w, err)
		return
	}
	for _, a := range apps {
		a.GitToken = ""
	}
	writeJSON(w, http.StatusOK, apps)
}

func (s *Server) createApp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		store.App
		Deploy bool `json:"deploy"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	a := &body.App
	if err := s.normalizeApp(a); err != nil {
		fail(w, err)
		return
	}
	a.Slug = s.slugFor(a.Name)
	a.WebhookSecret = randomSecret()
	a.AutoDeploy = true
	if a.Domains == "" {
		a.Domains = s.defaultDomain(a.Slug, a.ServerID)
	}
	if err := s.Store.CreateApp(a); err != nil {
		fail(w, err)
		return
	}
	if body.Deploy {
		if _, err := s.Deploy.Enqueue(a, deploy.Options{Trigger: "manual"}); err != nil {
			fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, s.appView(r, a))
}

func (s *Server) loadApp(w http.ResponseWriter, r *http.Request) *store.App {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return nil
	}
	a, err := s.Store.App(id)
	if err != nil {
		fail(w, err)
		return nil
	}
	return a
}

func (s *Server) getApp(w http.ResponseWriter, r *http.Request) {
	if a := s.loadApp(w, r); a != nil {
		writeJSON(w, http.StatusOK, s.appView(r, a))
	}
}

func (s *Server) updateApp(w http.ResponseWriter, r *http.Request) {
	a := s.loadApp(w, r)
	if a == nil {
		return
	}
	var body struct {
		store.App
		ClearGitToken bool `json:"clear_git_token"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	upd := body.App
	upd.ID, upd.Slug, upd.WebhookSecret = a.ID, a.Slug, a.WebhookSecret
	// An empty token in the form means "unchanged", since the UI never sees the stored one.
	if upd.GitToken == "" && !body.ClearGitToken {
		upd.GitToken = a.GitToken
	}
	if err := s.normalizeApp(&upd); err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.UpdateApp(&upd); err != nil {
		fail(w, err)
		return
	}
	fresh, _ := s.Store.App(a.ID)
	writeJSON(w, http.StatusOK, s.appView(r, fresh))
}

func (s *Server) deleteApp(w http.ResponseWriter, r *http.Request) {
	a := s.loadApp(w, r)
	if a == nil {
		return
	}
	if err := s.Deploy.Destroy(a, r.URL.Query().Get("volumes") == "1"); err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.DeleteApp(a.ID); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deployApp(w http.ResponseWriter, r *http.Request) {
	a := s.loadApp(w, r)
	if a == nil {
		return
	}
	trigger := "manual"
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		trigger = "api"
	}
	d, err := s.Deploy.Enqueue(a, deploy.Options{Trigger: trigger})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

func (s *Server) appAction(w http.ResponseWriter, r *http.Request) {
	a := s.loadApp(w, r)
	if a == nil {
		return
	}
	var err error
	switch r.PathValue("action") {
	case "stop":
		err = s.Deploy.Stop(a)
	case "start":
		err = s.Deploy.Start(a)
	case "restart":
		err = s.Deploy.Restart(a)
	case "generate-domain":
		a.Domains = strings.Trim(a.Domains+","+s.defaultDomain(a.Slug, a.ServerID), ",")
		err = s.Store.UpdateApp(a)
	default:
		writeErr(w, http.StatusNotFound, "unknown action")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	fresh, _ := s.Store.App(a.ID)
	writeJSON(w, http.StatusOK, s.appView(r, fresh))
}

func (s *Server) appLogs(w http.ResponseWriter, r *http.Request) {
	a := s.loadApp(w, r)
	if a == nil {
		return
	}
	logs, err := s.Deploy.Logs(a, queryInt(r, "tail", 300))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"logs": "", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": logs})
}

func (s *Server) getEnv(w http.ResponseWriter, r *http.Request) {
	a := s.loadApp(w, r)
	if a == nil {
		return
	}
	vars, err := s.Store.EnvVars(a.ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vars)
}

func (s *Server) putEnv(w http.ResponseWriter, r *http.Request) {
	a := s.loadApp(w, r)
	if a == nil {
		return
	}
	var vars []*store.EnvVar
	if err := decode(r, &vars); err != nil {
		fail(w, err)
		return
	}
	seen := map[string]bool{}
	for _, v := range vars {
		v.Key = strings.TrimSpace(v.Key)
		if !envKeyRe.MatchString(v.Key) {
			fail(w, invalid("invalid variable name %q", v.Key))
			return
		}
		if seen[v.Key] {
			fail(w, invalid("duplicate variable %q", v.Key))
			return
		}
		seen[v.Key] = true
	}
	if err := s.Store.ReplaceEnvVars(a.ID, vars); err != nil {
		fail(w, err)
		return
	}
	s.getEnv(w, r)
}

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	a := s.loadApp(w, r)
	if a == nil {
		return
	}
	list, err := s.Store.Deployments(a.ID, queryInt(r, "limit", 30))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	d, err := s.Store.Deployment(id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// rollback redeploys the exact image a previous successful deployment built.
func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	d, err := s.Store.Deployment(id)
	if err != nil {
		fail(w, err)
		return
	}
	if d.Status != "success" || d.Image == "" || d.PRNumber > 0 {
		writeErr(w, http.StatusBadRequest, "only successful, non-preview deployments with an image can be rolled back to")
		return
	}
	a, err := s.Store.App(d.AppID)
	if err != nil {
		fail(w, err)
		return
	}
	nd, err := s.Deploy.Enqueue(a, deploy.Options{Trigger: "rollback", RollbackImage: d.Image,
		CommitSHA: d.CommitSHA, CommitMessage: "Rollback to deployment #" + fmt.Sprint(d.ID)})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, nd)
}
