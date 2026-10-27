package store

import (
	"strings"
	"time"
)

type Server struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	User        string    `json:"user"`
	IsLocal     bool      `json:"is_local"`
	PublicIP    string    `json:"public_ip"`
	ProxyStatus string    `json:"proxy_status"`
	CreatedAt   time.Time `json:"created_at"`
}

type Project struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

type App struct {
	ID             int64     `json:"id"`
	ProjectID      int64     `json:"project_id"`
	ServerID       int64     `json:"server_id"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	SourceType     string    `json:"source_type"` // git | image
	GitURL         string    `json:"git_url"`
	GitBranch      string    `json:"git_branch"`
	GitToken       string    `json:"git_token,omitempty"`
	BuildPack      string    `json:"build_pack"` // auto | dockerfile | nixpacks | static | compose
	BaseDir        string    `json:"base_dir"`
	DockerfilePath string    `json:"dockerfile_path"`
	ComposePath    string    `json:"compose_path"`
	ComposeService string    `json:"compose_service"`
	PublishDir     string    `json:"publish_dir"`
	InstallCommand string    `json:"install_command"`
	BuildCommand   string    `json:"build_command"`
	StartCommand   string    `json:"start_command"`
	Image          string    `json:"image"`
	Port           int       `json:"port"`
	Domains        string    `json:"domains"` // comma separated, optional http:// or https:// prefix
	HealthPath     string    `json:"health_path"`
	MemoryLimit    string    `json:"memory_limit"`
	CPULimit       string    `json:"cpu_limit"`
	AutoDeploy     bool      `json:"auto_deploy"`
	PreviewDeploys bool      `json:"preview_deploys"`
	WebhookSecret  string    `json:"webhook_secret"`
	BasicAuthUser  string    `json:"basic_auth_user"`
	BasicAuthHash  string    `json:"-"` // bcrypt hash, never sent to clients
	Status         string    `json:"status"`
	CurrentImage   string    `json:"current_image"`
	CreatedAt      time.Time `json:"created_at"`
}

// BasicAuth returns the "user:hash" pair for the proxy, or "" when the app
// is not password protected.
func (a *App) BasicAuth() string {
	if a.BasicAuthUser == "" || a.BasicAuthHash == "" {
		return ""
	}
	return a.BasicAuthUser + ":" + a.BasicAuthHash
}

// DomainList returns the configured domains with blanks removed.
func (a *App) DomainList() []string {
	var out []string
	for _, d := range strings.Split(a.Domains, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

type EnvVar struct {
	ID      int64  `json:"id"`
	AppID   int64  `json:"app_id"`
	Key     string `json:"key"`
	Value   string `json:"value"`
	IsBuild bool   `json:"is_build"`
}

type Deployment struct {
	ID            int64      `json:"id"`
	AppID         int64      `json:"app_id"`
	Trigger       string     `json:"trigger"`
	CommitSHA     string     `json:"commit_sha"`
	CommitMessage string     `json:"commit_message"`
	PRNumber      int        `json:"pr_number"`
	GitRef        string     `json:"git_ref"`
	RollbackImage string     `json:"rollback_image"`
	Image         string     `json:"image"`
	Status        string     `json:"status"`
	Logs          string     `json:"logs,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	FinishedAt    *time.Time `json:"finished_at"`
}

// ---- servers ----

const serverCols = `id, name, host, port, user, is_local, public_ip, proxy_status, created_at`

func scanServer(sc interface{ Scan(...any) error }) (*Server, error) {
	v := &Server{}
	err := sc.Scan(&v.ID, &v.Name, &v.Host, &v.Port, &v.User, &v.IsLocal, &v.PublicIP, &v.ProxyStatus, &v.CreatedAt)
	return v, notFound(err)
}

func (s *Store) CreateServer(v *Server) error {
	res, err := s.DB.Exec(`INSERT INTO servers (name, host, port, user, is_local, public_ip) VALUES (?, ?, ?, ?, ?, ?)`,
		v.Name, v.Host, v.Port, v.User, b2i(v.IsLocal), v.PublicIP)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	v.CreatedAt = time.Now().UTC().Truncate(time.Second)
	return nil
}

func (s *Store) UpdateServer(v *Server) error {
	_, err := s.DB.Exec(`UPDATE servers SET name = ?, host = ?, port = ?, user = ?, public_ip = ? WHERE id = ?`,
		v.Name, v.Host, v.Port, v.User, v.PublicIP, v.ID)
	return err
}

func (s *Store) SetServerProxyStatus(id int64, status string) {
	s.DB.Exec(`UPDATE servers SET proxy_status = ? WHERE id = ?`, status, id)
}

func (s *Store) Server(id int64) (*Server, error) {
	return scanServer(s.DB.QueryRow(`SELECT `+serverCols+` FROM servers WHERE id = ?`, id))
}

func (s *Store) LocalServer() (*Server, error) {
	return scanServer(s.DB.QueryRow(`SELECT ` + serverCols + ` FROM servers WHERE is_local = 1 ORDER BY id LIMIT 1`))
}

