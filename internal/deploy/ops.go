package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/99proteam/selfdeploy/internal/store"
)

func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Minute)
}

func (e *Engine) composeDir(name string) string { return filepath.Join(e.DataDir, "compose", name) }

func (e *Engine) isCompose(name string) bool {
	_, err := os.Stat(e.composeDir(name))
	return err == nil
}

// compose runs a docker compose command against a deployed compose app.
func (e *Engine) compose(ctx context.Context, app *store.App, name string, args ...string) (string, error) {
	c, _, err := e.Client(app.ServerID)
	if err != nil {
		return "", err
	}
	return c.Run(ctx, append([]string{"compose", "-p", name}, args...)...)
}

func (e *Engine) containers(ctx context.Context, app *store.App, name string) ([]string, error) {
	c, _, err := e.Client(app.ServerID)
	if err != nil {
		return nil, err
	}
	return c.ContainersByLabel(ctx, labelApp+"="+name)
}

// Stop stops every container of the app (and its compose services).
func (e *Engine) Stop(app *store.App) error {
	ctx, cancel := opCtx()
	defer cancel()
	if e.isCompose(app.Slug) {
		if _, err := e.compose(ctx, app, app.Slug, "stop"); err != nil {
			return err
		}
	} else {
		names, err := e.containers(ctx, app, app.Slug)
		if err != nil {
			return err
		}
		c, _, _ := e.Client(app.ServerID)
		for _, n := range names {
			if _, err := c.Run(ctx, "stop", "-t", "15", n); err != nil {
				return err
			}
		}
	}
	e.Store.SetAppStatus(app.ID, "stopped")
	return nil
}

// Start starts stopped containers; an app that was never deployed gets deployed.
func (e *Engine) Start(app *store.App) error {
	ctx, cancel := opCtx()
	defer cancel()
	if e.isCompose(app.Slug) {
		if _, err := e.compose(ctx, app, app.Slug, "start"); err != nil {
			return err
		}
		e.Store.SetAppStatus(app.ID, "running")
		return nil
	}
	names, err := e.containers(ctx, app, app.Slug)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		_, err := e.Enqueue(app, Options{Trigger: "manual"})
		return err
	}
	c, _, _ := e.Client(app.ServerID)
	for _, n := range names {
		if _, err := c.Run(ctx, "start", n); err != nil {
			return err
		}
	}
	e.Store.SetAppStatus(app.ID, "running")
	return nil
}

func (e *Engine) Restart(app *store.App) error {
	ctx, cancel := opCtx()
	defer cancel()
	if e.isCompose(app.Slug) {
		_, err := e.compose(ctx, app, app.Slug, "restart")
		return err
	}
	names, err := e.containers(ctx, app, app.Slug)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return errors.New("app is not deployed yet")
	}
	c, _, _ := e.Client(app.ServerID)
	for _, n := range names {
		if _, err := c.Run(ctx, "restart", n); err != nil {
			return err
		}
	}
	e.Store.SetAppStatus(app.ID, "running")
	return nil
}

// Logs returns the latest runtime log lines of the app.
func (e *Engine) Logs(app *store.App, tail int) (string, error) {
	ctx, cancel := opCtx()
	defer cancel()
	t := fmt.Sprint(tail)
	c, _, err := e.Client(app.ServerID)
	if err != nil {
		return "", err
	}
	if e.isCompose(app.Slug) {
		return c.Combined(ctx, "compose", "-p", app.Slug, "logs", "--no-color", "--timestamps", "--tail", t)
	}
	names, err := e.containers(ctx, app, app.Slug)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", errors.New("app is not running")
	}
	// Combined, because docker logs replays the container's stderr on its own stderr.
	return c.Combined(ctx, "logs", "--timestamps", "--tail", t, names[0])
}

// RemovePreview tears down the preview deployment of a pull request.
func (e *Engine) RemovePreview(app *store.App, pr int) error {
	ctx, cancel := opCtx()
	defer cancel()
	name := fmt.Sprintf("%s-pr%d", app.Slug, pr)
	if e.isCompose(name) {
		e.compose(ctx, app, name, "down", "--remove-orphans")
		return os.RemoveAll(e.composeDir(name))
	}
	names, err := e.containers(ctx, app, name)
	if err != nil {
		return err
	}
	c, _, _ := e.Client(app.ServerID)
	c.Remove(ctx, names...)
	return nil
}

// Destroy removes all containers, previews and images of an app.
func (e *Engine) Destroy(app *store.App, removeVolumes bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, _, err := e.Client(app.ServerID)
	if err != nil {
		return err
	}
	if e.isCompose(app.Slug) {
		args := []string{"down", "--remove-orphans"}
		if removeVolumes {
			args = append(args, "-v")
		}
		e.compose(ctx, app, app.Slug, args...)
		os.RemoveAll(e.composeDir(app.Slug))
	}
	all, _ := c.Run(ctx, "ps", "-a", "--filter", "label=selfdeploy.managed=true", "--format", `{{.Names}} {{.Label "selfdeploy.app"}}`)
	for _, line := range splitLines(all) {
		var cname, label string
		fmt.Sscan(line, &cname, &label)
		if label == app.Slug || hasPreviewPrefix(label, app.Slug) {
			c.Remove(ctx, cname)
		}
	}
	imgs, _ := c.Run(ctx, "images", "--format", "{{.Repository}}:{{.Tag}}", "--filter", "reference=selfdeploy/"+app.Slug+"*")
	for _, img := range splitLines(imgs) {
		c.Run(ctx, "rmi", "-f", img)
	}
	return nil
}

func hasPreviewPrefix(label, slug string) bool {
	var n int
	_, err := fmt.Sscanf(label, slug+"-pr%d", &n)
	return err == nil && label == fmt.Sprintf("%s-pr%d", slug, n)
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}
