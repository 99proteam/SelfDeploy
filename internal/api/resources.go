package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"path/filepath"
	"strings"
	"time"

	"github.com/99proteam/selfdeploy/internal/databases"
	"github.com/99proteam/selfdeploy/internal/docker"
	"github.com/99proteam/selfdeploy/internal/proxy"
	"github.com/99proteam/selfdeploy/internal/services"
	"github.com/99proteam/selfdeploy/internal/store"
	"github.com/99proteam/selfdeploy/internal/system"
)

func reqCtx(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// ---- overview & settings ----

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	apps, _ := s.Store.Apps(0)
	dbs, _ := s.Store.Databases(0)
	svcs, _ := s.Store.Services(0)
	projects, _ := s.Store.Projects()
	servers, _ := s.Store.Servers()
	var recent []map[string]any
	rows, err := s.Store.DB.Query(`SELECT d.id, d.app_id, a.name, d.trigger, d.status, d.commit_sha, d.commit_message,
		d.created_at FROM deployments d JOIN apps a ON a.id = d.app_id ORDER BY d.id DESC LIMIT 10`)
	if err == nil {
		for rows.Next() {
			var id, appID int64
			var name, trigger, status, sha, msg string
			var created time.Time
			if rows.Scan(&id, &appID, &name, &trigger, &status, &sha, &msg, &created) == nil {
				recent = append(recent, map[string]any{"id": id, "app_id": appID, "app_name": name, "trigger": trigger,
					"status": status, "commit_sha": sha, "commit_message": msg, "created_at": created})
			}
		}
		rows.Close()
	}
	for _, a := range apps {
		a.GitToken = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"apps": apps, "databases": dbs, "services": svcs, "projects": projects, "servers": servers,
		"recent_deployments": recent, "metrics": system.LocalMetrics(s.DataDir), "version": s.Version,
	})
}

var settingKeys = map[string]bool{
	"instance_domain": true, "wildcard_domain": true, "acme_email": true,
	"s3_endpoint": true, "s3_region": true, "s3_bucket": true, "s3_access_key": true, "s3_secret_key": true,
	"smtp_host": true, "smtp_port": true, "smtp_user": true, "smtp_password": true, "smtp_from": true,
}

var secretSettings = map[string]bool{"s3_secret_key": true, "smtp_password": true}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	all, err := s.Store.Settings()
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]string{}
	for k := range settingKeys {
		v := all[k]
		if secretSettings[k] && v != "" {
			v = "********"
		}
		out[k] = v
	}
	out["mail_hostname"] = all["mail_hostname"]
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if err := decode(r, &in); err != nil {
		fail(w, err)
		return
	}
	for k, v := range in {
		if !settingKeys[k] {
			continue
		}
		v = strings.TrimSpace(v)
		if secretSettings[k] && v == "********" {
			continue
		}
		switch k {
		case "instance_domain":
			if v != "" {
				if _, err := proxy.ParseDomain(v); err != nil {
					fail(w, invalid("instance domain: %v", err))
					return
				}
			}
		case "wildcard_domain":
			v = strings.TrimPrefix(v, "*.")
			if v != "" {
				if _, err := proxy.ParseDomain(v); err != nil {
					fail(w, invalid("wildcard domain: %v", err))
					return
				}
			}
		case "acme_email":
			if v != "" {
				if _, err := mail.ParseAddress(v); err != nil {
					fail(w, invalid("invalid Let's Encrypt email"))
					return
				}
			}
		}
		if err := s.Store.SetSetting(k, v); err != nil {
			fail(w, err)
			return
		}
	}
	if _, ok := in["instance_domain"]; ok {
		s.applyInstanceDomain()
	}
	s.getSettings(w, r)
}

// applyInstanceDomain puts the dashboard itself behind its domain with HTTPS.
// The installer runs SelfDeploy as the "selfdeploy" container on the proxy network.
func (s *Server) applyInstanceDomain() {
	if err := proxy.WriteDashboardRoute(s.Store.Setting("instance_domain"), "http://selfdeploy:8000"); err != nil {
		log.Printf("dashboard route: %v", err)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := proxy.EnsureProxy(ctx, &docker.Client{}, s.Store.Setting("acme_email"), io.Discard); err != nil {
			log.Printf("proxy: %v", err)
		}
	}()
}