func (s *Store) Servers() ([]*Server, error) {
	rows, err := s.DB.Query(`SELECT ` + serverCols + ` FROM servers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Server{}
	for rows.Next() {
		v, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) DeleteServer(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM servers WHERE id = ? AND is_local = 0`, id)
	return err
}

// ---- projects ----

func (s *Store) CreateProject(p *Project) error {
	res, err := s.DB.Exec(`INSERT INTO projects (name, description) VALUES (?, ?)`, p.Name, p.Description)
	if err != nil {
		return err
	}
	p.ID, _ = res.LastInsertId()
	p.CreatedAt = time.Now().UTC().Truncate(time.Second)
	return nil
}

func (s *Store) UpdateProject(p *Project) error {
	_, err := s.DB.Exec(`UPDATE projects SET name = ?, description = ? WHERE id = ?`, p.Name, p.Description, p.ID)
	return err
}

func (s *Store) Project(id int64) (*Project, error) {
	p := &Project{}
	err := s.DB.QueryRow(`SELECT id, name, description, created_at FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt)
	return p, notFound(err)
}

func (s *Store) Projects() ([]*Project, error) {
	rows, err := s.DB.Query(`SELECT id, name, description, created_at FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Project{}
	for rows.Next() {
		p := &Project{}
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteProject(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

// ---- apps ----

const appCols = `id, project_id, server_id, name, slug, source_type, git_url, git_branch, git_token, build_pack,
	base_dir, dockerfile_path, compose_path, compose_service, publish_dir, install_command, build_command,
	start_command, image, port, domains, health_path, memory_limit, cpu_limit, auto_deploy, preview_deploys,
	webhook_secret, basic_auth_user, basic_auth_hash, status, current_image, created_at`

func scanApp(sc interface{ Scan(...any) error }) (*App, error) {
	a := &App{}
	err := sc.Scan(&a.ID, &a.ProjectID, &a.ServerID, &a.Name, &a.Slug, &a.SourceType, &a.GitURL, &a.GitBranch,
		&a.GitToken, &a.BuildPack, &a.BaseDir, &a.DockerfilePath, &a.ComposePath, &a.ComposeService, &a.PublishDir,
		&a.InstallCommand, &a.BuildCommand, &a.StartCommand, &a.Image, &a.Port, &a.Domains, &a.HealthPath,
		&a.MemoryLimit, &a.CPULimit, &a.AutoDeploy, &a.PreviewDeploys, &a.WebhookSecret, &a.BasicAuthUser,
		&a.BasicAuthHash, &a.Status,
		&a.CurrentImage, &a.CreatedAt)
	return a, notFound(err)
}

func (s *Store) CreateApp(a *App) error {
	res, err := s.DB.Exec(`INSERT INTO apps (project_id, server_id, name, slug, source_type, git_url, git_branch,
		git_token, build_pack, base_dir, dockerfile_path, compose_path, compose_service, publish_dir,
		install_command, build_command, start_command, image, port, domains, health_path, memory_limit,
		cpu_limit, auto_deploy, preview_deploys, webhook_secret, basic_auth_user, basic_auth_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ProjectID, a.ServerID, a.Name, a.Slug, a.SourceType, a.GitURL, a.GitBranch, a.GitToken, a.BuildPack,
		a.BaseDir, a.DockerfilePath, a.ComposePath, a.ComposeService, a.PublishDir, a.InstallCommand,
		a.BuildCommand, a.StartCommand, a.Image, a.Port, a.Domains, a.HealthPath, a.MemoryLimit, a.CPULimit,
		b2i(a.AutoDeploy), b2i(a.PreviewDeploys), a.WebhookSecret, a.BasicAuthUser, a.BasicAuthHash)
	if err != nil {
		return err
	}
	a.ID, _ = res.LastInsertId()
	a.CreatedAt = time.Now().UTC().Truncate(time.Second)
	return nil
}

// UpdateApp saves the user-editable fields. Slug and webhook secret are fixed at creation.
func (s *Store) UpdateApp(a *App) error {
	_, err := s.DB.Exec(`UPDATE apps SET project_id = ?, server_id = ?, name = ?, source_type = ?, git_url = ?,
		git_branch = ?, git_token = ?, build_pack = ?, base_dir = ?, dockerfile_path = ?, compose_path = ?,
		compose_service = ?, publish_dir = ?, install_command = ?, build_command = ?, start_command = ?, image = ?,
		port = ?, domains = ?, health_path = ?, memory_limit = ?, cpu_limit = ?, auto_deploy = ?,
		preview_deploys = ?, basic_auth_user = ?, basic_auth_hash = ? WHERE id = ?`,
		a.ProjectID, a.ServerID, a.Name, a.SourceType, a.GitURL, a.GitBranch, a.GitToken, a.BuildPack, a.BaseDir,
		a.DockerfilePath, a.ComposePath, a.ComposeService, a.PublishDir, a.InstallCommand, a.BuildCommand,
		a.StartCommand, a.Image, a.Port, a.Domains, a.HealthPath, a.MemoryLimit, a.CPULimit, b2i(a.AutoDeploy),
		b2i(a.PreviewDeploys), a.BasicAuthUser, a.BasicAuthHash, a.ID)
	return err
}

func (s *Store) SetAppStatus(id int64, status string) {
	s.DB.Exec(`UPDATE apps SET status = ? WHERE id = ?`, status, id)
}

func (s *Store) SetAppPort(id int64, port int) {
	s.DB.Exec(`UPDATE apps SET port = ? WHERE id = ?`, port, id)
}

func (s *Store) SetAppImage(id int64, image string) {
	s.DB.Exec(`UPDATE apps SET current_image = ?, status = 'running' WHERE id = ?`, image, id)
}

func (s *Store) App(id int64) (*App, error) {
	return scanApp(s.DB.QueryRow(`SELECT `+appCols+` FROM apps WHERE id = ?`, id))
}

func (s *Store) Apps(projectID int64) ([]*App, error) {
	q := `SELECT ` + appCols + ` FROM apps`
	var args []any
	if projectID > 0 {
		q += ` WHERE project_id = ?`
		args = append(args, projectID)
	}
	rows, err := s.DB.Query(q+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*App{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) DeleteApp(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM apps WHERE id = ?`, id)
	return err
}

func (s *Store) SlugTaken(slug string) bool {
	var n int
	s.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM apps WHERE slug = ?) + (SELECT COUNT(*) FROM databases WHERE slug = ?)
		+ (SELECT COUNT(*) FROM services WHERE slug = ?)`, slug, slug, slug).Scan(&n)
	return n > 0
}

// ---- env vars ----

func (s *Store) EnvVars(appID int64) ([]*EnvVar, error) {
	rows, err := s.DB.Query(`SELECT id, app_id, key, value, is_build FROM env_vars WHERE app_id = ? ORDER BY key`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EnvVar{}
	for rows.Next() {
		e := &EnvVar{}
		if err := rows.Scan(&e.ID, &e.AppID, &e.Key, &e.Value, &e.IsBuild); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReplaceEnvVars swaps an app's whole variable set in one transaction.
func (s *Store) ReplaceEnvVars(appID int64, vars []*EnvVar) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM env_vars WHERE app_id = ?`, appID); err != nil {
		return err
	}
	for _, v := range vars {
		if _, err := tx.Exec(`INSERT INTO env_vars (app_id, key, value, is_build) VALUES (?, ?, ?, ?)`,
			appID, v.Key, v.Value, b2i(v.IsBuild)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- deployments ----

const deployCols = `id, app_id, trigger, commit_sha, commit_message, pr_number, git_ref, rollback_image, image,
	status, logs, created_at, finished_at`

func scanDeployment(sc interface{ Scan(...any) error }) (*Deployment, error) {
	d := &Deployment{}
	err := sc.Scan(&d.ID, &d.AppID, &d.Trigger, &d.CommitSHA, &d.CommitMessage, &d.PRNumber, &d.GitRef,
		&d.RollbackImage, &d.Image, &d.Status, &d.Logs, &d.CreatedAt, &d.FinishedAt)
	return d, notFound(err)
}

func (s *Store) CreateDeployment(d *Deployment) error {
	res, err := s.DB.Exec(`INSERT INTO deployments (app_id, trigger, commit_sha, commit_message, pr_number, git_ref,
		rollback_image) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.AppID, d.Trigger, d.CommitSHA, d.CommitMessage, d.PRNumber, d.GitRef, d.RollbackImage)
	if err != nil {
		return err
	}
	d.ID, _ = res.LastInsertId()
	d.CreatedAt = time.Now().UTC().Truncate(time.Second)
	d.Status = "queued"
	return nil
}

func (s *Store) Deployment(id int64) (*Deployment, error) {
	return scanDeployment(s.DB.QueryRow(`SELECT `+deployCols+` FROM deployments WHERE id = ?`, id))
}

func (s *Store) Deployments(appID int64, limit int) ([]*Deployment, error) {
	rows, err := s.DB.Query(`SELECT `+deployCols+` FROM deployments WHERE app_id = ? ORDER BY id DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Deployment{}
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		d.Logs = ""
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) AppendDeploymentLog(id int64, text string) {
	s.DB.Exec(`UPDATE deployments SET logs = logs || ? WHERE id = ?`, text, id)
}

func (s *Store) SetDeploymentStatus(id int64, status string) {
	s.DB.Exec(`UPDATE deployments SET status = ? WHERE id = ?`, status, id)
}

func (s *Store) SetDeploymentCommit(id int64, sha, message string) {
	s.DB.Exec(`UPDATE deployments SET commit_sha = ?, commit_message = ? WHERE id = ?`, sha, message, id)
}

func (s *Store) FinishDeployment(id int64, status, image string) {
	s.DB.Exec(`UPDATE deployments SET status = ?, image = ?, finished_at = ? WHERE id = ?`, status, image, now(), id)
}

// FailStaleDeployments marks deployments interrupted by a restart as failed.
func (s *Store) FailStaleDeployments() {
	s.DB.Exec(`UPDATE deployments SET status = 'failed', logs = logs || ?, finished_at = ?
		WHERE status IN ('queued', 'running')`, "\nInterrupted by SelfDeploy restart.\n", now())
}
