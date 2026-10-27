// Package deploy builds applications from git or images and rolls them out
// with zero downtime behind the Traefik proxy.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/99proteam/selfdeploy/internal/docker"
	"github.com/99proteam/selfdeploy/internal/notify"
	"github.com/99proteam/selfdeploy/internal/proxy"
	"github.com/99proteam/selfdeploy/internal/store"
)

const (
	labelApp        = "selfdeploy.app"
	labelDeployment = "selfdeploy.deployment"
	keepImages      = 5
	deployTimeout   = 45 * time.Minute
)

type Engine struct {
	Store   *store.Store
	Notify  *notify.Notifier
	DataDir string

	queue chan int64
	mu    sync.Mutex
	locks map[int64]*sync.Mutex
}

func New(s *store.Store, n *notify.Notifier, dataDir string) *Engine {
	return &Engine{Store: s, Notify: n, DataDir: dataDir, queue: make(chan int64, 256), locks: map[int64]*sync.Mutex{}}
}

// StartWorkers launches the build workers. Deployments of the same app run one at a
// time; different apps build in parallel.
func (e *Engine) StartWorkers(workers int) {
	e.Store.FailStaleDeployments()
	for i := 0; i < workers; i++ {
		go func() {
			for id := range e.queue {
				e.run(id)
			}
		}()
	}
}

// Options describe what a deployment should do beyond a normal redeploy.
type Options struct {
	Trigger       string // manual | webhook | api | rollback | preview
	CommitSHA     string
	CommitMessage string
	PRNumber      int
	GitRef        string // overrides the branch, e.g. refs/pull/12/head
	RollbackImage string
}

func (e *Engine) Enqueue(app *store.App, o Options) (*store.Deployment, error) {
	d := &store.Deployment{AppID: app.ID, Trigger: o.Trigger, CommitSHA: o.CommitSHA,
		CommitMessage: o.CommitMessage, PRNumber: o.PRNumber, GitRef: o.GitRef, RollbackImage: o.RollbackImage}
	if err := e.Store.CreateDeployment(d); err != nil {
		return nil, err
	}
	select {
	case e.queue <- d.ID:
	default:
		e.Store.FinishDeployment(d.ID, "failed", "")
		return nil, errors.New("deployment queue is full, try again shortly")
	}
	return d, nil
}

func (e *Engine) appLock(id int64) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.locks[id] == nil {
		e.locks[id] = &sync.Mutex{}
	}
	return e.locks[id]
}

// Client returns the docker client for the server an app lives on.
func (e *Engine) Client(serverID int64) (*docker.Client, *store.Server, error) {
	srv, err := e.Store.Server(serverID)
	if err != nil {
		return nil, nil, fmt.Errorf("server %d: %w", serverID, err)
	}
	return docker.ForServer(srv.IsLocal, srv.User, srv.Host, srv.Port), srv, nil
}

// target is where a deployment lands: the app itself or a PR preview of it.
type target struct {
	name    string
	domains []proxy.Domain
	preview bool
	opts    proxy.Options
}

func (e *Engine) targetFor(app *store.App, pr int) (target, error) {
	domains, err := proxy.ParseDomains(app.DomainList())
	if err != nil {
		return target{}, err
	}
	opts := proxy.Options{BasicAuth: app.BasicAuth()}
	if pr == 0 {
		return target{name: app.Slug, domains: domains, opts: opts}, nil
	}
	t := target{name: fmt.Sprintf("%s-pr%d", app.Slug, pr), preview: true, opts: opts}
	for _, d := range domains {
		t.domains = append(t.domains, proxy.Domain{Host: fmt.Sprintf("pr%d.%s", pr, d.Host), HTTPS: d.HTTPS})
	}
	return t, nil
}