// ---- servers ----

func (s *Server) listServers(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.Servers()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) normalizeServer(v *store.Server) error {
	v.Name, v.Host, v.User = strings.TrimSpace(v.Name), strings.TrimSpace(v.Host), strings.TrimSpace(v.User)
	if v.Name == "" || v.Host == "" {
		return invalid("name and host are required")
	}
	if strings.ContainsAny(v.Host, " @/") || strings.HasPrefix(v.Host, "-") {
		return invalid("host must be an IP address or hostname")
	}
	if v.User == "" {
		v.User = "root"
	}
	if strings.ContainsAny(v.User, " @/:") || strings.HasPrefix(v.User, "-") {
		return invalid("invalid SSH user")
	}
	if v.Port == 0 {
		v.Port = 22
	}
	if v.PublicIP == "" {
		v.PublicIP = v.Host
	}
	return nil
}

func (s *Server) createServer(w http.ResponseWriter, r *http.Request) {
	var v store.Server
	if err := decode(r, &v); err != nil {
		fail(w, err)
		return
	}
	v.IsLocal = false
	if err := s.normalizeServer(&v); err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.CreateServer(&v); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updateServer(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	cur, err := s.Store.Server(id)
	if err != nil {
		fail(w, err)
		return
	}
	var v store.Server
	if err := decode(r, &v); err != nil {
		fail(w, err)
		return
	}
	v.ID, v.IsLocal = id, cur.IsLocal
	if cur.IsLocal {
		v.Host, v.User, v.Port = cur.Host, cur.User, cur.Port
		if v.Name == "" {
			v.Name = cur.Name
		}
	} else if err := s.normalizeServer(&v); err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.UpdateServer(&v); err != nil {
		fail(w, err)
		return
	}
	fresh, _ := s.Store.Server(id)
	writeJSON(w, http.StatusOK, fresh)
}

func (s *Server) deleteServer(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	var n int
	s.Store.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM apps WHERE server_id = ?) + (SELECT COUNT(*) FROM databases WHERE server_id = ?)
		+ (SELECT COUNT(*) FROM services WHERE server_id = ?)`, id, id, id).Scan(&n)
	if n > 0 {
		writeErr(w, http.StatusConflict, "move or delete the resources on this server first")
		return
	}
	if err := s.Store.DeleteServer(id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) serverClient(w http.ResponseWriter, r *http.Request) (*docker.Client, *store.Server) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return nil, nil
	}
	srv, err := s.Store.Server(id)
	if err != nil {
		fail(w, err)
		return nil, nil
	}
	return docker.ForServer(srv.IsLocal, srv.User, srv.Host, srv.Port), srv
}

func (s *Server) validateServer(w http.ResponseWriter, r *http.Request) {
	c, _ := s.serverClient(w, r)
	if c == nil {
		return
	}
	ctx, cancel := reqCtx(r, 30*time.Second)
	defer cancel()
	ver, err := c.Run(ctx, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "docker_version": ver})
}

func (s *Server) installProxy(w http.ResponseWriter, r *http.Request) {
	c, srv := s.serverClient(w, r)
	if c == nil {
		return
	}
	t := s.startTask("Install proxy on "+srv.Name, 10*time.Minute, func(ctx context.Context, t *task) error {
		err := proxy.EnsureProxy(ctx, c, s.Store.Setting("acme_email"), t)
		status := "running"
		if err != nil {
			status = "error"
		} else {
			fmt.Fprintln(t, "Proxy is running.")
		}
		s.Store.SetServerProxyStatus(srv.ID, status)
		return err
	})
	taskResponse(w, t)
}

func (s *Server) serverStats(w http.ResponseWriter, r *http.Request) {
	c, srv := s.serverClient(w, r)
	if c == nil {
		return
	}
	ctx, cancel := reqCtx(r, 30*time.Second)
	defer cancel()
	m := system.RemoteMetrics(ctx, c)
	if srv.IsLocal {
		m = system.LocalMetrics(s.DataDir)
	}
	stats, err := system.ContainerStats(ctx, c)
	resp := map[string]any{"metrics": m, "containers": stats}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) sshKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"public_key": s.SSHPublicKey})
}

// ---- projects ----

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.Projects()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var p store.Project
	if err := decode(r, &p); err != nil {
		fail(w, err)
		return
	}
	if p.Name = strings.TrimSpace(p.Name); p.Name == "" {
		fail(w, invalid("name is required"))
		return
	}
	if err := s.Store.CreateProject(&p); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	p, err := s.Store.Project(id)
	if err != nil {
		fail(w, err)
		return
	}
	apps, _ := s.Store.Apps(id)
	for _, a := range apps {
		a.GitToken = ""
	}
	dbs, _ := s.Store.Databases(id)
	svcs, _ := s.Store.Services(id)
	writeJSON(w, http.StatusOK, map[string]any{"project": p, "apps": apps, "databases": dbs, "services": svcs})
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	var p store.Project
	if err := decode(r, &p); err != nil {
		fail(w, err)
		return
	}
	p.ID = id
	if p.Name = strings.TrimSpace(p.Name); p.Name == "" {
		fail(w, invalid("name is required"))
		return
	}
	if err := s.Store.UpdateProject(&p); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	apps, _ := s.Store.Apps(id)
	dbs, _ := s.Store.Databases(id)
	svcs, _ := s.Store.Services(id)
	if len(apps)+len(dbs)+len(svcs) > 0 {
		writeErr(w, http.StatusConflict, "delete the project's apps, databases and services first")
		return
	}
	if err := s.Store.DeleteProject(id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- databases ----

func (s *Server) dbView(d *store.Database) map[string]any {
	host := ""
	if srv, err := s.Store.Server(d.ServerID); err == nil {
		host = srv.PublicIP
	}
	return map[string]any{"database": d, "internal_url": databases.InternalURL(d),
		"public_url": databases.PublicURL(d, host), "internal_host": d.Slug,
		"port": databases.Engines[d.Engine].Port}
}

func (s *Server) listDatabases(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.Databases(int64(queryInt(r, "project", 0)))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createDatabase(w http.ResponseWriter, r *http.Request) {
	var d store.Database
	if err := decode(r, &d); err != nil {
		fail(w, err)
		return
	}
	eng, ok := databases.Engines[d.Engine]
	if !ok {
		fail(w, invalid("engine must be postgres, mysql, mariadb, mongodb or redis"))
		return
	}
	if d.Name = strings.TrimSpace(d.Name); d.Name == "" {
		d.Name = d.Engine
	}
	if _, err := s.Store.Project(d.ProjectID); err != nil {
		fail(w, invalid("project not found"))
		return
	}
	if d.ServerID == 0 {
		srv, err := s.Store.LocalServer()
		if err != nil {
			fail(w, err)
			return
		}
		d.ServerID = srv.ID
	}
	if d.Version == "" {
		d.Version = eng.DefaultVersion
	}
	if strings.ContainsAny(d.Version, " :/@") {
		fail(w, invalid("invalid version tag"))
		return
	}
	if err := databases.ValidateCron(d.BackupCron); err != nil {
		fail(w, invalid("invalid backup schedule: %v", err))
		return
	}
	if d.PublicPort < 0 || d.PublicPort > 65535 {
		fail(w, invalid("invalid public port"))
		return
	}
	d.Slug = s.slugFor(d.Name)
	d.User, d.DBName, d.Password = "selfdeploy", "app", services.RandomString(28)
	if d.Engine == "mongodb" {
		d.User = "root"
	}
	if d.BackupKeep == 0 {
		d.BackupKeep = 7
	}
	if err := s.Store.CreateDatabase(&d); err != nil {
		fail(w, err)
		return
	}
	s.Backups.ScheduleOne(&d)
	t := s.startTask("Start database "+d.Name, 15*time.Minute, func(ctx context.Context, t *task) error {
		fmt.Fprintf(t, "Pulling %s:%s and starting...\n", eng.Image, d.Version)
		return databases.Start(ctx, s.Store, &d)
	})
	writeJSON(w, http.StatusCreated, map[string]any{"database": d, "task_id": t.ID})
}

func (s *Server) loadDatabase(w http.ResponseWriter, r *http.Request) *store.Database {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return nil
	}
	d, err := s.Store.Database(id)
	if err != nil {
		fail(w, err)
		return nil
	}
	return d
}

func (s *Server) getDatabase(w http.ResponseWriter, r *http.Request) {
	if d := s.loadDatabase(w, r); d != nil {
		writeJSON(w, http.StatusOK, s.dbView(d))
	}
}

func (s *Server) updateDatabase(w http.ResponseWriter, r *http.Request) {
	d := s.loadDatabase(w, r)
	if d == nil {
		return
	}
	var in store.Database
	if err := decode(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := databases.ValidateCron(in.BackupCron); err != nil {
		fail(w, invalid("invalid backup schedule: %v", err))
		return
	}
	if in.PublicPort < 0 || in.PublicPort > 65535 {
		fail(w, invalid("invalid public port"))
		return
	}
	portChanged := in.PublicPort != d.PublicPort
	if strings.TrimSpace(in.Name) != "" {
		d.Name = strings.TrimSpace(in.Name)
	}
	d.PublicPort, d.BackupCron, d.BackupKeep, d.BackupS3 = in.PublicPort, in.BackupCron, in.BackupKeep, in.BackupS3
	if err := s.Store.UpdateDatabase(d); err != nil {
		fail(w, err)
		return
	}
	s.Backups.ScheduleOne(d)
	if portChanged && d.Status == "running" {
		ctx, cancel := reqCtx(r, 2*time.Minute)
		defer cancel()
		if err := databases.Start(ctx, s.Store, d); err != nil {
			fail(w, err)
			return
		}
	}
	fresh, _ := s.Store.Database(d.ID)
	writeJSON(w, http.StatusOK, s.dbView(fresh))
}

func (s *Server) deleteDatabase(w http.ResponseWriter, r *http.Request) {
	d := s.loadDatabase(w, r)
	if d == nil {
		return
	}
	ctx, cancel := reqCtx(r, 2*time.Minute)
	defer cancel()
	if err := databases.Destroy(ctx, s.Store, d, r.URL.Query().Get("volumes") == "1"); err != nil {
		fail(w, err)
		return
	}
	s.Backups.Unschedule(d.ID)
	if err := s.Store.DeleteDatabase(d.ID); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) databaseAction(w http.ResponseWriter, r *http.Request) {
	d := s.loadDatabase(w, r)
	if d == nil {
		return
	}
	switch r.PathValue("action") {
	case "start", "restart":
		t := s.startTask("Start database "+d.Name, 15*time.Minute, func(ctx context.Context, t *task) error {
			return databases.Start(ctx, s.Store, d)
		})
		taskResponse(w, t)
	case "stop":
		ctx, cancel := reqCtx(r, 2*time.Minute)
		defer cancel()
		if err := databases.Stop(ctx, s.Store, d); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case "backup":
		t := s.startTask("Backup "+d.Name, 2*time.Hour, func(ctx context.Context, t *task) error {
			rec := s.Backups.Run(d)
			if rec == nil || rec.Status != "success" {
				msg := "backup failed"
				if rec != nil {
					msg = rec.Message
				}
				return fmt.Errorf("%s", msg)
			}
			fmt.Fprintf(t, "Backup saved: %s (%d bytes)\n", filepath.Base(rec.File), rec.Size)
			return nil
		})
		taskResponse(w, t)
	default:
		writeErr(w, http.StatusNotFound, "unknown action")
	}
}

func (s *Server) databaseLogs(w http.ResponseWriter, r *http.Request) {
	d := s.loadDatabase(w, r)
	if d == nil {
		return
	}
	ctx, cancel := reqCtx(r, 30*time.Second)
	defer cancel()
	logs, err := databases.Logs(ctx, s.Store, d, queryInt(r, "tail", 300))
	resp := map[string]string{"logs": logs}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	d := s.loadDatabase(w, r)
	if d == nil {
		return
	}
	list, err := s.Store.Backups(d.ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) loadBackup(w http.ResponseWriter, r *http.Request) (*store.Backup, *store.Database) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return nil, nil
	}
	b, err := s.Store.Backup(id)
	if err != nil {
		fail(w, err)
		return nil, nil
	}
	d, err := s.Store.Database(b.DatabaseID)
	if err != nil {
		fail(w, err)
		return nil, nil
	}
	return b, d
}

func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	b, d := s.loadBackup(w, r)
	if b == nil {
		return
	}
	t := s.startTask("Restore "+d.Name, 2*time.Hour, func(ctx context.Context, t *task) error {
		fmt.Fprintf(t, "Restoring %s into %s...\n", filepath.Base(b.File), d.Name)
		if err := s.Backups.Restore(d, b); err != nil {
			return err
		}
		fmt.Fprintln(t, "Restore complete.")
		return nil
	})
	taskResponse(w, t)
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	b, _ := s.loadBackup(w, r)
	if b == nil {
		return
	}
	if b.File == "" {
		writeErr(w, http.StatusNotFound, "backup file missing")
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filepath.Base(b.File)))
	http.ServeFile(w, r, b.File)
}

func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request) {
	b, d := s.loadBackup(w, r)
	if b == nil {
		return
	}
	s.Backups.Delete(d, b)
	w.WriteHeader(http.StatusNoContent)
}

// ---- services ----

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, services.Templates)
}

func (s *Server) serviceView(svc *store.Service) map[string]any {
	t, _ := services.FindTemplate(svc.Template)
	notes, _ := s.Services.Render(t, svc, t.Notes)
	creds := map[string]string{}
	json.Unmarshal([]byte(svc.Variables), &creds)
	return map[string]any{"service": svc, "template": t, "notes": notes, "credentials": creds}
}

func (s *Server) listServices(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.Services(int64(queryInt(r, "project", 0)))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) deployServiceTask(svc *store.Service) *task {
	return s.startTask("Deploy "+svc.Name, 30*time.Minute, func(ctx context.Context, t *task) error {
		return s.Services.Deploy(ctx, svc, t)
	})
}

func (s *Server) createService(w http.ResponseWriter, r *http.Request) {
	var svc store.Service
	if err := decode(r, &svc); err != nil {
		fail(w, err)
		return
	}
	t, ok := services.FindTemplate(svc.Template)
	if !ok {
		fail(w, invalid("unknown template"))
		return
	}
	if _, err := s.Store.Project(svc.ProjectID); err != nil {
		fail(w, invalid("project not found"))
		return
	}
	if svc.ServerID == 0 {
		srv, err := s.Store.LocalServer()
		if err != nil {
			fail(w, err)
			return
		}
		svc.ServerID = srv.ID
	}
	if svc.Name = strings.TrimSpace(svc.Name); svc.Name == "" {
		svc.Name = t.Name
	}
	svc.Slug = s.slugFor(svc.Name)
	if svc.Domain == "" {
		svc.Domain = s.defaultDomain(svc.Slug, svc.ServerID)
	} else if d, err := proxy.ParseDomain(svc.Domain); err != nil {
		fail(w, invalid("%v", err))
		return
	} else {
		svc.Domain = d.URL()
	}
	vars, _ := json.Marshal(services.GenerateSecrets(t))
	svc.Variables = string(vars)
	if err := s.Store.CreateService(&svc); err != nil {
		fail(w, err)
		return
	}
	task := s.deployServiceTask(&svc)
	writeJSON(w, http.StatusCreated, map[string]any{"service": svc, "task_id": task.ID})
}

func (s *Server) loadService(w http.ResponseWriter, r *http.Request) *store.Service {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return nil
	}
	svc, err := s.Store.Service(id)
	if err != nil {
		fail(w, err)
		return nil
	}
	return svc
}

func (s *Server) getService(w http.ResponseWriter, r *http.Request) {
	if svc := s.loadService(w, r); svc != nil {
		writeJSON(w, http.StatusOK, s.serviceView(svc))
	}
}

// updateService changes the domain and redeploys.
func (s *Server) updateService(w http.ResponseWriter, r *http.Request) {
	svc := s.loadService(w, r)
	if svc == nil {
		return
	}
	var in struct {
		Domain string `json:"domain"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, err)
		return
	}
	domain := ""
	if strings.TrimSpace(in.Domain) != "" {
		d, err := proxy.ParseDomain(in.Domain)
		if err != nil {
			fail(w, invalid("%v", err))
			return
		}
		domain = d.URL()
	}
	if err := s.Store.UpdateServiceDomain(svc.ID, domain); err != nil {
		fail(w, err)
		return
	}
	svc.Domain = domain
	taskResponse(w, s.deployServiceTask(svc))
}

