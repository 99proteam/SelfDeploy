// Package docker drives the docker CLI, locally or on a remote server over SSH
// (DOCKER_HOST=ssh://user@host), so every feature works the same on any server.
package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Client struct {
	// Host is a DOCKER_HOST value; empty means the local daemon.
	Host string
}

func (c *Client) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	if c.Host != "" {
		cmd.Env = append(cmd.Env, "DOCKER_HOST="+c.Host)
	}
	return cmd
}

// Run executes a docker command and returns its trimmed stdout.
func (c *Client) Run(ctx context.Context, args ...string) (string, error) {
	return c.RunInput(ctx, nil, args...)
}

func (c *Client) RunInput(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	cmd := c.command(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdin = stdin
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("docker %s: %w: %s", firstArgs(args), err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Stream runs a docker command, copying stdout and stderr to w (used for build logs).
func (c *Client) Stream(ctx context.Context, w io.Writer, args ...string) error {
	return c.StreamIn(ctx, w, "", args...)
}

// StreamIn is Stream with a working directory.
func (c *Client) StreamIn(ctx context.Context, w io.Writer, dir string, args ...string) error {
	cmd := c.command(ctx, "docker", args...)
	cmd.Dir = dir
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w", firstArgs(args), err)
	}
	return nil
}

// Combined returns stdout and stderr interleaved, as needed for container logs.
func (c *Client) Combined(ctx context.Context, args ...string) (string, error) {
	cmd := c.command(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", firstArgs(args), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Output runs a docker command writing only stdout to w; stderr is kept for the error.
func (c *Client) Output(ctx context.Context, w io.Writer, args ...string) error {
	cmd := c.command(ctx, "docker", args...)
	var stderr bytes.Buffer
	cmd.Stdout = w
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w: %s", firstArgs(args), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Exec runs an arbitrary program (git, nixpacks) with this client's DOCKER_HOST.
func (c *Client) Exec(ctx context.Context, w io.Writer, dir, name string, args ...string) error {
	cmd := c.command(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func firstArgs(args []string) string {
	if len(args) > 2 {
		args = args[:2]
	}
	return strings.Join(args, " ")
}

// EnsureNetwork creates a bridge network if it does not exist yet.
func (c *Client) EnsureNetwork(ctx context.Context, name string) error {
	if _, err := c.Run(ctx, "network", "inspect", name); err == nil {
		return nil
	}
	_, err := c.Run(ctx, "network", "create", name)
	return err
}

// ContainerState returns "running", "exited", ... or "" when the container does not exist.
func (c *Client) ContainerState(ctx context.Context, name string) string {
	out, err := c.Run(ctx, "inspect", "-f", "{{.State.Status}}", name)
	if err != nil {
		return ""
	}
	return out
}

func (c *Client) ContainerLabel(ctx context.Context, name, label string) string {
	out, _ := c.Run(ctx, "inspect", "-f", fmt.Sprintf(`{{index .Config.Labels %q}}`, label), name)
	return out
}

// ContainersByLabel lists container names (running or not) matching a label filter like "key=value".
func (c *Client) ContainersByLabel(ctx context.Context, label string) ([]string, error) {
	out, err := c.Run(ctx, "ps", "-a", "--filter", "label="+label, "--format", "{{.Names}}")
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// Remove force-removes containers, ignoring ones that are already gone.
func (c *Client) Remove(ctx context.Context, names ...string) {
	for _, n := range names {
		c.Run(ctx, "rm", "-f", n)
	}
}

// Ping reports whether the daemon is reachable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Run(ctx, "version", "--format", "{{.Server.Version}}")
	return err
}
