// Package databases runs one-click PostgreSQL, MySQL, MariaDB, MongoDB and
// Redis containers, with scheduled backups to disk and S3.
package databases

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/99proteam/selfdeploy/internal/docker"
	"github.com/99proteam/selfdeploy/internal/proxy"
	"github.com/99proteam/selfdeploy/internal/store"
)

type Engine struct {
	Image          string
	DefaultVersion string
	Port           int
	DataPath       string
	BackupExt      string
}

var Engines = map[string]Engine{
	"postgres": {Image: "postgres", DefaultVersion: "16-alpine", Port: 5432, DataPath: "/var/lib/postgresql/data", BackupExt: "sql.gz"},
	"mysql":    {Image: "mysql", DefaultVersion: "8.4", Port: 3306, DataPath: "/var/lib/mysql", BackupExt: "sql.gz"},
	"mariadb":  {Image: "mariadb", DefaultVersion: "11", Port: 3306, DataPath: "/var/lib/mysql", BackupExt: "sql.gz"},
	"mongodb":  {Image: "mongo", DefaultVersion: "7", Port: 27017, DataPath: "/data/db", BackupExt: "archive.gz"},
	"redis":    {Image: "redis", DefaultVersion: "7-alpine", Port: 6379, DataPath: "/data", BackupExt: "rdb"},
}

func volume(d *store.Database) string { return "selfdeploy-db-" + d.Slug }

// envFor returns the container environment that initialises users and passwords.
func envFor(d *store.Database) []string {
	switch d.Engine {
	case "postgres":
		return []string{"POSTGRES_USER=" + d.User, "POSTGRES_PASSWORD=" + d.Password, "POSTGRES_DB=" + d.DBName}
	case "mysql":
		return []string{"MYSQL_USER=" + d.User, "MYSQL_PASSWORD=" + d.Password, "MYSQL_DATABASE=" + d.DBName,
			"MYSQL_ROOT_PASSWORD=" + d.Password}
	case "mariadb":
		return []string{"MARIADB_USER=" + d.User, "MARIADB_PASSWORD=" + d.Password, "MARIADB_DATABASE=" + d.DBName,
			"MARIADB_ROOT_PASSWORD=" + d.Password}
	case "mongodb":
		return []string{"MONGO_INITDB_ROOT_USERNAME=" + d.User, "MONGO_INITDB_ROOT_PASSWORD=" + d.Password,
			"MONGO_INITDB_DATABASE=" + d.DBName}
	}
	return nil
}

// InternalURL is the connection string apps on the same server use.
func InternalURL(d *store.Database) string { return connURL(d, d.Slug, Engines[d.Engine].Port) }

// PublicURL is the connection string from outside, when a public port is set.
func PublicURL(d *store.Database, host string) string {
	if d.PublicPort == 0 || host == "" {
		return ""
	}
	return connURL(d, host, d.PublicPort)
}

func connURL(d *store.Database, host string, port int) string {
	hp := host + ":" + strconv.Itoa(port)
	user := url.UserPassword(d.User, d.Password)
	switch d.Engine {
	case "postgres":
		return (&url.URL{Scheme: "postgres", User: user, Host: hp, Path: "/" + d.DBName}).String()
	case "mysql", "mariadb":
		return (&url.URL{Scheme: "mysql", User: user, Host: hp, Path: "/" + d.DBName}).String()
	case "mongodb":
		return (&url.URL{Scheme: "mongodb", User: user, Host: hp, Path: "/" + d.DBName, RawQuery: "authSource=admin"}).String()
	case "redis":
		return (&url.URL{Scheme: "redis", User: url.UserPassword("default", d.Password), Host: hp}).String()
	}
	return ""
}

func client(s *store.Store, d *store.Database) (*docker.Client, error) {
	srv, err := s.Server(d.ServerID)
	if err != nil {
		return nil, err
	}
	return docker.ForServer(srv.IsLocal, srv.User, srv.Host, srv.Port), nil
}

// Start (re)creates the database container. Data lives in a named volume,
// so recreating it to change the public port is safe.
func Start(ctx context.Context, s *store.Store, d *store.Database) error {
	eng, ok := Engines[d.Engine]
	if !ok {
		return fmt.Errorf("unknown engine %q", d.Engine)
	}
	c, err := client(s, d)
	if err != nil {
		return err
	}
	if err := c.EnsureNetwork(ctx, proxy.Network); err != nil {
		return err
	}
	c.Remove(ctx, d.Slug)
	args := []string{"run", "-d", "--name", d.Slug, "--network", proxy.Network, "--restart", "unless-stopped",
		"-v", volume(d) + ":" + eng.DataPath,
		"--label", "selfdeploy.managed=true", "--label", "selfdeploy.database=" + d.Slug,
		"--log-opt", "max-size=10m", "--log-opt", "max-file=3"}
	if d.PublicPort > 0 {
		args = append(args, "-p", fmt.Sprintf("%d:%d", d.PublicPort, eng.Port))
	}
	for _, kv := range envFor(d) {
		args = append(args, "-e", kv)
	}
	args = append(args, eng.Image+":"+d.Version)
	if d.Engine == "redis" {
		args = append(args, "redis-server", "--requirepass", d.Password, "--appendonly", "yes")
	}
	if _, err := c.Run(ctx, args...); err != nil {
		s.SetDatabaseStatus(d.ID, "failed")
		return err
	}
	s.SetDatabaseStatus(d.ID, "running")
	return nil
}

func Stop(ctx context.Context, s *store.Store, d *store.Database) error {
	c, err := client(s, d)
	if err != nil {
		return err
	}
	if _, err := c.Run(ctx, "stop", d.Slug); err != nil {
		return err
	}
	s.SetDatabaseStatus(d.ID, "stopped")
	return nil
}

// Destroy removes the container and, when asked, the data volume.
func Destroy(ctx context.Context, s *store.Store, d *store.Database, removeData bool) error {
	c, err := client(s, d)
	if err != nil {
		return err
	}
	c.Remove(ctx, d.Slug)
	if removeData {
		c.Run(ctx, "volume", "rm", volume(d))
	}
	return nil
}

func Logs(ctx context.Context, s *store.Store, d *store.Database, tail int) (string, error) {
	c, err := client(s, d)
	if err != nil {
		return "", err
	}
	return c.Combined(ctx, "logs", "--timestamps", "--tail", strconv.Itoa(tail), d.Slug)
}

func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Hour)
}
