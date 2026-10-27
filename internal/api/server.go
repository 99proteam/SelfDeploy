// Package api serves the REST API, git webhooks and the embedded dashboard.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/99proteam/selfdeploy/internal/databases"
	"github.com/99proteam/selfdeploy/internal/deploy"
	"github.com/99proteam/selfdeploy/internal/mail"
	"github.com/99proteam/selfdeploy/internal/notify"
	"github.com/99proteam/selfdeploy/internal/services"
	"github.com/99proteam/selfdeploy/internal/store"
)

type Server struct {
	Store    *store.Store
	Deploy   *deploy.Engine
	Backups  *databases.Backups
	Services *services.Manager
	Mail     *mail.Manager
	Notify   *notify.Notifier
	DataDir  string
	Version  string
	Web      fs.FS

	SSHPublicKey string

	tasks   sync.Map // id -> *task
	limiter *loginLimiter
}

type ctxKey struct{}

func userFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxKey{}).(*store.User)
	return u
}

// Handler wires every route.
func (s *Server) Handler() http.Handler {
	s.limiter = newLoginLimiter()
	mux := http.NewServeMux()

	// public
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/register", s.register)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.HandleFunc("POST /webhooks/{provider}/{id}", s.webhook)
	mux.HandleFunc("GET /webhooks/deploy/{id}", s.webhook)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// authenticated
	a := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.auth(h, false)) }
	admin := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.auth(h, true)) }

	a("GET /api/me", s.me)
	a("POST /api/me/password", s.changePassword)
	a("GET /api/tokens", s.listTokens)
	a("POST /api/tokens", s.createToken)
	a("DELETE /api/tokens/{id}", s.deleteToken)
	admin("GET /api/users", s.listUsers)
	admin("POST /api/users", s.createUser)
	admin("DELETE /api/users/{id}", s.deleteUser)

	a("GET /api/overview", s.overview)
	a("GET /api/tasks/{id}", s.getTask)
	admin("GET /api/settings", s.getSettings)
	admin("PUT /api/settings", s.putSettings)

	a("GET /api/servers", s.listServers)
	admin("POST /api/servers", s.createServer)
	admin("PUT /api/servers/{id}", s.updateServer)
	admin("DELETE /api/servers/{id}", s.deleteServer)
	a("POST /api/servers/{id}/validate", s.validateServer)
	a("POST /api/servers/{id}/proxy", s.installProxy)
	a("GET /api/servers/{id}/stats", s.serverStats)
	a("GET /api/ssh-key", s.sshKey)

	a("GET /api/projects", s.listProjects)
	a("POST /api/projects", s.createProject)
	a("GET /api/projects/{id}", s.getProject)
	a("PUT /api/projects/{id}", s.updateProject)
	a("DELETE /api/projects/{id}", s.deleteProject)

	a("GET /api/apps", s.listApps)
	a("POST /api/apps", s.createApp)
	a("GET /api/apps/{id}", s.getApp)
	a("PUT /api/apps/{id}", s.updateApp)
	a("DELETE /api/apps/{id}", s.deleteApp)
	a("POST /api/apps/{id}/deploy", s.deployApp)
	a("POST /api/apps/{id}/clone", s.cloneApp)
	a("POST /api/apps/{id}/{action}", s.appAction)
	a("GET /api/apps/{id}/logs", s.appLogs)
	a("GET /api/apps/{id}/env", s.getEnv)
	a("PUT /api/apps/{id}/env", s.putEnv)
	a("GET /api/apps/{id}/deployments", s.listDeployments)
	a("GET /api/deployments/{id}", s.getDeployment)
	a("POST /api/deployments/{id}/rollback", s.rollback)

	a("GET /api/databases", s.listDatabases)
	a("POST /api/databases", s.createDatabase)
	a("GET /api/databases/{id}", s.getDatabase)
	a("PUT /api/databases/{id}", s.updateDatabase)
	a("DELETE /api/databases/{id}", s.deleteDatabase)
	a("POST /api/databases/{id}/{action}", s.databaseAction)
	a("GET /api/databases/{id}/logs", s.databaseLogs)
	a("GET /api/databases/{id}/backups", s.listBackups)
	a("POST /api/backups/{id}/restore", s.restoreBackup)
	a("GET /api/backups/{id}/download", s.downloadBackup)
	a("DELETE /api/backups/{id}", s.deleteBackup)

	a("GET /api/templates", s.listTemplates)
	a("GET /api/services", s.listServices)
	a("POST /api/services", s.createService)
	a("GET /api/services/{id}", s.getService)
	a("PUT /api/services/{id}", s.updateService)
	a("DELETE /api/services/{id}", s.deleteService)
	a("POST /api/services/{id}/{action}", s.serviceAction)
	a("GET /api/services/{id}/logs", s.serviceLogs)

	a("GET /api/mail", s.mailOverview)
	admin("POST /api/mail/enable", s.mailEnable)
	admin("POST /api/mail/disable", s.mailDisable)
	a("POST /api/mail/domains", s.mailAddDomain)
	a("DELETE /api/mail/domains/{id}", s.mailDeleteDomain)
	a("POST /api/mail/domains/{id}/dkim", s.mailDKIM)
	a("POST /api/mail/mailboxes", s.mailAddMailbox)
	a("PUT /api/mail/mailboxes/{id}/password", s.mailPassword)
	a("DELETE /api/mail/mailboxes/{id}", s.mailDeleteMailbox)
	a("POST /api/mail/aliases", s.mailAddAlias)
	a("DELETE /api/mail/aliases/{id}", s.mailDeleteAlias)

	a("GET /api/notifications", s.listNotifications)
	a("POST /api/notifications", s.createNotification)
	a("PUT /api/notifications/{id}", s.updateNotification)
	a("DELETE /api/notifications/{id}", s.deleteNotification)
	a("POST /api/notifications/{id}/test", s.testNotification)

	mux.Handle("/", s.static())
	return securityHeaders(mux)
}

