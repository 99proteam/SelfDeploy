// Package store holds SelfDeploy's persistent state in a single SQLite file.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

type Store struct {
	DB *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows a single writer; serialising through one connection
	// avoids SQLITE_BUSY under concurrent deploys.
	db.SetMaxOpenConns(1)
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

var migrations = []string{
	`CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		email TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		password_hash TEXT NOT NULL,
		is_admin INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at DATETIME NOT NULL
	);
	CREATE TABLE api_tokens (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		token_hash TEXT NOT NULL UNIQUE,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE servers (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		host TEXT NOT NULL DEFAULT '',
		port INTEGER NOT NULL DEFAULT 22,
		user TEXT NOT NULL DEFAULT 'root',
		is_local INTEGER NOT NULL DEFAULT 0,
		public_ip TEXT NOT NULL DEFAULT '',
		proxy_status TEXT NOT NULL DEFAULT 'unknown',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE projects (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE apps (
		id INTEGER PRIMARY KEY,
		project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		server_id INTEGER NOT NULL REFERENCES servers(id),
		name TEXT NOT NULL,
		slug TEXT NOT NULL UNIQUE,
		source_type TEXT NOT NULL DEFAULT 'git',
		git_url TEXT NOT NULL DEFAULT '',
		git_branch TEXT NOT NULL DEFAULT 'main',
		git_token TEXT NOT NULL DEFAULT '',
		build_pack TEXT NOT NULL DEFAULT 'auto',
		base_dir TEXT NOT NULL DEFAULT '.',
		dockerfile_path TEXT NOT NULL DEFAULT 'Dockerfile',
		compose_path TEXT NOT NULL DEFAULT 'docker-compose.yml',
		compose_service TEXT NOT NULL DEFAULT '',
		publish_dir TEXT NOT NULL DEFAULT 'dist',
		install_command TEXT NOT NULL DEFAULT '',
		build_command TEXT NOT NULL DEFAULT '',
		start_command TEXT NOT NULL DEFAULT '',
		image TEXT NOT NULL DEFAULT '',
		port INTEGER NOT NULL DEFAULT 3000,
		domains TEXT NOT NULL DEFAULT '',
		health_path TEXT NOT NULL DEFAULT '',
		memory_limit TEXT NOT NULL DEFAULT '',
		cpu_limit TEXT NOT NULL DEFAULT '',
		auto_deploy INTEGER NOT NULL DEFAULT 1,
		preview_deploys INTEGER NOT NULL DEFAULT 0,
		webhook_secret TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'created',
		current_image TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE env_vars (
		id INTEGER PRIMARY KEY,
		app_id INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		is_build INTEGER NOT NULL DEFAULT 0,
		UNIQUE(app_id, key)
	);
	CREATE TABLE deployments (
		id INTEGER PRIMARY KEY,
		app_id INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		trigger TEXT NOT NULL,
		commit_sha TEXT NOT NULL DEFAULT '',
		commit_message TEXT NOT NULL DEFAULT '',
		pr_number INTEGER NOT NULL DEFAULT 0,
		git_ref TEXT NOT NULL DEFAULT '',
		rollback_image TEXT NOT NULL DEFAULT '',
		image TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'queued',
		logs TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		finished_at DATETIME
	);
	CREATE INDEX deployments_app ON deployments(app_id, id DESC);
	CREATE TABLE databases (
		id INTEGER PRIMARY KEY,
		project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		server_id INTEGER NOT NULL REFERENCES servers(id),
		name TEXT NOT NULL,
		slug TEXT NOT NULL UNIQUE,
		engine TEXT NOT NULL,
		version TEXT NOT NULL,
		db_user TEXT NOT NULL DEFAULT '',
		db_password TEXT NOT NULL DEFAULT '',
		db_name TEXT NOT NULL DEFAULT '',
		public_port INTEGER NOT NULL DEFAULT 0,
		backup_cron TEXT NOT NULL DEFAULT '',
		backup_keep INTEGER NOT NULL DEFAULT 7,
		backup_s3 INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'created',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE backups (
		id INTEGER PRIMARY KEY,
		database_id INTEGER NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
		file TEXT NOT NULL DEFAULT '',
		size INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'running',
		message TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE services (
		id INTEGER PRIMARY KEY,
		project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		server_id INTEGER NOT NULL REFERENCES servers(id),
		template TEXT NOT NULL,
		name TEXT NOT NULL,
		slug TEXT NOT NULL UNIQUE,
		domain TEXT NOT NULL DEFAULT '',
		variables TEXT NOT NULL DEFAULT '{}',
		status TEXT NOT NULL DEFAULT 'created',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE mail_domains (
		id INTEGER PRIMARY KEY,
		domain TEXT NOT NULL UNIQUE,
		dkim_record TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE mailboxes (
		id INTEGER PRIMARY KEY,
		domain_id INTEGER NOT NULL REFERENCES mail_domains(id) ON DELETE CASCADE,
		address TEXT NOT NULL UNIQUE,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE mail_aliases (
		id INTEGER PRIMARY KEY,
		source TEXT NOT NULL,
		destination TEXT NOT NULL,
		UNIQUE(source, destination)
	);
	CREATE TABLE notifications (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		kind TEXT NOT NULL,
		target TEXT NOT NULL,
		extra TEXT NOT NULL DEFAULT '',
		events TEXT NOT NULL DEFAULT 'deploy_success,deploy_failed,backup_failed',
		enabled INTEGER NOT NULL DEFAULT 1
	);`,
	`ALTER TABLE apps ADD COLUMN basic_auth_user TEXT NOT NULL DEFAULT '';
	ALTER TABLE apps ADD COLUMN basic_auth_hash TEXT NOT NULL DEFAULT '';`,
}

func (s *Store) migrate() error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var version int
	if err := s.DB.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ---- settings ----

func (s *Store) Setting(key string) string {
	var v string
	s.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) Settings() (map[string]string, error) {
	rows, err := s.DB.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func now() time.Time { return time.Now().UTC() }
