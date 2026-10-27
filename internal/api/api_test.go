package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/99proteam/selfdeploy/internal/deploy"
	"github.com/99proteam/selfdeploy/internal/notify"
	"github.com/99proteam/selfdeploy/internal/store"
)

type client struct {
	t   *testing.T
	srv *httptest.Server
	hc  *http.Client
}

func (c *client) do(method, path string, body any, headers ...string) (int, map[string]any) {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SelfDeploy", "1")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// newTestServer runs the API against a temp database. The deploy engine has
// no workers, so queued deployments are recorded but never executed.
func newTestServer(t *testing.T) (*client, *Server) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateServer(&store.Server{Name: "localhost", IsLocal: true, PublicIP: "203.0.113.7"}); err != nil {
		t.Fatal(err)
	}
	n := notify.New(st)
	s := &Server{Store: st, Notify: n, Deploy: deploy.New(st, n, dir), DataDir: dir, Version: "test",
		Web: fstest.MapFS{"index.html": {Data: []byte("<html>dashboard</html>")}}}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &client{t: t, srv: ts, hc: &http.Client{Jar: jar}}, s
}

func TestAuthFlow(t *testing.T) {
	c, _ := newTestServer(t)
	if code, out := c.do("GET", "/api/auth/status", nil); code != 200 || out["setup_required"] != true {
		t.Fatalf("status: %d %v", code, out)
	}
	if code, _ := c.do("GET", "/api/apps", nil); code != 401 {
		t.Fatalf("unauthenticated apps: %d", code)
	}
	if code, _ := c.do("POST", "/api/auth/register", map[string]string{"email": "a@example.com", "password": "short"}); code != 400 {
		t.Fatalf("weak password accepted: %d", code)
	}
	if code, out := c.do("POST", "/api/auth/register", map[string]string{"email": "a@example.com", "password": "longenough"}); code != 200 || out["is_admin"] != true {
		t.Fatalf("register: %d %v", code, out)
	}
	if code, _ := c.do("POST", "/api/auth/register", map[string]string{"email": "b@example.com", "password": "longenough"}); code != 403 {
		t.Fatalf("second registration allowed: %d", code)
	}
	if code, _ := c.do("GET", "/api/apps", nil); code != 200 {
		t.Fatalf("session not accepted: %d", code)
	}
	// Cookie-authenticated writes need the custom header (CSRF protection).
	req, _ := http.NewRequest("POST", c.srv.URL+"/api/projects", strings.NewReader(`{"name":"x"}`))
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("write without X-SelfDeploy header: %d", resp.StatusCode)
	}
	// API tokens work without cookies or the header.
	_, tok := c.do("POST", "/api/tokens", map[string]string{"name": "ci"})
	bare := &client{t: t, srv: c.srv, hc: &http.Client{}}
	if code, _ := bare.do("GET", "/api/projects", nil, "Authorization", "Bearer "+tok["token"].(string)); code != 200 {
		t.Fatalf("api token rejected: %d", code)
	}
	if code, _ := bare.do("GET", "/api/projects", nil, "Authorization", "Bearer wrong"); code != 401 {
		t.Fatalf("bad api token accepted: %d", code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	c, _ := newTestServer(t)
	c.do("POST", "/api/auth/register", map[string]string{"email": "a@example.com", "password": "longenough"})
	for i := 0; i < 10; i++ {
		c.do("POST", "/api/auth/login", map[string]string{"email": "a@example.com", "password": "wrong"})
	}
	if code, _ := c.do("POST", "/api/auth/login", map[string]string{"email": "a@example.com", "password": "longenough"}); code != 429 {
		t.Fatalf("expected rate limit, got %d", code)
	}
}

func setupApp(t *testing.T, c *client, app map[string]any) map[string]any {
	t.Helper()
	c.do("POST", "/api/auth/register", map[string]string{"email": "a@example.com", "password": "longenough"})
	_, p := c.do("POST", "/api/projects", map[string]string{"name": "Demo"})
	app["project_id"] = p["id"]
	code, out := c.do("POST", "/api/apps", app)
	if code != 201 {
		t.Fatalf("create app: %d %v", code, out)
	}
	return out["app"].(map[string]any)
}

func TestCreateAppDefaults(t *testing.T) {
	c, _ := newTestServer(t)
	a := setupApp(t, c, map[string]any{"name": "My Blog!", "git_url": "https://github.com/example/blog.git"})
	if a["slug"] != "my-blog" || a["domains"] != "http://my-blog.203.0.113.7.sslip.io" || a["git_branch"] != "main" {
		t.Fatalf("unexpected defaults: %v", a)
	}
	// A second app with the same name gets a unique slug.
	_, out := c.do("POST", "/api/apps", map[string]any{"name": "My Blog!", "project_id": a["project_id"], "source_type": "image", "image": "nginx"})
	if s := out["app"].(map[string]any)["slug"].(string); s == "my-blog" || !strings.HasPrefix(s, "my-blog-") {
		t.Fatalf("slug not unique: %s", s)
	}
	for _, bad := range []map[string]any{
		{"name": "x", "git_url": "file:///etc"},
		{"name": "x", "git_url": "https://github.com/a/b", "git_branch": "--upload-pack=evil"},
		{"name": "x", "source_type": "image", "image": "-v /:/host"},
		{"name": "x", "git_url": "https://github.com/a/b", "domains": "bad domain"},
		{"name": "x", "git_url": "https://github.com/a/b", "memory_limit": "lots"},
	} {
		bad["project_id"] = a["project_id"]
		if code, _ := c.do("POST", "/api/apps", bad); code != 400 {
			t.Errorf("accepted invalid app %v: %d", bad, code)
		}
	}
}

func TestGitTokenIsNeverReturned(t *testing.T) {
	c, s := newTestServer(t)
	a := setupApp(t, c, map[string]any{"name": "private", "git_url": "https://github.com/example/private.git", "git_token": "ghp_secret"})
	if _, ok := a["git_token"]; ok {
		t.Fatal("token returned on create")
	}
	id := int64(a["id"].(float64))
	// Saving the form without a token keeps the stored one.
	a["git_token"] = ""
	c.do("PUT", fmt.Sprintf("/api/apps/%d", id), a)
	stored, _ := s.Store.App(id)
	if stored.GitToken != "ghp_secret" {
		t.Fatalf("token lost on update: %q", stored.GitToken)
	}
}

func TestGitHubWebhook(t *testing.T) {
	c, s := newTestServer(t)
	a := setupApp(t, c, map[string]any{"name": "web", "git_url": "https://github.com/example/web.git"})
	id := int64(a["id"].(float64))
	secret := a["webhook_secret"].(string)
	push := []byte(`{"ref":"refs/heads/main","after":"abc123","head_commit":{"message":"fix: things\n\nbody"}}`)

	send := func(body []byte, sig, event string) int {
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/webhooks/github/%d", c.srv.URL, id), bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-Hub-Signature-256", "sha256="+sig)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := send(push, hmacHex("wrong", push), "push"); code != 401 {
		t.Fatalf("bad signature: %d", code)
	}
	if code := send(push, hmacHex(secret, push), "push"); code != 202 {
		t.Fatalf("valid push: %d", code)
	}
	deps, _ := s.Store.Deployments(id, 10)
	if len(deps) != 1 || deps[0].CommitSHA != "abc123" || deps[0].CommitMessage != "fix: things" || deps[0].Trigger != "webhook" {
		t.Fatalf("deployment not recorded correctly: %+v", deps)
	}
	other := []byte(`{"ref":"refs/heads/dev","after":"def"}`)
	if code := send(other, hmacHex(secret, other), "push"); code != 200 {
		t.Fatalf("other branch: %d", code)
	}
	// Pull requests only deploy previews when enabled.
	pr := []byte(`{"action":"opened","number":7,"pull_request":{"title":"t","head":{"sha":"fff","repo":{"full_name":"example/web"}},"base":{"ref":"main","repo":{"full_name":"example/web"}}}}`)
	send(pr, hmacHex(secret, pr), "pull_request")
	if deps, _ := s.Store.Deployments(id, 10); len(deps) != 1 {
		t.Fatalf("preview deployed while disabled")
	}
	app, _ := s.Store.App(id)
	app.PreviewDeploys = true
	s.Store.UpdateApp(app)
	send(pr, hmacHex(secret, pr), "pull_request")
	deps, _ = s.Store.Deployments(id, 10)
	if len(deps) != 2 || deps[0].PRNumber != 7 || deps[0].GitRef != "refs/pull/7/head" {
		t.Fatalf("preview not queued: %+v", deps[0])
	}
	// Pull requests from forks never run on the server.
	fork := []byte(`{"action":"opened","number":8,"pull_request":{"title":"t","head":{"sha":"eee","repo":{"full_name":"stranger/web"}},"base":{"ref":"main","repo":{"full_name":"example/web"}}}}`)
	send(fork, hmacHex(secret, fork), "pull_request")
	if deps, _ := s.Store.Deployments(id, 10); len(deps) != 2 {
		t.Fatalf("fork PR was deployed: %+v", deps[0])
	}
}

func TestParseGitLabAndBitbucket(t *testing.T) {
	ev, err := parseGitLab("Push Hook", []byte(`{"ref":"refs/heads/main","checkout_sha":"aaa","commits":[{"message":"one"},{"message":"two"}]}`))
	if err != nil || ev.kind != "push" || ev.branch != "main" || ev.sha != "aaa" || ev.message != "two" {
		t.Fatalf("gitlab push: %+v %v", ev, err)
	}
	ev, _ = parseGitLab("Merge Request Hook", []byte(`{"object_attributes":{"iid":3,"action":"merge","target_branch":"main"}}`))
	if ev.kind != "pr_close" || ev.pr != 3 {
		t.Fatalf("gitlab mr: %+v", ev)
	}
	ev, _ = parseGitLab("Merge Request Hook", []byte(`{"object_attributes":{"iid":4,"action":"open","target_branch":"main","source_project_id":9,"target_project_id":1}}`))
	if ev.kind != "pr_open" || !ev.fork {
		t.Fatalf("gitlab fork mr: %+v", ev)
	}
	ev, _ = parseBitbucket("repo:push", []byte(`{"push":{"changes":[{"new":{"type":"branch","name":"main","target":{"hash":"bbb","message":"m"}}}]}}`))
	if ev.kind != "push" || ev.branch != "main" || ev.sha != "bbb" {
		t.Fatalf("bitbucket: %+v", ev)
	}
	ev, _ = parseGitHub("push", []byte(`{"ref":"refs/heads/main","deleted":true}`), "")
	if ev.kind != "ignore" {
		t.Fatalf("branch deletion should be ignored: %+v", ev)
	}
}

func TestEnvValidation(t *testing.T) {
	c, _ := newTestServer(t)
	a := setupApp(t, c, map[string]any{"name": "web", "git_url": "https://github.com/example/web.git"})
	path := fmt.Sprintf("/api/apps/%d/env", int64(a["id"].(float64)))
	req := func(vars any) int {
		var buf bytes.Buffer
		json.NewEncoder(&buf).Encode(vars)
		r, _ := http.NewRequest("PUT", c.srv.URL+path, &buf)
		r.Header.Set("X-SelfDeploy", "1")
		resp, err := c.hc.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := req([]map[string]any{{"key": "DATABASE_URL", "value": "postgres://x"}, {"key": "NODE_ENV", "value": "production", "is_build": true}}); code != 200 {
		t.Fatalf("valid env: %d", code)
	}
	if code := req([]map[string]any{{"key": "BAD KEY", "value": "x"}}); code != 400 {
		t.Fatalf("invalid key accepted: %d", code)
	}
	if code := req([]map[string]any{{"key": "A", "value": "1"}, {"key": "A", "value": "2"}}); code != 400 {
		t.Fatalf("duplicate key accepted: %d", code)
	}
}

func TestDashboardFallback(t *testing.T) {
	c, _ := newTestServer(t)
	resp, err := http.Get(c.srv.URL + "/some/deep/link")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(buf.String(), "dashboard") {
		t.Fatalf("SPA fallback: %d %s", resp.StatusCode, buf.String())
	}
	if resp, _ := http.Get(c.srv.URL + "/api/nope"); resp.StatusCode != 404 {
		t.Fatalf("unknown API path: %d", resp.StatusCode)
	}
}

func TestPasswordProtection(t *testing.T) {
	c, s := newTestServer(t)
	a := setupApp(t, c, map[string]any{"name": "staging", "git_url": "https://github.com/example/web.git"})
	id := int64(a["id"].(float64))
	path := fmt.Sprintf("/api/apps/%d", id)

	a["basic_auth_user"] = "team"
	if code, _ := c.do("PUT", path, a); code != 400 {
		t.Fatalf("protection enabled without a password: %d", code)
	}
	a["basic_auth_password"] = "short"
	if code, _ := c.do("PUT", path, a); code != 400 {
		t.Fatalf("short password accepted: %d", code)
	}
	a["basic_auth_password"] = "correct horse"
	code, out := c.do("PUT", path, a)
	if code != 200 || out["password_protected"] != true {
		t.Fatalf("enable protection: %d %v", code, out)
	}
	stored, _ := s.Store.App(id)
	if !strings.HasPrefix(stored.BasicAuth(), "team:$2") {
		t.Fatalf("expected a bcrypt hash, got %q", stored.BasicAuth())
	}
	if strings.Contains(fmt.Sprint(out), stored.BasicAuthHash) {
		t.Fatal("password hash returned to the client")
	}
	// Saving the form again without a password keeps the stored hash.
	delete(a, "basic_auth_password")
	c.do("PUT", path, a)
	if again, _ := s.Store.App(id); again.BasicAuthHash != stored.BasicAuthHash {
		t.Fatal("hash lost on update")
	}
	// Clearing the username turns protection off.
	a["basic_auth_user"] = ""
	if _, out := c.do("PUT", path, a); out["password_protected"] != false {
		t.Fatalf("protection not removed: %v", out)
	}
}

func TestCloneApp(t *testing.T) {
	c, s := newTestServer(t)
	a := setupApp(t, c, map[string]any{"name": "web", "git_url": "https://github.com/example/web.git",
		"git_token": "ghp_secret", "build_command": "npm run build", "port": 8080})
	id := int64(a["id"].(float64))
	s.Store.ReplaceEnvVars(id, []*store.EnvVar{{Key: "NODE_ENV", Value: "production", IsBuild: true}})

	code, out := c.do("POST", fmt.Sprintf("/api/apps/%d/clone", id), map[string]any{"name": "web staging"})
	if code != 201 {
		t.Fatalf("clone: %d %v", code, out)
	}
	cp := out["app"].(map[string]any)
	cid := int64(cp["id"].(float64))
	if cid == id || cp["slug"] != "web-staging" || cp["webhook_secret"] == a["webhook_secret"] {
		t.Fatalf("clone must get its own identity: %v", cp)
	}
	if cp["domains"] != "http://web-staging.203.0.113.7.sslip.io" || cp["build_command"] != "npm run build" || cp["port"] != float64(8080) {
		t.Fatalf("unexpected clone settings: %v", cp)
	}
	if stored, _ := s.Store.App(cid); stored.GitToken != "ghp_secret" {
		t.Fatal("git token not copied")
	}
	vars, _ := s.Store.EnvVars(cid)
	if len(vars) != 1 || vars[0].Key != "NODE_ENV" || !vars[0].IsBuild {
		t.Fatalf("env vars not copied: %+v", vars)
	}
	// Without a name the copy is called "<name> copy".
	if _, out := c.do("POST", fmt.Sprintf("/api/apps/%d/clone", id), map[string]any{}); out["app"].(map[string]any)["name"] != "web copy" {
		t.Fatalf("default clone name: %v", out)
	}
}
