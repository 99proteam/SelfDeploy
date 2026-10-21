package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99proteam/selfdeploy/internal/store"
)

func TestAuthURL(t *testing.T) {
	if got := authURL("https://github.com/a/b.git", "tok"); got != "https://x-access-token:tok@github.com/a/b.git" {
		t.Errorf("github: %s", got)
	}
	if got := authURL("https://gitlab.com/a/b.git", "tok"); got != "https://oauth2:tok@gitlab.com/a/b.git" {
		t.Errorf("gitlab: %s", got)
	}
	if got := authURL("git@github.com:a/b.git", "tok"); got != "git@github.com:a/b.git" {
		t.Errorf("ssh urls must be untouched: %s", got)
	}
}

func TestSafeJoin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "build")
	if p, err := safeJoin(root, "apps/web"); err != nil || p != filepath.Join(root, "apps", "web") {
		t.Errorf("subdir: %s %v", p, err)
	}
	if p, err := safeJoin(root, "../../etc"); err != nil || !strings.HasPrefix(p, root) {
		t.Errorf("escape not contained: %s %v", p, err)
	}
}

func TestDetectBuildPack(t *testing.T) {
	cases := map[string]string{"Dockerfile": "dockerfile", "compose.yaml": "compose", "index.html": "static", "package.json": "nixpacks"}
	for file, want := range cases {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, file), []byte("x"), 0o644)
		got, err := detectBuildPack(&store.App{BuildPack: "auto", BaseDir: "."}, dir)
		if err != nil || got != want {
			t.Errorf("%s: got %s %v, want %s", file, got, err, want)
		}
	}
}

func TestStaticDockerfile(t *testing.T) {
	dir := t.TempDir()
	app := &store.App{PublishDir: "dist", BuildCommand: "npm run build", Port: 3000}
	if err := writeStaticDockerfile(dir, app); err != nil {
		t.Fatal(err)
	}
	df, _ := os.ReadFile(filepath.Join(dir, ".selfdeploy.Dockerfile"))
	if !strings.Contains(string(df), "RUN npm run build") || !strings.Contains(string(df), "COPY --from=build /app/dist ") {
		t.Errorf("unexpected Dockerfile:\n%s", df)
	}
	ignore, _ := os.ReadFile(filepath.Join(dir, ".dockerignore"))
	if !strings.Contains(string(ignore), ".git") {
		t.Error(".git must be excluded from the image")
	}
	if app.Port != 80 {
		t.Errorf("static sites are served on port 80, got %d", app.Port)
	}
}

func TestPreviewTarget(t *testing.T) {
	e := &Engine{}
	tg, err := e.targetFor(&store.App{Slug: "web", Domains: "https://web.example.com,http://web.1.2.3.4.sslip.io"}, 12)
	if err != nil {
		t.Fatal(err)
	}
	if tg.name != "web-pr12" || tg.domains[0].Host != "pr12.web.example.com" || !tg.domains[0].HTTPS || tg.domains[1].HTTPS {
		t.Errorf("unexpected preview target: %+v", tg)
	}
	if !hasPreviewPrefix("web-pr12", "web") || hasPreviewPrefix("web-pro", "web") || hasPreviewPrefix("web", "web") {
		t.Error("hasPreviewPrefix")
	}
}

func TestDotenvQuote(t *testing.T) {
	cases := map[string]string{"pa$word": "'pa$word'", "": "''", "it's": `"it's"`, "a\nb": `"a\nb"`}
	for in, want := range cases {
		if got := dotenvQuote(in); got != want {
			t.Errorf("dotenvQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