// static serves the single-page dashboard, falling back to index.html.
func (s *Server) static() http.Handler {
	files := http.FileServer(http.FS(s.Web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if r.URL.Path != "/" {
			if _, err := fs.Stat(s.Web, strings.TrimPrefix(r.URL.Path, "/")); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(s.Web, "index.html")
		if err != nil {
			http.Error(w, "dashboard not built", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// auth accepts a session cookie or "Authorization: Bearer <api token>".
// Cookie-authenticated writes must carry the X-SelfDeploy header, which a
// cross-site form or image cannot send, so they are safe from CSRF.
func (s *Server) auth(h http.HandlerFunc, adminOnly bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u *store.User
		var err error
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			u, err = s.Store.UserByAPIToken(strings.TrimSpace(tok))
		} else if c, cerr := r.Cookie(sessionCookie); cerr == nil {
			u, err = s.Store.UserBySession(c.Value)
			if err == nil && r.Method != http.MethodGet && r.Header.Get("X-SelfDeploy") == "" {
				writeErr(w, http.StatusForbidden, "missing X-SelfDeploy header")
				return
			}
		} else {
			err = store.ErrNotFound
		}
		if err != nil || u == nil {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if adminOnly && !u.IsAdmin {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail maps store and validation errors to HTTP responses.
func fail(w http.ResponseWriter, err error) {
	var ve validationError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.As(err, &ve):
		writeErr(w, http.StatusBadRequest, ve.Error())
	case strings.Contains(err.Error(), "UNIQUE constraint failed"):
		writeErr(w, http.StatusConflict, "already exists")
	default:
		log.Printf("api: %v", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

type validationError string

func (v validationError) Error() string { return string(v) }

func invalid(format string, args ...any) error { return validationError(fmt.Sprintf(format, args...)) }

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return invalid("invalid JSON body: %v", err)
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, invalid("invalid id")
	}
	return id, nil
}

func queryInt(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil && v > 0 {
		return v
	}
	return def
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slugFor turns a name into a unique DNS- and docker-safe identifier.
func (s *Server) slugFor(name string) string {
	base := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(base) > 32 {
		base = strings.Trim(base[:32], "-")
	}
	if base == "" {
		base = "app"
	}
	if !s.Store.SlugTaken(base) && !reserved[base] {
		return base
	}
	for {
		cand := base + "-" + strings.ToLower(services.RandomString(4))
		if !s.Store.SlugTaken(cand) {
			return cand
		}
	}
}

var reserved = map[string]bool{"selfdeploy": true, "selfdeploy-proxy": true, "selfdeploy-mail": true}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- background tasks (long operations the UI polls) ----

type task struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Status  string    `json:"status"` // running | success | failed
	Error   string    `json:"error,omitempty"`
	Started time.Time `json:"started"`
	mu      sync.Mutex
	buf     bytes.Buffer
}

func (t *task) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.Write(p)
}

func (s *Server) startTask(name string, timeout time.Duration, fn func(ctx context.Context, t *task) error) *task {
	t := &task{ID: services.RandomString(16), Name: name, Status: "running", Started: time.Now()}
	s.tasks.Store(t.ID, t)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		err := fn(ctx, t)
		t.mu.Lock()
		t.Status = "success"
		if err != nil {
			t.Status, t.Error = "failed", err.Error()
			fmt.Fprintf(&t.buf, "\nError: %v\n", err)
		}
		t.mu.Unlock()
		// Forget finished tasks after an hour.
		time.AfterFunc(time.Hour, func() { s.tasks.Delete(t.ID) })
	}()
	return t
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	v, ok := s.tasks.Load(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	t := v.(*task)
	t.mu.Lock()
	defer t.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"id": t.ID, "name": t.Name, "status": t.Status, "error": t.Error,
		"log": t.buf.String(), "started": t.Started})
}

func taskResponse(w http.ResponseWriter, t *task) {
	writeJSON(w, http.StatusAccepted, map[string]string{"task_id": t.ID})
}