func (e *Engine) run(id int64) {
	d, err := e.Store.Deployment(id)
	if err != nil {
		return
	}
	app, err := e.Store.App(d.AppID)
	if err != nil {
		e.Store.FinishDeployment(id, "failed", "")
		return
	}
	lock := e.appLock(app.ID)
	lock.Lock()
	defer lock.Unlock()

	e.Store.SetDeploymentStatus(id, "running")
	if d.PRNumber == 0 {
		e.Store.SetAppStatus(app.ID, "deploying")
	}
	w := newLogWriter(e.Store, id, app.GitToken)
	ctx, cancel := context.WithTimeout(context.Background(), deployTimeout)
	defer cancel()

	start := time.Now()
	image, err := e.deploy(ctx, w, app, d)
	if err != nil {
		fmt.Fprintf(w, "\n❌ Deployment failed: %v\n", err)
	} else {
		fmt.Fprintf(w, "\n✅ Deployment finished in %s\n", time.Since(start).Round(time.Second))
	}
	w.Close()

	title := fmt.Sprintf("%s deployed", app.Name)
	event, status := notify.DeploySuccess, "success"
	if err != nil {
		title, event, status = fmt.Sprintf("%s deployment failed", app.Name), notify.DeployFailed, "failed"
	}
	e.Store.FinishDeployment(id, status, image)
	if d.PRNumber == 0 {
		if err == nil {
			e.Store.SetAppImage(app.ID, image)
		} else if app.CurrentImage != "" {
			e.Store.SetAppStatus(app.ID, "running") // the previous version keeps serving
		} else {
			e.Store.SetAppStatus(app.ID, "failed")
		}
	}
	msg := fmt.Sprintf("Deployment #%d (%s)", id, d.Trigger)
	if d.PRNumber > 0 {
		msg += fmt.Sprintf(", preview for PR #%d", d.PRNumber)
	}
	if err != nil {
		msg += ": " + err.Error()
	} else if doms := app.DomainList(); len(doms) > 0 {
		msg += " is live at " + doms[0]
	}
	e.Notify.Send(event, title, msg)
}

func (e *Engine) deploy(ctx context.Context, w io.Writer, app *store.App, d *store.Deployment) (string, error) {
	c, srv, err := e.Client(app.ServerID)
	if err != nil {
		return "", err
	}
	t, err := e.targetFor(app, d.PRNumber)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(w, "Deploying %s to server %q (trigger: %s)\n", t.name, srv.Name, d.Trigger)
	if err := proxy.EnsureProxy(ctx, c, e.Store.Setting("acme_email"), w); err != nil {
		e.Store.SetServerProxyStatus(srv.ID, "error")
		return "", fmt.Errorf("proxy: %w", err)
	}
	e.Store.SetServerProxyStatus(srv.ID, "running")

	env, buildEnv, err := e.envFor(app, t, d)
	if err != nil {
		return "", err
	}

	var image string
	switch {
	case d.RollbackImage != "":
		image = d.RollbackImage
		fmt.Fprintf(w, "Rolling back to image %s\n", image)
		if _, err := c.Run(ctx, "image", "inspect", image); err != nil {
			return "", fmt.Errorf("image %s is no longer available on the server", image)
		}
	case app.SourceType == "image":
		image = app.Image
		fmt.Fprintf(w, "Pulling %s...\n", image)
		if err := c.Stream(ctx, w, "pull", image); err != nil {
			return "", err
		}
	default:
		dir := filepath.Join(e.DataDir, "builds", strconv.FormatInt(d.ID, 10))
		defer os.RemoveAll(dir)
		if err := e.checkout(ctx, w, app, d, dir); err != nil {
			return "", err
		}
		pack, err := detectBuildPack(app, dir)
		if err != nil {
			return "", err
		}
		if pack == "compose" {
			return "", e.deployCompose(ctx, w, c, app, t, dir, env)
		}
		image = fmt.Sprintf("selfdeploy/%s:%d", t.name, d.ID)
		if err := e.build(ctx, w, c, app, pack, dir, image, buildEnv); err != nil {
			return "", err
		}
	}

	if err := e.rollout(ctx, w, c, app, t, d.ID, image, env); err != nil {
		return "", err
	}
	if strings.HasPrefix(image, "selfdeploy/") {
		e.pruneImages(ctx, c, t.name, image)
	}
	for _, dom := range t.domains {
		fmt.Fprintf(w, "🌐 %s\n", dom.URL())
	}
	return image, nil
}

// envFor splits the app's variables into runtime env and build args, adding
// the defaults most frameworks expect.
func (e *Engine) envFor(app *store.App, t target, d *store.Deployment) (env, build []string, err error) {
	vars, err := e.Store.EnvVars(app.ID)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, v := range vars {
		seen[v.Key] = true
		if v.IsBuild {
			build = append(build, v.Key+"="+v.Value)
		}
		env = append(env, v.Key+"="+v.Value)
	}
	if !seen["PORT"] {
		env = append(env, fmt.Sprintf("PORT=%d", app.Port))
	}
	if !seen["HOST"] {
		env = append(env, "HOST=0.0.0.0")
	}
	if len(t.domains) > 0 {
		env = append(env, "SELFDEPLOY_URL="+t.domains[0].URL())
	}
	if d.PRNumber > 0 {
		env = append(env, fmt.Sprintf("SELFDEPLOY_PR=%d", d.PRNumber))
	}
	return env, build, nil
}

