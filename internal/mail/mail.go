// Package mail runs a full mail server (SMTP, IMAP, DKIM, SPF, DMARC, spam
// filtering) using docker-mailserver, with TLS certificates from the proxy.
package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/99proteam/selfdeploy/internal/docker"
	"github.com/99proteam/selfdeploy/internal/proxy"
	"github.com/99proteam/selfdeploy/internal/store"
)

const (
	Container = "selfdeploy-mail"
	Image     = "mailserver/docker-mailserver:14"
)

type Manager struct {
	Store *store.Store
}

func (m *Manager) client() (*docker.Client, *store.Server, error) {
	srv, err := m.Store.LocalServer()
	if err != nil {
		return nil, nil, err
	}
	return docker.ForServer(srv.IsLocal, srv.User, srv.Host, srv.Port), srv, nil
}

func (m *Manager) Hostname() string { return m.Store.Setting("mail_hostname") }

// Status reports whether the mail container is running.
func (m *Manager) Status(ctx context.Context) string {
	c, _, err := m.client()
	if err != nil {
		return "unknown"
	}
	if st := c.ContainerState(ctx, Container); st != "" {
		return st
	}
	return "not installed"
}

var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func ValidDomain(d string) bool { return len(d) <= 253 && hostRe.MatchString(d) }

// Enable installs or reconfigures the mail server for the given hostname
// (e.g. mail.example.com). It first lets the proxy obtain a Let's Encrypt
// certificate for that name, then starts the mail server with it.
func (m *Manager) Enable(ctx context.Context, hostname string, log io.Writer) error {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	if !ValidDomain(hostname) {
		return fmt.Errorf("invalid mail hostname %q", hostname)
	}
	c, _, err := m.client()
	if err != nil {
		return err
	}
	if err := proxy.EnsureProxy(ctx, c, m.Store.Setting("acme_email"), log); err != nil {
		return err
	}
	if err := m.Store.SetSetting("mail_hostname", hostname); err != nil {
		return err
	}
	fmt.Fprintf(log, "Pulling %s...\n", Image)
	if _, err := c.Run(ctx, "pull", Image); err != nil {
		return err
	}

	// Routing labels make Traefik request a certificate for the mail hostname.
	labels := proxy.Labels(Container, 25, []proxy.Domain{{Host: hostname, HTTPS: true}})
	sslType := "letsencrypt"
	fmt.Fprintf(log, "Requesting TLS certificate for %s (DNS A record must point to this server)...\n", hostname)
	cert := m.waitForCert(ctx, c, hostname, labels)
	if !cert {
		sslType = ""
		fmt.Fprintln(log, "Certificate not issued yet (check the DNS A record); starting without TLS. Run setup again once DNS resolves.")
	}

	// Dovecot listens on IPv6 too by default and refuses to start on hosts
	// with IPv6 disabled; Docker forwards IPv6 traffic over IPv4 anyway.
	if _, err := c.Run(ctx, "run", "--rm", "-v", "selfdeploy-mail-config:/cfg", "busybox:1.36", "sh", "-c",
		`grep -qs '^listen' /cfg/dovecot.cf || echo 'listen = *' >> /cfg/dovecot.cf`); err != nil {
		return err
	}

	c.Remove(ctx, Container)
	args := []string{"run", "-d", "--name", Container, "--hostname", hostname, "--restart", "unless-stopped",
		"--network", proxy.Network, "--network-alias", hostname, "--cap-add", "NET_ADMIN",
		"-p", "25:25", "-p", "465:465", "-p", "587:587", "-p", "143:143", "-p", "993:993",
		"-v", "selfdeploy-mail-data:/var/mail",
		"-v", "selfdeploy-mail-state:/var/mail-state",
		"-v", "selfdeploy-mail-logs:/var/log/mail",
		"-v", "selfdeploy-mail-config:/tmp/docker-mailserver",
		"-v", proxy.CertVolume + ":/etc/letsencrypt:ro",
		"-e", "SSL_TYPE=" + sslType,
		"-e", "SSL_DOMAIN=" + hostname,
		"-e", "ENABLE_FAIL2BAN=1",
		"-e", "ENABLE_OPENDKIM=1",
		"-e", "ENABLE_OPENDMARC=1",
		"-e", "ENABLE_POLICYD_SPF=1",
		"-e", "ENABLE_SPAMASSASSIN=1",
		"-e", "ENABLE_CLAMAV=0",
		"-e", "SPOOF_PROTECTION=1",
		"-e", "PERMIT_DOCKER=connected-networks",
		"-e", "ONE_DIR=1",
		"--label", "selfdeploy.managed=true",
		"--stop-timeout", "30"}
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	fmt.Fprintln(log, "Starting mail server...")
	if _, err := c.Run(ctx, append(args, Image)...); err != nil {
		return err
	}
	fmt.Fprintln(log, "Mail server started. Add a domain and a mailbox to start sending and receiving.")
	return nil
}

