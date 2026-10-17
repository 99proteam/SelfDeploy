// Package services deploys one-click open-source apps from compose templates.
package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/99proteam/selfdeploy/internal/docker"
	"github.com/99proteam/selfdeploy/internal/proxy"
	"github.com/99proteam/selfdeploy/internal/store"
)

type Manager struct {
	Store   *store.Store
	DataDir string
}

var tokenRe = regexp.MustCompile(`\{\{([A-Z_0-9]+)\}\}`)

const alnum = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// RandomString returns a password made of unambiguous letters and digits.
func RandomString(n int) string {
	b := make([]byte, n)
	for i := range b {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alnum))))
		b[i] = alnum[k.Int64()]
	}
	return string(b)
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// GenerateSecrets creates every PASSWORD*/SECRET_KEY* value a template needs.
func GenerateSecrets(t Template) map[string]string {
	out := map[string]string{}
	for _, m := range tokenRe.FindAllStringSubmatch(t.Compose, -1) {
		name := m[1]
		if _, ok := out[name]; ok {
			continue
		}
		switch {
		case strings.HasPrefix(name, "PASSWORD"):
			out[name] = RandomString(24)
		case strings.HasPrefix(name, "SECRET_KEY"):
			out[name] = randomHex(32)
		}
	}
	return out
}

func (m *Manager) dir(svc *store.Service) string {
	return filepath.Join(m.DataDir, "services", svc.Slug)
}

func (m *Manager) client(svc *store.Service) (*docker.Client, error) {
	srv, err := m.Store.Server(svc.ServerID)
	if err != nil {
		return nil, err
	}
	return docker.ForServer(srv.IsLocal, srv.User, srv.Host, srv.Port), nil
}

// Render fills a template's placeholders for a service.
func (m *Manager) Render(t Template, svc *store.Service, text string) (string, error) {
	vars := map[string]string{}
	if svc.Variables != "" {
		if err := json.Unmarshal([]byte(svc.Variables), &vars); err != nil {
			return "", err
		}
	}
	vars["NAME"] = svc.Slug
	vars["MAIL_HOST"] = m.Store.Setting("mail_hostname")
	vars["URL"], vars["HOST"], vars["SCHEME"], vars["SECURE"] = "", "", "http", "false"
	if svc.Domain != "" {
		d, err := proxy.ParseDomain(svc.Domain)
		if err != nil {
			return "", err
		}
		vars["URL"], vars["HOST"] = d.URL(), d.Host
		if d.HTTPS {
			vars["SCHEME"], vars["SECURE"] = "https", "true"
		}
	}
	return tokenRe.ReplaceAllStringFunc(text, func(tok string) string {
		return vars[tok[2:len(tok)-2]]
	}), nil
}

// Deploy writes the compose project and starts (or updates) it.
func (m *Manager) Deploy(ctx context.Context, svc *store.Service, w io.Writer) error {
	t, ok := FindTemplate(svc.Template)
	if !ok {
		return fmt.Errorf("unknown template %q", svc.Template)
	}
	c, err := m.client(svc)
	if err != nil {
		return err
	}
	if err := proxy.EnsureProxy(ctx, c, m.Store.Setting("acme_email"), w); err != nil {
		return err
	}
	compose, err := m.Render(t, svc, t.Compose)
	if err != nil {
		return err
	}
	dir := m.dir(svc)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0o600); err != nil {
		return err
	}
	var domains []proxy.Domain
	if svc.Domain != "" {
		d, err := proxy.ParseDomain(svc.Domain)
		if err != nil {
			return err
		}
		domains = append(domains, d)
	}
	labels := append(proxy.Labels(svc.Slug, t.Port, domains), "selfdeploy.managed=true", "selfdeploy.service="+svc.Slug)
	override := map[string]any{
		"networks": map[string]any{proxy.Network: map[string]any{"external": true}},
		"services": map[string]any{t.Main: map[string]any{
			"labels": labels,
			"networks": map[string]any{"default": map[string]any{},
				proxy.Network: map[string]any{"aliases": []string{svc.Slug}}},
		}},
	}
	ob, _ := json.MarshalIndent(override, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.selfdeploy.yml"), ob, 0o600); err != nil {
		return err
	}
	m.Store.SetServiceStatus(svc.ID, "deploying")
	err = c.StreamIn(ctx, w, dir, "compose", "-p", svc.Slug, "-f", "docker-compose.yml",
		"-f", "docker-compose.selfdeploy.yml", "up", "-d", "--pull", "missing", "--remove-orphans")
	if err != nil {
		m.Store.SetServiceStatus(svc.ID, "failed")
		return err
	}
	m.Store.SetServiceStatus(svc.ID, "running")
	return nil
}

func (m *Manager) compose(ctx context.Context, svc *store.Service, args ...string) (string, error) {
	c, err := m.client(svc)
	if err != nil {
		return "", err
	}
	dir := m.dir(svc)
	base := []string{"compose", "-p", svc.Slug}
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err == nil {
		base = append(base, "-f", filepath.Join(dir, "docker-compose.yml"), "-f", filepath.Join(dir, "docker-compose.selfdeploy.yml"))
	}
	return c.Combined(ctx, append(base, args...)...)
}

func (m *Manager) Stop(ctx context.Context, svc *store.Service) error {
	if _, err := m.compose(ctx, svc, "stop"); err != nil {
		return err
	}
	m.Store.SetServiceStatus(svc.ID, "stopped")
	return nil
}

func (m *Manager) Start(ctx context.Context, svc *store.Service) error {
	if _, err := m.compose(ctx, svc, "start"); err != nil {
		return err
	}
	m.Store.SetServiceStatus(svc.ID, "running")
	return nil
}

func (m *Manager) Logs(ctx context.Context, svc *store.Service, tail int) (string, error) {
	return m.compose(ctx, svc, "logs", "--no-color", "--timestamps", "--tail", strconv.Itoa(tail))
}

// Destroy removes the service's containers, and its data volumes when asked.
func (m *Manager) Destroy(ctx context.Context, svc *store.Service, removeData bool) error {
	args := []string{"down", "--remove-orphans"}
	if removeData {
		args = append(args, "-v")
	}
	m.compose(ctx, svc, args...)
	return os.RemoveAll(m.dir(svc))
}