// rollout starts the new container next to the old one, waits for it to be
// healthy, then removes the old containers so traffic never drops.
func (e *Engine) rollout(ctx context.Context, w io.Writer, c *docker.Client, app *store.App, t target, id int64, image string, env []string) error {
	newName := fmt.Sprintf("%s-%d", t.name, id)
	port := app.Port
	if port == 0 {
		port = 80
	}
	args := []string{"run", "-d", "--name", newName, "--network", proxy.Network, "--restart", "unless-stopped",
		"--network-alias", t.name,
		"--label", labelApp + "=" + t.name,
		"--label", labelDeployment + "=" + strconv.FormatInt(id, 10),
		"--label", "selfdeploy.managed=true",
		"--log-opt", "max-size=10m", "--log-opt", "max-file=3"}
	if app.MemoryLimit != "" {
		args = append(args, "--memory", app.MemoryLimit)
	}
	if app.CPULimit != "" {
		args = append(args, "--cpus", app.CPULimit)
	}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	for _, l := range proxy.LabelsWith(t.name, port, t.domains, t.opts) {
		args = append(args, "--label", l)
	}
	if app.SourceType == "image" && app.StartCommand != "" {
		args = append(args, "--entrypoint", "sh", image, "-c", app.StartCommand)
	} else {
		args = append(args, image)
	}

	c.Remove(ctx, newName)
	fmt.Fprintf(w, "Starting container %s...\n", newName)
	if _, err := c.Run(ctx, args...); err != nil {
		return err
	}
	if err := e.waitHealthy(ctx, w, c, newName, port, app.HealthPath); err != nil {
		if logs, lerr := c.Run(ctx, "logs", "--tail", "60", newName); lerr == nil && logs != "" {
			fmt.Fprintf(w, "--- container logs ---\n%s\n----------------------\n", logs)
		}
		c.Remove(ctx, newName)
		return err
	}
	old, _ := c.ContainersByLabel(ctx, labelApp+"="+t.name)
	for _, name := range old {
		if name != newName {
			fmt.Fprintf(w, "Removing previous container %s\n", name)
			c.Run(ctx, "stop", "-t", "15", name)
			c.Remove(ctx, name)
		}
	}
	return nil
}

func (e *Engine) waitHealthy(ctx context.Context, w io.Writer, c *docker.Client, name string, port int, path string) error {
	if path == "" {
		fmt.Fprintln(w, "Waiting for the container to stay up (set a health check path for stricter checks)...")
		time.Sleep(5 * time.Second)
		if st := c.ContainerState(ctx, name); st != "running" {
			return fmt.Errorf("container is %s instead of running", st)
		}
		return nil
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	url := fmt.Sprintf("http://%s:%d%s", name, port, path)
	fmt.Fprintf(w, "Health checking %s...\n", url)
	for i := 0; i < 40; i++ {
		if st := c.ContainerState(ctx, name); st != "running" && st != "created" {
			return fmt.Errorf("container is %s", st)
		}
		_, err := c.Run(ctx, "run", "--rm", "--network", proxy.Network, "busybox:1.36",
			"wget", "-q", "-O", "/dev/null", "-T", "5", url)
		if err == nil {
			fmt.Fprintln(w, "Health check passed.")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return errors.New("health check did not pass within 2 minutes")
}

// pruneImages keeps the newest few images of an app so rollbacks stay possible.
func (e *Engine) pruneImages(ctx context.Context, c *docker.Client, name, current string) {
	out, err := c.Run(ctx, "images", "selfdeploy/"+name, "--format", "{{.Tag}}")
	if err != nil || out == "" {
		return
	}
	var tags []int
	for _, t := range strings.Split(out, "\n") {
		if n, err := strconv.Atoi(t); err == nil {
			tags = append(tags, n)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(tags)))
	for i, t := range tags {
		img := fmt.Sprintf("selfdeploy/%s:%d", name, t)
		if i >= keepImages && img != current {
			c.Run(ctx, "rmi", img)
		}
	}
}
