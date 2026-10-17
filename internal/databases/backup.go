package databases

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/99proteam/selfdeploy/internal/notify"
	"github.com/99proteam/selfdeploy/internal/s3"
	"github.com/99proteam/selfdeploy/internal/store"
)

type Backups struct {
	Store   *store.Store
	Notify  *notify.Notifier
	DataDir string

	mu      sync.Mutex
	cron    *cron.Cron
	entries map[int64]cron.EntryID
}

func NewBackups(s *store.Store, n *notify.Notifier, dataDir string) *Backups {
	b := &Backups{Store: s, Notify: n, DataDir: dataDir, cron: cron.New(), entries: map[int64]cron.EntryID{}}
	b.cron.Start()
	return b
}

func S3Config(s *store.Store) s3.Config {
	return s3.Config{Endpoint: s.Setting("s3_endpoint"), Region: s.Setting("s3_region"), Bucket: s.Setting("s3_bucket"),
		AccessKey: s.Setting("s3_access_key"), SecretKey: s.Setting("s3_secret_key")}
}

// ValidateCron checks a 5-field cron expression (or @daily, @hourly ...).
func ValidateCron(expr string) error {
	if expr == "" {
		return nil
	}
	_, err := cron.ParseStandard(expr)
	return err
}

// Schedule (re)registers every database's backup schedule.
func (b *Backups) Schedule() {
	dbs, err := b.Store.Databases(0)
	if err != nil {
		log.Printf("backups: %v", err)
		return
	}
	for _, d := range dbs {
		b.ScheduleOne(d)
	}
}

func (b *Backups) ScheduleOne(d *store.Database) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if id, ok := b.entries[d.ID]; ok {
		b.cron.Remove(id)
		delete(b.entries, d.ID)
	}
	if d.BackupCron == "" {
		return
	}
	dbID := d.ID
	id, err := b.cron.AddFunc(d.BackupCron, func() {
		if cur, err := b.Store.Database(dbID); err == nil {
			b.Run(cur)
		}
	})
	if err != nil {
		log.Printf("backups: database %s: invalid schedule %q: %v", d.Slug, d.BackupCron, err)
		return
	}
	b.entries[d.ID] = id
}

func (b *Backups) Unschedule(id int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e, ok := b.entries[id]; ok {
		b.cron.Remove(e)
		delete(b.entries, id)
	}
}

func (b *Backups) dir(d *store.Database) string { return filepath.Join(b.DataDir, "backups", d.Slug) }

// Run takes a backup now, uploads it to S3 if enabled, and applies retention.
func (b *Backups) Run(d *store.Database) *store.Backup {
	rec, err := b.Store.CreateBackup(d.ID)
	if err != nil {
		log.Printf("backups: %v", err)
		return nil
	}
	ctx, cancel := opCtx()
	defer cancel()
	err = b.dump(ctx, d, rec)
	if err == nil && d.BackupS3 {
		cfg := S3Config(b.Store)
		if !cfg.Valid() {
			err = errors.New("backup saved locally, but S3 storage is not configured in Settings")
		} else if err = s3.PutFile(ctx, cfg, s3Key(d, rec.File), rec.File); err != nil && strings.Contains(err.Error(), "NoSuchBucket") {
			if err = s3.EnsureBucket(ctx, cfg); err == nil {
				err = s3.PutFile(ctx, cfg, s3Key(d, rec.File), rec.File)
			}
		}
		if err == nil {
			rec.Message = "uploaded to S3"
		}
	}
	if err != nil {
		rec.Status, rec.Message = "failed", err.Error()
		b.Notify.Send(notify.BackupFailed, "Backup failed: "+d.Name, err.Error())
	} else {
		rec.Status = "success"
		b.Notify.Send(notify.BackupSuccess, "Backup finished: "+d.Name, fmt.Sprintf("%s (%d bytes)", filepath.Base(rec.File), rec.Size))
	}
	b.Store.FinishBackup(rec)
	b.prune(d)
	return rec
}

func s3Key(d *store.Database, file string) string {
	return "selfdeploy/" + d.Slug + "/" + filepath.Base(file)
}

