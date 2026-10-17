package api

import (
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	mailsrv "github.com/99proteam/selfdeploy/internal/mail"
)

func (s *Server) mailOverview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 20*time.Second)
	defer cancel()
	domains, err := s.Store.MailDomains()
	if err != nil {
		fail(w, err)
		return
	}
	boxes, _ := s.Store.Mailboxes()
	aliases, _ := s.Store.MailAliases()
	hostname := s.Mail.Hostname()
	ip := ""
	if srv, err := s.Store.LocalServer(); err == nil {
		ip = srv.PublicIP
	}
	var withDNS []map[string]any
	for _, d := range domains {
		withDNS = append(withDNS, map[string]any{"domain": d,
			"dns": mailsrv.DNSRecords(d.Domain, hostname, ip, d.DKIMRecord)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": s.Mail.Status(ctx), "hostname": hostname, "server_ip": ip,
		"domains": withDNS, "mailboxes": boxes, "aliases": aliases,
		"smtp": map[string]any{"host": hostname, "submission_port": 587, "smtps_port": 465, "imaps_port": 993,
			"internal_host": mailsrv.Container, "internal_port": 25},
	})
}

func (s *Server) mailEnable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hostname string `json:"hostname"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	host := strings.ToLower(strings.TrimSpace(body.Hostname))
	if !mailsrv.ValidDomain(host) {
		fail(w, invalid("enter a hostname like mail.example.com"))
		return
	}
	t := s.startTask("Set up mail server", 20*time.Minute, func(ctx context.Context, t *task) error {
		return s.Mail.Enable(ctx, host, t)
	})
	taskResponse(w, t)
}

func (s *Server) mailDisable(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, time.Minute)
	defer cancel()
	if err := s.Mail.Disable(ctx); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) mailAddDomain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain string `json:"domain"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	domain := strings.ToLower(strings.TrimSpace(body.Domain))
	if !mailsrv.ValidDomain(domain) {
		fail(w, invalid("invalid domain"))
		return
	}
	d, err := s.Store.CreateMailDomain(domain)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) mailDeleteDomain(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	d, err := s.Store.MailDomain(id)
	if err != nil {
		fail(w, err)
		return
	}
	boxes, _ := s.Store.Mailboxes()
	ctx, cancel := reqCtx(r, 2*time.Minute)
	defer cancel()
	for _, b := range boxes {
		if b.DomainID == d.ID {
			if err := s.Mail.DeleteMailbox(ctx, b.Address); err != nil {
				fail(w, err)
				return
			}
		}
	}
	if err := s.Store.DeleteMailDomain(id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mailDKIM (re)generates the domain's DKIM key; the mail server must be running
// and must already have at least one mailbox for the domain.
func (s *Server) mailDKIM(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	d, err := s.Store.MailDomain(id)
	if err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := reqCtx(r, 3*time.Minute)
	defer cancel()
	rec, err := s.Mail.GenerateDKIM(ctx, d.Domain)
	if err != nil {
		fail(w, err)
		return
	}
	s.Store.SetMailDomainDKIM(d.ID, rec)
	writeJSON(w, http.StatusOK, map[string]string{"dkim": rec})
}

func (s *Server) mailAddMailbox(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Address  string `json:"address"`
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	addr := strings.ToLower(strings.TrimSpace(body.Address))
	parsed, err := mail.ParseAddress(addr)
	if err != nil || parsed.Address != addr {
		fail(w, invalid("invalid email address"))
		return
	}
	if len(body.Password) < 8 || strings.ContainsAny(body.Password, "\n\r") {
		fail(w, invalid("password must be at least 8 characters"))
		return
	}
	domain := addr[strings.LastIndex(addr, "@")+1:]
	var domainID int64
	domains, _ := s.Store.MailDomains()
	for _, d := range domains {
		if d.Domain == domain {
			domainID = d.ID
		}
	}
	if domainID == 0 {
		fail(w, invalid("add the domain %s first", domain))
		return
	}
	ctx, cancel := reqCtx(r, 2*time.Minute)
	defer cancel()
	if err := s.Mail.AddMailbox(ctx, addr, body.Password); err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.CreateMailbox(domainID, addr); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"address": addr})
}

func (s *Server) mailPassword(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	box, err := s.Store.Mailbox(id)
	if err != nil {
		fail(w, err)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	if len(body.Password) < 8 || strings.ContainsAny(body.Password, "\n\r") {
		fail(w, invalid("password must be at least 8 characters"))
		return
	}
	ctx, cancel := reqCtx(r, time.Minute)
	defer cancel()
	if err := s.Mail.UpdatePassword(ctx, box.Address, body.Password); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) mailDeleteMailbox(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	box, err := s.Store.Mailbox(id)
	if err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := reqCtx(r, time.Minute)
	defer cancel()
	if err := s.Mail.DeleteMailbox(ctx, box.Address); err != nil {
		fail(w, err)
		return
	}
	s.Store.DeleteMailbox(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) mailAddAlias(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, err)
		return
	}
	src := strings.ToLower(strings.TrimSpace(body.Source))
	dst := strings.ToLower(strings.TrimSpace(body.Destination))
	for _, a := range []string{src, dst} {
		if p, err := mail.ParseAddress(a); err != nil || p.Address != a {
			fail(w, invalid("invalid address %q", a))
			return
		}
	}
	ctx, cancel := reqCtx(r, time.Minute)
	defer cancel()
	if err := s.Mail.AddAlias(ctx, src, dst); err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.CreateMailAlias(src, dst); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"source": src, "destination": dst})
}

func (s *Server) mailDeleteAlias(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	a, err := s.Store.MailAlias(id)
	if err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := reqCtx(r, time.Minute)
	defer cancel()
	if err := s.Mail.DeleteAlias(ctx, a.Source, a.Destination); err != nil {
		fail(w, fmt.Errorf("remove alias: %w", err))
		return
	}
	s.Store.DeleteMailAlias(id)
	w.WriteHeader(http.StatusNoContent)
}
