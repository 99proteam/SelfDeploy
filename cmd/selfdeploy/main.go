// Command selfdeploy is the SelfDeploy server: an open-source, self-hosted
// PaaS with git auto-deploy, automatic SSL, databases, mail and one-click apps.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/99proteam/selfdeploy/internal/api"
	"github.com/99proteam/selfdeploy/internal/databases"
	"github.com/99proteam/selfdeploy/internal/deploy"
	"github.com/99proteam/selfdeploy/internal/mail"
	"github.com/99proteam/selfdeploy/internal/notify"
	"github.com/99proteam/selfdeploy/internal/proxy"
	"github.com/99proteam/selfdeploy/internal/services"
	"github.com/99proteam/selfdeploy/internal/store"
	"github.com/99proteam/selfdeploy/internal/system"
	"github.com/99proteam/selfdeploy/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	dataDir := env("SELFDEPLOY_DATA", "/data/selfdeploy")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	st, err := store.Open(filepath.Join(dataDir, "selfdeploy.db"))
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer st.Close()

	if len(os.Args) > 1 {
		os.Exit(command(st, os.Args[1:]))
	}

	proxy.DynamicDir = filepath.Join(dataDir, "proxy")
	if err := ensureLocalServer(st); err != nil {
		log.Fatalf("local server: %v", err)
	}
	pubKey, err := system.EnsureSSHKey(dataDir)
	if err != nil {
		log.Printf("warning: SSH key unavailable (remote servers and git over SSH will not work): %v", err)
	}

	n := notify.New(st)
	engine := deploy.New(st, n, dataDir)
	workers, _ := strconv.Atoi(env("SELFDEPLOY_BUILD_WORKERS", "2"))
	engine.StartWorkers(max(workers, 1))
	backups := databases.NewBackups(st, n, dataDir)
	backups.Schedule()

	webFS, err := fs.Sub(web.Files, "static")
	if err != nil {
		log.Fatal(err)
	}
	srv := &api.Server{
		Store: st, Deploy: engine, Backups: backups, Notify: n, DataDir: dataDir, Version: version, Web: webFS,
		Services:     &services.Manager{Store: st, DataDir: dataDir},
		Mail:         &mail.Manager{Store: st},
		SSHPublicKey: pubKey,
	}
	if d := st.Setting("instance_domain"); d != "" {
		if err := proxy.WriteDashboardRoute(d, "http://selfdeploy:8000"); err != nil {
			log.Printf("dashboard route: %v", err)
		}
	}

	go func() {
		for range time.Tick(time.Hour) {
			st.PurgeExpiredSessions()
		}
	}()

	addr := env("SELFDEPLOY_ADDR", ":8000")
	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout: 2 * time.Minute}
	go func() {
		log.Printf("SelfDeploy %s listening on %s (data: %s)", version, addr, dataDir)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	httpSrv.Shutdown(ctx)
}

// ensureLocalServer registers the machine SelfDeploy runs on as a server.
func ensureLocalServer(st *store.Store) error {
	srv, err := st.LocalServer()
	if errors.Is(err, store.ErrNotFound) {
		ip := env("SELFDEPLOY_PUBLIC_IP", "")
		if ip == "" {
			ip = system.PublicIP()
		}
		return st.CreateServer(&store.Server{Name: "localhost", IsLocal: true, PublicIP: ip, Port: 22, User: "root"})
	}
	if err != nil {
		return err
	}
	if ip := env("SELFDEPLOY_PUBLIC_IP", ""); ip != "" && ip != srv.PublicIP {
		srv.PublicIP = ip
		return st.UpdateServer(srv)
	}
	if srv.PublicIP == "" {
		if srv.PublicIP = system.PublicIP(); srv.PublicIP != "" {
			return st.UpdateServer(srv)
		}
	}
	return nil
}

// command runs maintenance subcommands, e.g. `selfdeploy reset-password admin@example.com`.
func command(st *store.Store, args []string) int {
	switch args[0] {
	case "version":
		fmt.Println(version)
		return 0
	case "reset-password":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: selfdeploy reset-password <email>")
			return 2
		}
		u, err := st.UserByEmail(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "user %s not found\n", args[1])
			return 1
		}
		pw := services.RandomString(16)
		hash, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err := st.UpdatePassword(u.ID, string(hash)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("New password for %s: %s\n", u.Email, pw)
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown command %q (commands: version, reset-password)\n", args[0])
	return 2
}
