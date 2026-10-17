package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/99proteam/selfdeploy/internal/store"
)

const (
	sessionCookie = "sd_session"
	sessionTTL    = 14 * 24 * time.Hour
)

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type credentials struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
	IsAdmin  bool   `json:"is_admin"`
}

func (c *credentials) validate() error {
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	if _, err := mail.ParseAddress(c.Email); err != nil {
		return invalid("a valid email is required")
	}
	if len(c.Password) < 8 {
		return invalid("password must be at least 8 characters")
	}
	return nil
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.CountUsers()
	if err != nil {
		fail(w, err)
		return
	}
	resp := map[string]any{"setup_required": n == 0, "version": s.Version}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if u, err := s.Store.UserBySession(c.Value); err == nil {
			resp["user"] = u
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// register creates the first (admin) account; later accounts are added by an admin.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := decode(r, &c); err != nil {
		fail(w, err)
		return
	}
	if err := c.validate(); err != nil {
		fail(w, err)
		return
	}
	if n, _ := s.Store.CountUsers(); n > 0 {
		writeErr(w, http.StatusForbidden, "registration is closed; ask an admin for an account")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
	if err != nil {
		fail(w, err)
		return
	}
	u, err := s.Store.CreateUser(c.Email, c.Name, string(hash), true)
	if err != nil {
		fail(w, err)
		return
	}
	s.startSession(w, r, u)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) {
	tok := newToken()
	if err := s.Store.CreateSession(tok, u.ID, sessionTTL); err != nil {
		fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds())})
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "too many login attempts, try again in a few minutes")
		return
	}
	var c credentials
	if err := decode(r, &c); err != nil {
		fail(w, err)
		return
	}
	u, err := s.Store.UserByEmail(strings.ToLower(strings.TrimSpace(c.Email)))
	if err != nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(c.Password)) != nil {
		s.limiter.fail(ip)
		writeErr(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	s.limiter.reset(ip)
	s.startSession(w, r, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, userFrom(r)) }

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	u := userFrom(r)
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body.Current)) != nil {
		writeErr(w, http.StatusBadRequest, "current password is wrong")
		return
	}
	if len(body.New) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(body.New), bcrypt.DefaultCost)
	if err := s.Store.UpdatePassword(u.ID, string(hash)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.APITokens(userFrom(r).ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		body.Name = "API token"
	}
	tok := "sd_" + newToken()
	if err := s.Store.CreateAPIToken(userFrom(r).ID, body.Name, tok); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"token": tok, "name": body.Name})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.DeleteAPIToken(userFrom(r).ID, id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.Users()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := decode(r, &c); err != nil {
		fail(w, err)
		return
	}
	if err := c.validate(); err != nil {
		fail(w, err)
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
	u, err := s.Store.CreateUser(c.Email, c.Name, string(hash), c.IsAdmin)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	if id == userFrom(r).ID {
		writeErr(w, http.StatusBadRequest, "you cannot delete your own account")
		return
	}
	if err := s.Store.DeleteUser(id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// loginLimiter allows 10 failed logins per IP per 15 minutes.
type loginLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{hits: map[string][]time.Time{}} }

func (l *loginLimiter) recent(ip string) []time.Time {
	cutoff := time.Now().Add(-15 * time.Minute)
	var keep []time.Time
	for _, t := range l.hits[ip] {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.hits, ip)
	} else {
		l.hits[ip] = keep
	}
	return keep
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip)) < 10
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hits[ip] = append(l.recent(ip), time.Now())
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, ip)
}
