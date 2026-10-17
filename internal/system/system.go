// Package system reports host metrics and sets up SSH access to remote servers.
package system

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/99proteam/selfdeploy/internal/docker"
)

type Metrics struct {
	CPUs        int     `json:"cpus"`
	Load1       float64 `json:"load1"`
	MemTotal    uint64  `json:"mem_total"`
	MemUsed     uint64  `json:"mem_used"`
	DiskTotal   uint64  `json:"disk_total"`
	DiskUsed    uint64  `json:"disk_used"`
	UptimeHours float64 `json:"uptime_hours"`
}

// LocalMetrics reads /proc and statfs. Inside a container /proc/meminfo and
// /proc/loadavg still describe the host, which is what we want.
func LocalMetrics(dataDir string) Metrics {
	m := Metrics{}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			m.Load1, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			s, _ := strconv.ParseFloat(f[0], 64)
			m.UptimeHours = s / 3600
		}
	}
	if f, err := os.Open("/proc/cpuinfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "processor") {
				m.CPUs++
			}
		}
		f.Close()
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		vals := map[string]uint64{}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) >= 2 {
				v, _ := strconv.ParseUint(fields[1], 10, 64)
				vals[strings.TrimSuffix(fields[0], ":")] = v * 1024
			}
		}
		f.Close()
		m.MemTotal = vals["MemTotal"]
		if avail, ok := vals["MemAvailable"]; ok && avail <= m.MemTotal {
			m.MemUsed = m.MemTotal - avail
		}
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dataDir, &st); err == nil {
		m.DiskTotal = st.Blocks * uint64(st.Bsize)
		m.DiskUsed = m.DiskTotal - st.Bavail*uint64(st.Bsize)
	}
	return m
}

type ContainerStat struct {
	Name     string `json:"name"`
	CPU      string `json:"cpu"`
	Mem      string `json:"mem"`
	MemPerc  string `json:"mem_perc"`
	NetIO    string `json:"net_io"`
	BlockIO  string `json:"block_io"`
	PIDs     string `json:"pids"`
	Instance string `json:"-"`
}

// ContainerStats returns a one-shot `docker stats` snapshot of all containers.
func ContainerStats(ctx context.Context, c *docker.Client) ([]ContainerStat, error) {
	out, err := c.Run(ctx, "stats", "--no-stream", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var list []ContainerStat
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		var raw map[string]string
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		list = append(list, ContainerStat{Name: raw["Name"], CPU: raw["CPUPerc"], Mem: raw["MemUsage"],
			MemPerc: raw["MemPerc"], NetIO: raw["NetIO"], BlockIO: raw["BlockIO"], PIDs: raw["PIDs"]})
	}
	return list, nil
}

// RemoteMetrics asks a remote docker daemon for CPU count and memory.
func RemoteMetrics(ctx context.Context, c *docker.Client) Metrics {
	m := Metrics{}
	out, err := c.Run(ctx, "info", "--format", "{{.NCPU}} {{.MemTotal}}")
	if err == nil {
		fmt.Sscan(out, &m.CPUs, &m.MemTotal)
	}
	return m
}

// PublicIP asks a public echo service for this server's address.
func PublicIP() string {
	client := &http.Client{Timeout: 5 * time.Second}
	for _, u := range []string{"https://api.ipify.org", "https://ifconfig.me/ip", "https://icanhazip.com"} {
		resp, err := client.Get(u)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if ip := strings.TrimSpace(string(b)); resp.StatusCode == 200 && ip != "" && !strings.ContainsAny(ip, " <") {
			return ip
		}
	}
	return ""
}

// EnsureSSHKey creates SelfDeploy's SSH key (used for remote servers and
// private git repos over SSH) and an ssh config that uses it.
func EnsureSSHKey(dataDir string) (publicKey string, err error) {
	dir := filepath.Join(dataDir, "ssh")
	key := filepath.Join(dir, "id_ed25519")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if _, err := os.Stat(key); os.IsNotExist(err) {
		out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "selfdeploy", "-f", key).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("ssh-keygen: %v: %s", err, out)
		}
	}
	home, err := os.UserHomeDir()
	if err == nil {
		sshDir := filepath.Join(home, ".ssh")
		os.MkdirAll(sshDir, 0o700)
		cfgPath := filepath.Join(sshDir, "config")
		// Never overwrite an ssh config we did not write (e.g. when run outside docker).
		if old, err := os.ReadFile(cfgPath); err == nil && !strings.HasPrefix(string(old), sshMarker) {
			return readPub(key)
		}
		cfg := sshMarker + fmt.Sprintf("Host *\n  IdentityFile %s\n  StrictHostKeyChecking accept-new\n  UserKnownHostsFile %s\n  ServerAliveInterval 30\n  ControlMaster auto\n  ControlPath %s/cm-%%r@%%h:%%p\n  ControlPersist 10m\n",
			key, filepath.Join(dir, "known_hosts"), dir)
		os.WriteFile(cfgPath, []byte(cfg), 0o600)
	}
	return readPub(key)
}

const sshMarker = "# managed by selfdeploy\n"

func readPub(key string) (string, error) {
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(pub)), nil
}
