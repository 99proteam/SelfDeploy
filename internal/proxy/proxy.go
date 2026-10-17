// Package proxy runs Traefik as the edge router: it routes domains to
// containers and issues Let's Encrypt certificates automatically.
package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/99proteam/selfdeploy/internal/docker"
)

const (
	Network        = "selfdeploy"
	Container      = "selfdeploy-proxy"
	CertVolume     = "selfdeploy-letsencrypt"
	CertResolver   = "letsencrypt"
	TraefikImage   = "traefik:v3.6"
	configLabel    = "selfdeploy.config"
	redirectSuffix = "-https-redirect"
)

// DynamicDir is a host directory mounted into Traefik for file-based routes
// (used to put the dashboard itself behind a domain with HTTPS).
var DynamicDir = "/data/selfdeploy/proxy"

// EnsureProxy makes sure Traefik is running with the current configuration,
// recreating it only when the configuration changed.
func EnsureProxy(ctx context.Context, c *docker.Client, acmeEmail string, log io.Writer) error {
	if err := c.EnsureNetwork(ctx, Network); err != nil {
		return err
	}
	args := []string{
		"--providers.docker=true",
		"--providers.docker.exposedbydefault=false",
		"--providers.docker.network=" + Network,
		"--entrypoints.web.address=:80",
		"--entrypoints.websecure.address=:443",
		"--entrypoints.websecure.http.tls=true",
		"--certificatesresolvers." + CertResolver + ".acme.storage=/letsencrypt/acme.json",
		"--certificatesresolvers." + CertResolver + ".acme.httpchallenge=true",
		"--certificatesresolvers." + CertResolver + ".acme.httpchallenge.entrypoint=web",
		"--providers.file.directory=/etc/traefik/dynamic",
		"--providers.file.watch=true",
		"--ping=true",
		"--log.level=INFO",
	}
	if acmeEmail != "" {
		args = append(args, "--certificatesresolvers."+CertResolver+".acme.email="+acmeEmail)
	}
	sum := sha256.Sum256([]byte(TraefikImage + strings.Join(args, " ")))
	hash := hex.EncodeToString(sum[:8])

	if c.ContainerState(ctx, Container) == "running" && c.ContainerLabel(ctx, Container, configLabel) == hash {
		return nil
	}
	fmt.Fprintf(log, "Starting reverse proxy (%s)...\n", TraefikImage)
	c.Remove(ctx, Container)
	run := []string{"run", "-d", "--name", Container, "--restart", "unless-stopped",
		"--network", Network, "-p", "80:80", "-p", "443:443", "-p", "443:443/udp",
		"-v", "/var/run/docker.sock:/var/run/docker.sock:ro",
		"-v", CertVolume + ":/letsencrypt",
		"-v", DynamicDir + ":/etc/traefik/dynamic:ro",
		"--label", configLabel + "=" + hash,
		"--label", "selfdeploy.managed=true",
		TraefikImage}
	_, err := c.Run(ctx, append(run, args...)...)
	return err
}

// Domain is one routable hostname for an app.
type Domain struct {
	Host  string
	HTTPS bool
}

func (d Domain) URL() string {
	if d.HTTPS {
		return "https://" + d.Host
	}
	return "http://" + d.Host
}