func (b *Backups) dump(ctx context.Context, d *store.Database, rec *store.Backup) error {
	c, err := client(b.Store, d)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(b.dir(d), 0o700); err != nil {
		return err
	}
	eng := Engines[d.Engine]
	rec.File = filepath.Join(b.dir(d), time.Now().UTC().Format("20060102-150405")+"."+eng.BackupExt)
	f, err := os.OpenFile(rec.File, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	var out io.Writer = f
	var gz *gzip.Writer
	if eng.BackupExt == "sql.gz" {
		gz = gzip.NewWriter(f)
		out = gz
	}
	var args []string
	switch d.Engine {
	case "postgres":
		args = []string{"exec", d.Slug, "pg_dump", "-U", d.User, "-d", d.DBName, "--clean", "--if-exists"}
	case "mysql":
		args = []string{"exec", "-e", "MYSQL_PWD=" + d.Password, d.Slug, "mysqldump", "-u", "root",
			"--single-transaction", "--routines", "--triggers", "--databases", d.DBName}
	case "mariadb":
		args = []string{"exec", "-e", "MYSQL_PWD=" + d.Password, d.Slug, "mariadb-dump", "-u", "root",
			"--single-transaction", "--routines", "--triggers", "--databases", d.DBName}
	case "mongodb":
		args = []string{"exec", d.Slug, "mongodump", "--username", d.User, "--password", d.Password,
			"--authenticationDatabase", "admin", "--archive", "--gzip"}
	case "redis":
		args = []string{"exec", "-e", "REDISCLI_AUTH=" + d.Password, d.Slug, "redis-cli", "--rdb", "-"}
	}
	err = c.Output(ctx, out, args...)
	if gz != nil {
		if cerr := gz.Close(); err == nil {
			err = cerr
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(rec.File)
		return err
	}
	if st, err := os.Stat(rec.File); err == nil {
		rec.Size = st.Size()
	}
	return nil
}

// Restore loads a backup file back into the database.
func (b *Backups) Restore(d *store.Database, rec *store.Backup) error {
	c, err := client(b.Store, d)
	if err != nil {
		return err
	}
	f, err := os.Open(rec.File)
	if err != nil {
		return err
	}
	defer f.Close()
	var in io.Reader = f
	if Engines[d.Engine].BackupExt == "sql.gz" {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		in = gz
	}
	var args []string
	switch d.Engine {
	case "postgres":
		args = []string{"exec", "-i", d.Slug, "psql", "-q", "-U", d.User, "-d", d.DBName, "-v", "ON_ERROR_STOP=1"}
	case "mysql":
		args = []string{"exec", "-i", "-e", "MYSQL_PWD=" + d.Password, d.Slug, "mysql", "-u", "root"}
	case "mariadb":
		args = []string{"exec", "-i", "-e", "MYSQL_PWD=" + d.Password, d.Slug, "mariadb", "-u", "root"}
	case "mongodb":
		args = []string{"exec", "-i", d.Slug, "mongorestore", "--username", d.User, "--password", d.Password,
			"--authenticationDatabase", "admin", "--archive", "--gzip", "--drop"}
	default:
		return fmt.Errorf("restore is not supported for %s; copy the .rdb file into the data volume instead", d.Engine)
	}
	ctx, cancel := opCtx()
	defer cancel()
	_, err = c.RunInput(ctx, in, args...)
	return err
}

// prune keeps the newest BackupKeep successful backups (and the last few
// failure records, for troubleshooting) and deletes the rest.
func (b *Backups) prune(d *store.Database) {
	keep := d.BackupKeep
	if keep <= 0 {
		return
	}
	list, err := b.Store.Backups(d.ID)
	if err != nil {
		return
	}
	kept, failed := 0, 0
	for _, rec := range list {
		switch rec.Status {
		case "running":
		case "success":
			if kept++; kept > keep {
				b.Delete(d, rec)
			}
		default:
			if failed++; failed > 10 {
				b.Delete(d, rec)
			}
		}
	}
}

// Delete removes a backup locally and from S3.
func (b *Backups) Delete(d *store.Database, rec *store.Backup) {
	if rec.File != "" {
		os.Remove(rec.File)
		if d.BackupS3 {
			if cfg := S3Config(b.Store); cfg.Valid() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				s3.Delete(ctx, cfg, s3Key(d, rec.File))
				cancel()
			}
		}
	}
	b.Store.DeleteBackup(rec.ID)
}