func (s *Server) deleteService(w http.ResponseWriter, r *http.Request) {
	svc := s.loadService(w, r)
	if svc == nil {
		return
	}
	ctx, cancel := reqCtx(r, 5*time.Minute)
	defer cancel()
	if err := s.Services.Destroy(ctx, svc, r.URL.Query().Get("volumes") == "1"); err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.DeleteService(svc.ID); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) serviceAction(w http.ResponseWriter, r *http.Request) {
	svc := s.loadService(w, r)
	if svc == nil {
		return
	}
	ctx, cancel := reqCtx(r, 3*time.Minute)
	defer cancel()
	var err error
	switch r.PathValue("action") {
	case "deploy":
		taskResponse(w, s.deployServiceTask(svc))
		return
	case "start":
		err = s.Services.Start(ctx, svc)
	case "stop":
		err = s.Services.Stop(ctx, svc)
	default:
		writeErr(w, http.StatusNotFound, "unknown action")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) serviceLogs(w http.ResponseWriter, r *http.Request) {
	svc := s.loadService(w, r)
	if svc == nil {
		return
	}
	ctx, cancel := reqCtx(r, 30*time.Second)
	defer cancel()
	logs, err := s.Services.Logs(ctx, svc, queryInt(r, "tail", 300))
	resp := map[string]string{"logs": logs}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- notifications ----

var notifyKinds = map[string]bool{"discord": true, "slack": true, "telegram": true, "webhook": true, "email": true}

func validateNotification(n *store.Notification) error {
	if !notifyKinds[n.Kind] {
		return invalid("kind must be discord, slack, telegram, webhook or email")
	}
	n.Target = strings.TrimSpace(n.Target)
	if n.Target == "" {
		return invalid("target is required")
	}
	if (n.Kind == "discord" || n.Kind == "slack" || n.Kind == "webhook") && !strings.HasPrefix(n.Target, "https://") &&
		!strings.HasPrefix(n.Target, "http://") {
		return invalid("webhook URL must start with https://")
	}
	if n.Kind == "telegram" && strings.TrimSpace(n.Extra) == "" {
		return invalid("telegram needs a chat id")
	}
	if n.Name = strings.TrimSpace(n.Name); n.Name == "" {
		n.Name = n.Kind
	}
	if n.Events == "" {
		n.Events = "deploy_success,deploy_failed,backup_failed"
	}
	return nil
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.Notifications()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createNotification(w http.ResponseWriter, r *http.Request) {
	var n store.Notification
	if err := decode(r, &n); err != nil {
		fail(w, err)
		return
	}
	if err := validateNotification(&n); err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.CreateNotification(&n); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (s *Server) updateNotification(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	var n store.Notification
	if err := decode(r, &n); err != nil {
		fail(w, err)
		return
	}
	n.ID = id
	if err := validateNotification(&n); err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.UpdateNotification(&n); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) deleteNotification(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.Store.DeleteNotification(id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testNotification(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	list, err := s.Store.Notifications()
	if err != nil {
		fail(w, err)
		return
	}
	for _, n := range list {
		if n.ID == id {
			if err := s.Notify.Deliver(n, "SelfDeploy test", "Notifications are working."); err != nil {
				writeErr(w, http.StatusBadGateway, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "not found")
}