// ParseDomain accepts "example.com", "https://example.com" or "http://example.com".
// Bare hostnames get HTTPS, except temporary sslip.io/nip.io hosts, which are served
// over plain HTTP because Let's Encrypt rate limits those shared domains.
func ParseDomain(s string) (Domain, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	d := Domain{HTTPS: true}
	explicit := false
	switch {
	case strings.HasPrefix(s, "https://"):
		s, explicit = strings.TrimPrefix(s, "https://"), true
	case strings.HasPrefix(s, "http://"):
		s, explicit, d.HTTPS = strings.TrimPrefix(s, "http://"), true, false
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	if !validHost(s) {
		return d, fmt.Errorf("invalid domain %q", s)
	}
	if !explicit && IsTemporary(s) {
		d.HTTPS = false
	}
	d.Host = s
	return d, nil
}

func validHost(h string) bool {
	if h == "" || len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func IsTemporary(host string) bool {
	return strings.HasSuffix(host, ".sslip.io") || strings.HasSuffix(host, ".nip.io")
}

// ParseDomains parses a list, skipping blanks.
func ParseDomains(list []string) ([]Domain, error) {
	var out []Domain
	for _, s := range list {
		if strings.TrimSpace(s) == "" {
			continue
		}
		d, err := ParseDomain(s)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// GenerateDomain picks a default domain for a new resource: a subdomain of the
// configured wildcard domain if there is one, otherwise a free temporary
// <name>.<ip>.sslip.io hostname that works without any DNS setup.
func GenerateDomain(name, wildcard, ip string) string {
	if w := strings.TrimPrefix(strings.TrimSpace(wildcard), "*."); w != "" {
		return "https://" + name + "." + w
	}
	if ip == "" {
		return ""
	}
	return "http://" + name + "." + strings.ReplaceAll(ip, ":", "-") + ".sslip.io"
}

// Labels returns the docker labels that make Traefik route the domains to
// the container's port, with automatic certificates and an HTTP->HTTPS redirect.
func Labels(name string, port int, domains []Domain) []string {
	if len(domains) == 0 {
		return nil
	}
	labels := []string{
		"traefik.enable=true",
		"traefik.docker.network=" + Network,
		fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port=%d", name, port),
		fmt.Sprintf("traefik.http.middlewares.%s%s.redirectscheme.scheme=https", name, redirectSuffix),
		fmt.Sprintf("traefik.http.middlewares.%s%s.redirectscheme.permanent=true", name, redirectSuffix),
	}
	for i, d := range domains {
		r := fmt.Sprintf("traefik.http.routers.%s-%d", name, i)
		rule := fmt.Sprintf("Host(`%s`)", d.Host)
		if d.HTTPS {
			labels = append(labels,
				r+"-http.rule="+rule,
				r+"-http.entrypoints=web",
				r+"-http.middlewares="+name+redirectSuffix,
				r+"-http.service="+name,
				r+".rule="+rule,
				r+".entrypoints=websecure",
				r+".tls=true",
				r+".tls.certresolver="+CertResolver,
				r+".service="+name,
			)
		} else {
			labels = append(labels,
				r+".rule="+rule,
				r+".entrypoints=web",
				r+".service="+name,
			)
		}
	}
	return labels
}

// WriteDashboardRoute routes domain to the SelfDeploy dashboard container
// (or removes the route when domain is empty).
func WriteDashboardRoute(domain, upstream string) error {
	path := filepath.Join(DynamicDir, "selfdeploy-dashboard.yml")
	if domain == "" {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	d, err := ParseDomain(domain)
	if err != nil {
		return err
	}
	const name = "selfdeploy-dashboard"
	rule := fmt.Sprintf("Host(`%s`)", d.Host)
	routers := map[string]any{}
	if d.HTTPS {
		routers[name] = map[string]any{"rule": rule, "entryPoints": []string{"websecure"}, "service": name,
			"tls": map[string]any{"certResolver": CertResolver}}
		routers[name+"-http"] = map[string]any{"rule": rule, "entryPoints": []string{"web"}, "service": name,
			"middlewares": []string{name + redirectSuffix}}
	} else {
		routers[name] = map[string]any{"rule": rule, "entryPoints": []string{"web"}, "service": name}
	}
	cfg := map[string]any{"http": map[string]any{
		"routers": routers,
		"middlewares": map[string]any{name + redirectSuffix: map[string]any{
			"redirectScheme": map[string]any{"scheme": "https", "permanent": true}}},
		"services": map[string]any{name: map[string]any{
			"loadBalancer": map[string]any{"servers": []map[string]string{{"url": upstream}}}}},
	}}
	b, _ := json.MarshalIndent(cfg, "", "  ") // JSON is valid YAML
	if err := os.MkdirAll(DynamicDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
