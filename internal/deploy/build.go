package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/99proteam/selfdeploy/internal/docker"
	"github.com/99proteam/selfdeploy/internal/proxy"
	"github.com/99proteam/selfdeploy/internal/store"
)

// authURL embeds an access token into an HTTPS clone URL for private repos.
func authURL(raw, token string) string {
	if token == "" || !strings.HasPrefix(raw, "https://") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	user := "oauth2" // GitLab, Gitea, Bitbucket
	if strings.HasSuffix(u.Host, "github.com") {
		user = "x-access-token"
	}
	u.User = url.UserPassword(user, token)
	return u.String()
}

func git(ctx context.Context, w io.Writer, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	return nil
}

// checkout fetches exactly one ref (branch, tag, commit or PR head) at depth 1.
func (e *Engine) checkout(ctx context.Context, w io.Writer, app *store.App, d *store.Deployment, dir string) error {
	if app.GitURL == "" {
		return errors.New("no git repository configured")
	}
	ref := app.GitBranch
	if d.GitRef != "" {
		ref = d.GitRef
	}
	if strings.HasPrefix(ref, "-") || strings.HasPrefix(app.GitURL, "-") {
		return errors.New("invalid git ref or URL")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(w, "Cloning %s (%s)...\n", app.GitURL, ref)
	if err := git(ctx, w, dir, "init", "-q"); err != nil {
		return err
	}
	if err := git(ctx, w, dir, "remote", "add", "origin", authURL(app.GitURL, app.GitToken)); err != nil {
		return err
	}
	// Webhook deploys pin the pushed commit; fall back to the branch head when
	// the git host does not allow fetching a commit by SHA.
	fetched := false
	if d.CommitSHA != "" && d.PRNumber == 0 && !strings.HasPrefix(d.CommitSHA, "-") {
		fetched = git(ctx, io.Discard, dir, "fetch", "-q", "--depth", "1", "origin", d.CommitSHA) == nil
	}
	if !fetched {
		if err := git(ctx, w, dir, "fetch", "-q", "--depth", "1", "origin", ref); err != nil {
			return err
		}
	}
	if err := git(ctx, w, dir, "checkout", "-q", "FETCH_HEAD"); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitmodules")); err == nil {
		if err := git(ctx, w, dir, "submodule", "update", "--init", "--recursive", "--depth", "1"); err != nil {
			return err
		}
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "log", "-1", "--format=%H%n%s").Output()
	if err == nil {
		parts := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
		msg := ""
		if len(parts) == 2 {
			msg = parts[1]
		}
		e.Store.SetDeploymentCommit(d.ID, parts[0], msg)
		fmt.Fprintf(w, "Commit %s %s\n", short(parts[0]), msg)
	}
	return nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// safeJoin resolves a user-supplied path inside the checkout, refusing escapes.
func safeJoin(root, rel string) (string, error) {
	p := filepath.Join(root, filepath.Clean("/"+rel))
	if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the repository", rel)
	}
	return p, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// detectBuildPack resolves "auto": a Dockerfile wins, then a compose file,
// then a plain static site, and Nixpacks handles everything else
// (Node, Python, Go, PHP, Ruby, Rust, Java, .NET, Elixir, Deno, Bun ...).
func detectBuildPack(app *store.App, dir string) (string, error) {
	if app.BuildPack != "auto" && app.BuildPack != "" {
		return app.BuildPack, nil
	}
	base, err := safeJoin(dir, app.BaseDir)
	if err != nil {
		return "", err
	}
	switch {
	case exists(filepath.Join(base, "Dockerfile")):
		return "dockerfile", nil
	case exists(filepath.Join(base, "docker-compose.yml")), exists(filepath.Join(base, "docker-compose.yaml")),
		exists(filepath.Join(base, "compose.yml")), exists(filepath.Join(base, "compose.yaml")):
		return "compose", nil
	case exists(filepath.Join(base, "index.html")) && !exists(filepath.Join(base, "package.json")):
		return "static", nil
	}
	return "nixpacks", nil
}

func (e *Engine) build(ctx context.Context, w io.Writer, c *docker.Client, app *store.App, pack, dir, image string, buildEnv []string) error {
	base, err := safeJoin(dir, app.BaseDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Building with %s build pack...\n", pack)
	switch pack {
	case "dockerfile":
		df, err := safeJoin(dir, filepath.Join(app.BaseDir, app.DockerfilePath))
		if err != nil {
			return err
		}
		args := []string{"build", "-t", image, "-f", df, "--label", "selfdeploy.managed=true"}
		for _, kv := range buildEnv {
			args = append(args, "--build-arg", kv)
		}
		return c.Stream(ctx, w, append(args, base)...)

	case "static":
		if err := writeStaticDockerfile(base, app); err != nil {
			return err
		}
		e.Store.SetAppPort(app.ID, app.Port) // nginx always listens on 80
		return c.Stream(ctx, w, "build", "-t", image, "-f", filepath.Join(base, ".selfdeploy.Dockerfile"), base)

	case "nixpacks":
		if _, err := exec.LookPath("nixpacks"); err != nil {
			return errors.New("nixpacks is not installed; add a Dockerfile or choose another build pack")
		}
		args := []string{"build", base, "--name", image}
		for _, kv := range buildEnv {
			args = append(args, "--env", kv)
		}
		if app.InstallCommand != "" {
			args = append(args, "--install-cmd", app.InstallCommand)
		}
		if app.BuildCommand != "" {
			args = append(args, "--build-cmd", app.BuildCommand)
		}
		if app.StartCommand != "" {
			args = append(args, "--start-cmd", app.StartCommand)
		}
		return c.Exec(ctx, w, dir, "nixpacks", args...)
	}
	return fmt.Errorf("unknown build pack %q", pack)
}

const staticNginx = `server {
  listen 80;
  root /usr/share/nginx/html;
  index index.html;
  gzip on;
  gzip_types text/css application/javascript application/json image/svg+xml;
  location / { try_files $uri $uri/ /index.html; }
  location ~* \.(js|css|png|jpg|jpeg|gif|svg|ico|woff2?)$ { expires 30d; add_header Cache-Control "public"; }
}
`

// writeStaticDockerfile serves a static or SPA build with nginx, running the
// install/build commands first when the site needs compiling (Vite, Astro, ...).
func writeStaticDockerfile(base string, app *store.App) error {
	if err := os.WriteFile(filepath.Join(base, ".selfdeploy-nginx.conf"), []byte(staticNginx), 0o644); err != nil {
		return err
	}
	// Never publish the repository history or our build files as web content.
	ignore, _ := os.ReadFile(filepath.Join(base, ".dockerignore"))
	ignore = append(ignore, []byte("\n.git\n.selfdeploy.Dockerfile\n")...)
	if err := os.WriteFile(filepath.Join(base, ".dockerignore"), ignore, 0o644); err != nil {
		return err
	}
	// A path inside the image, so always slash-separated.
	publish := path.Clean("/" + app.PublishDir)
	var df string
	if app.BuildCommand == "" {
		if app.PublishDir == "" || app.PublishDir == "dist" && !exists(filepath.Join(base, "dist")) {
			publish = "/"
		}
		df = "FROM nginx:alpine\nCOPY ." + publish + " /usr/share/nginx/html\n"
	} else {
		install := app.InstallCommand
		if install == "" {
			install = "if [ -f package-lock.json ]; then npm ci; elif [ -f pnpm-lock.yaml ]; then corepack enable && pnpm i --frozen-lockfile; elif [ -f yarn.lock ]; then corepack enable && yarn install --frozen-lockfile; else npm install; fi"
		}
		df = "FROM node:22-alpine AS build\nWORKDIR /app\nCOPY . .\nRUN " + install + "\nRUN " + app.BuildCommand +
			"\nFROM nginx:alpine\nCOPY --from=build /app" + publish + " /usr/share/nginx/html\n"
	}
	df += "COPY .selfdeploy-nginx.conf /etc/nginx/conf.d/default.conf\nEXPOSE 80\n"
	app.Port = 80
	return os.WriteFile(filepath.Join(base, ".selfdeploy.Dockerfile"), []byte(df), 0o644)
}

// deployCompose runs a docker-compose app. An override file attaches the
// chosen service to the proxy network with routing labels, and injects env vars.
func (e *Engine) deployCompose(ctx context.Context, w io.Writer, c *docker.Client, app *store.App, t target, dir string, env []string) error {
	base, err := safeJoin(dir, app.BaseDir)
	if err != nil {
		return err
	}
	file := app.ComposePath
	if file == "" || !exists(filepath.Join(base, file)) {
		for _, f := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
			if exists(filepath.Join(base, f)) {
				file = f
				break
			}
		}
	}
	composeFile, err := safeJoin(base, file)
	if err != nil {
		return err
	}
	// Compose apps run from a stable directory (not the temporary build dir)
	// so stop, logs and down keep working later. Use named volumes for data:
	// the directory is replaced on every deploy.
	stable := filepath.Join(e.DataDir, "compose", t.name)
	if err := os.MkdirAll(filepath.Dir(stable), 0o755); err != nil {
		return err
	}
	if err := syncDir(dir, stable); err != nil {
		return err
	}
	rel, _ := filepath.Rel(dir, composeFile)
	composeFile = filepath.Join(stable, rel)
	projectDir := filepath.Dir(composeFile)

	envMap := map[string]string{}
	var dotenv strings.Builder
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		// Compose interpolates $VAR in compose files; $$ is a literal dollar.
		envMap[k] = strings.ReplaceAll(v, "$", "$$")
		fmt.Fprintf(&dotenv, "%s=%s\n", k, dotenvQuote(v))
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte(dotenv.String()), 0o600); err != nil {
		return err
	}
	override := map[string]any{"networks": map[string]any{proxy.Network: map[string]any{"external": true}}}
	if app.ComposeService != "" {
		labels := proxy.Labels(t.name, app.Port, t.domains)
		labels = append(labels, labelApp+"="+t.name, "selfdeploy.managed=true")
		override["services"] = map[string]any{app.ComposeService: map[string]any{
			"labels":      labels,
			"networks":    map[string]any{"default": map[string]any{}, proxy.Network: map[string]any{}},
			"environment": envMap,
		}}
	} else {
		fmt.Fprintln(w, "No compose service selected for the domain; services are started without proxy routing.")
	}
	ob, _ := json.MarshalIndent(override, "", "  ") // JSON is valid YAML for compose
	overrideFile := filepath.Join(projectDir, "docker-compose.selfdeploy.yml")
	if err := os.WriteFile(overrideFile, ob, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "Running docker compose up for %s...\n", file)
	return c.StreamIn(ctx, w, projectDir, "compose", "-p", t.name, "-f", composeFile, "-f", overrideFile,
		"up", "-d", "--build", "--remove-orphans", "--pull", "missing")
}

// dotenvQuote quotes a value for a compose .env file. Single-quoted values are
// taken literally, so passwords containing $ survive; values that cannot be
// single-quoted fall back to double quotes with escapes.
func dotenvQuote(v string) string {
	if !strings.ContainsAny(v, "'\n\r") {
		return "'" + v + "'"
	}
	return strconv.Quote(v)
}

// syncDir replaces dst with src.
func syncDir(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return os.Rename(src, dst)
}
