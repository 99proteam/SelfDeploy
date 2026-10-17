// Package notify sends deployment and backup events to Discord, Slack,
// Telegram, generic webhooks and email.
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"github.com/99proteam/selfdeploy/internal/store"
)

const (
	DeploySuccess = "deploy_success"
	DeployFailed  = "deploy_failed"
	BackupFailed  = "backup_failed"
	BackupSuccess = "backup_success"
)

type Notifier struct {
	Store  *store.Store
	client *http.Client
}

func New(s *store.Store) *Notifier {
	return &Notifier{Store: s, client: &http.Client{Timeout: 15 * time.Second}}
}

// Send delivers an event to every enabled channel subscribed to it, in the background.
func (n *Notifier) Send(event, title, message string) {
	channels, err := n.Store.Notifications()
	if err != nil {
		return
	}
	for _, ch := range channels {
		if !ch.Enabled || !subscribed(ch.Events, event) {
			continue
		}
		go func(ch *store.Notification) {
			if err := n.Deliver(ch, title, message); err != nil {
				log.Printf("notify %s (%s): %v", ch.Name, ch.Kind, err)
			}
		}(ch)
	}
}

func subscribed(events, event string) bool {
	for _, e := range strings.Split(events, ",") {
		if strings.TrimSpace(e) == event {
			return true
		}
	}
	return false
}

// Deliver sends one message to one channel synchronously (also used by "send test").
func (n *Notifier) Deliver(ch *store.Notification, title, message string) error {
	text := title + "\n" + message
	switch ch.Kind {
	case "discord":
		return n.postJSON(ch.Target, map[string]any{"content": "**" + title + "**\n" + message})
	case "slack":
		return n.postJSON(ch.Target, map[string]any{"text": "*" + title + "*\n" + message})
	case "telegram":
		url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", ch.Target)
		return n.postJSON(url, map[string]any{"chat_id": ch.Extra, "text": text})
	case "webhook":
		return n.postJSON(ch.Target, map[string]any{"title": title, "message": message, "time": time.Now().UTC()})
	case "email":
		return n.sendEmail(ch.Target, title, message)
	}
	return fmt.Errorf("unknown notification kind %q", ch.Kind)
}

func (n *Notifier) postJSON(url string, body any) error {
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// sendEmail uses the SMTP settings (smtp_host, smtp_port, smtp_user, smtp_password, smtp_from).
func (n *Notifier) sendEmail(to, subject, body string) error {
	host := n.Store.Setting("smtp_host")
	if host == "" {
		return fmt.Errorf("SMTP is not configured in Settings")
	}
	port := n.Store.Setting("smtp_port")
	if port == "" {
		port = "587"
	}
	from := n.Store.Setting("smtp_from")
	user := n.Store.Setting("smtp_user")
	if from == "" {
		from = user
	}
	msg := []byte("From: " + from + "\r\nTo: " + to + "\r\nSubject: " + subject +
		"\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
	addr := net.JoinHostPort(host, port)
	var auth smtp.Auth
	if user != "" {
		auth = smtp.PlainAuth("", user, n.Store.Setting("smtp_password"), host)
	}
	if port != "465" {
		return smtp.SendMail(addr, auth, from, []string{to}, msg)
	}
	// Port 465 uses implicit TLS, which smtp.SendMail does not support.
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