// waitForCert runs a placeholder container carrying the routing labels until
// the proxy has stored a certificate for the hostname (up to ~2 minutes).
func (m *Manager) waitForCert(ctx context.Context, c *docker.Client, hostname string, labels []string) bool {
	if m.hasCert(ctx, c, hostname) {
		return true
	}
	placeholder := Container + "-acme"
	c.Remove(ctx, placeholder)
	args := []string{"run", "-d", "--name", placeholder, "--network", proxy.Network, "--label", "selfdeploy.managed=true"}
	for _, l := range labels {
		args = append(args, "--label", strings.ReplaceAll(l, Container, placeholder))
	}
	if _, err := c.Run(ctx, append(args, "busybox:1.36", "sleep", "300")...); err != nil {
		return false
	}
	defer c.Remove(context.Background(), placeholder)
	for i := 0; i < 24; i++ {
		if m.hasCert(ctx, c, hostname) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Second):
		}
	}
	return false
}

func (m *Manager) hasCert(ctx context.Context, c *docker.Client, hostname string) bool {
	out, err := c.Run(ctx, "exec", proxy.Container, "cat", "/letsencrypt/acme.json")
	return err == nil && strings.Contains(out, `"main": "`+hostname+`"`)
}

// Disable stops and removes the mail server; mail data volumes are kept.
func (m *Manager) Disable(ctx context.Context) error {
	c, _, err := m.client()
	if err != nil {
		return err
	}
	c.Remove(ctx, Container)
	return nil
}

func (m *Manager) setup(ctx context.Context, args ...string) (string, error) {
	c, _, err := m.client()
	if err != nil {
		return "", err
	}
	if c.ContainerState(ctx, Container) != "running" {
		return "", errors.New("the mail server is not running; enable it first")
	}
	return c.Run(ctx, append([]string{"exec", Container, "setup"}, args...)...)
}

func (m *Manager) AddMailbox(ctx context.Context, address, password string) error {
	_, err := m.setup(ctx, "email", "add", address, password)
	return err
}

func (m *Manager) UpdatePassword(ctx context.Context, address, password string) error {
	_, err := m.setup(ctx, "email", "update", address, password)
	return err
}

func (m *Manager) DeleteMailbox(ctx context.Context, address string) error {
	_, err := m.setup(ctx, "email", "del", "-y", address)
	return err
}

func (m *Manager) AddAlias(ctx context.Context, source, dest string) error {
	_, err := m.setup(ctx, "alias", "add", source, dest)
	return err
}

func (m *Manager) DeleteAlias(ctx context.Context, source, dest string) error {
	_, err := m.setup(ctx, "alias", "del", source, dest)
	return err
}

var quoted = regexp.MustCompile(`"([^"]*)"`)

// GenerateDKIM creates a 2048-bit DKIM key for the domain and returns the TXT
// record value to publish at mail._domainkey.<domain>.
func (m *Manager) GenerateDKIM(ctx context.Context, domain string) (string, error) {
	if _, err := m.setup(ctx, "config", "dkim", "keysize", "2048", "domain", domain); err != nil {
		return "", err
	}
	c, _, _ := m.client()
	out, err := c.Run(ctx, "exec", Container, "cat", "/tmp/docker-mailserver/opendkim/keys/"+domain+"/mail.txt")
	if err != nil {
		return "", err
	}
	var parts []string
	for _, mt := range quoted.FindAllStringSubmatch(out, -1) {
		parts = append(parts, mt[1])
	}
	if len(parts) == 0 {
		return "", errors.New("could not read the generated DKIM key")
	}
	// OpenDKIM only loads new keys on restart.
	c.Run(ctx, "restart", Container)
	return strings.Join(parts, ""), nil
}

type DNSRecord struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Value    string `json:"value"`
	Priority int    `json:"priority,omitempty"`
	Note     string `json:"note,omitempty"`
}

// DNSRecords lists everything a domain needs for reliable delivery.
func DNSRecords(domain, hostname, ip, dkim string) []DNSRecord {
	recs := []DNSRecord{
		{Type: "A", Name: hostname, Value: ip, Note: "Mail server address"},
		{Type: "MX", Name: domain, Value: hostname, Priority: 10, Note: "Receive mail for " + domain},
		{Type: "TXT", Name: domain, Value: "v=spf1 mx a:" + hostname + " ~all", Note: "SPF: who may send for " + domain},
		{Type: "TXT", Name: "_dmarc." + domain, Value: "v=DMARC1; p=quarantine; rua=mailto:postmaster@" + domain + "; adkim=s; aspf=s", Note: "DMARC policy"},
	}
	if dkim != "" {
		recs = append(recs, DNSRecord{Type: "TXT", Name: "mail._domainkey." + domain, Value: dkim, Note: "DKIM signature key"})
	}
	recs = append(recs, DNSRecord{Type: "PTR", Name: ip, Value: hostname, Note: "Reverse DNS: set this at your VPS provider"})
	return recs
}
