package proxy

import (
	"strings"
	"testing"
)

func TestParseDomain(t *testing.T) {
	cases := []struct {
		in    string
		host  string
		https bool
		bad   bool
	}{
		{in: "example.com", host: "example.com", https: true},
		{in: "https://App.Example.com/", host: "app.example.com", https: true},
		{in: "http://example.com:8080/path", host: "example.com", https: false},
		{in: "my-app.1.2.3.4.sslip.io", host: "my-app.1.2.3.4.sslip.io", https: false},
		{in: "https://my-app.1.2.3.4.sslip.io", host: "my-app.1.2.3.4.sslip.io", https: true},
		{in: "localhost", bad: true},
		{in: "-bad.example.com", bad: true},
		{in: "exa mple.com", bad: true},
		{in: "a`b.com", bad: true},
	}
	for _, c := range cases {
		d, err := ParseDomain(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("ParseDomain(%q) = %+v, want error", c.in, d)
			}
			continue
		}
		if err != nil || d.Host != c.host || d.HTTPS != c.https {
			t.Errorf("ParseDomain(%q) = %+v, %v; want host %q https %v", c.in, d, err, c.host, c.https)
		}
	}
}

func TestGenerateDomain(t *testing.T) {
	if got := GenerateDomain("blog", "", "203.0.113.7"); got != "http://blog.203.0.113.7.sslip.io" {
		t.Errorf("sslip: got %q", got)
	}
	if got := GenerateDomain("blog", "*.apps.example.com", "203.0.113.7"); got != "https://blog.apps.example.com" {
		t.Errorf("wildcard: got %q", got)
	}
	if got := GenerateDomain("blog", "", "2001:db8::1"); got != "http://blog.2001-db8--1.sslip.io" {
		t.Errorf("ipv6: got %q", got)
	}
	if got := GenerateDomain("blog", "", ""); got != "" {
		t.Errorf("no ip: got %q", got)
	}
}

func TestLabels(t *testing.T) {
	labels := strings.Join(Labels("web", 3000, []Domain{{Host: "example.com", HTTPS: true}, {Host: "x.1.2.3.4.sslip.io"}}), "\n")
	for _, want := range []string{
		"traefik.enable=true",
		"traefik.http.services.web.loadbalancer.server.port=3000",
		"traefik.http.routers.web-0.tls.certresolver=letsencrypt",
		"traefik.http.routers.web-0-http.middlewares=web-https-redirect",
		"traefik.http.routers.web-1.entrypoints=web",
		"traefik.http.routers.web-1.rule=Host(`x.1.2.3.4.sslip.io`)",
	} {
		if !strings.Contains(labels, want) {
			t.Errorf("labels missing %q:\n%s", want, labels)
		}
	}
	if strings.Contains(labels, "web-1.tls") {
		t.Error("temporary domain must not request a certificate")
	}
	if Labels("web", 80, nil) != nil {
		t.Error("no domains should mean no labels")
	}
}

func TestLabelsWithBasicAuth(t *testing.T) {
	doms := []Domain{{Host: "example.com", HTTPS: true}, {Host: "x.1.2.3.4.sslip.io"}}
	labels := strings.Join(LabelsWith("web", 3000, doms, Options{BasicAuth: "team:$2a$10$hash"}), "\n")
	for _, want := range []string{
		"traefik.http.middlewares.web-auth.basicauth.users=team:$2a$10$hash",
		"traefik.http.routers.web-0.middlewares=web-auth",
		"traefik.http.routers.web-1.middlewares=web-auth",
		// The HTTP router of an HTTPS domain only redirects; auth happens after the redirect.
		"traefik.http.routers.web-0-http.middlewares=web-https-redirect",
	} {
		if !strings.Contains(labels, want) {
			t.Errorf("labels missing %q:\n%s", want, labels)
		}
	}
	if plain := strings.Join(Labels("web", 3000, doms), "\n"); strings.Contains(plain, "basicauth") {
		t.Error("auth middleware added without options")
	}
}
