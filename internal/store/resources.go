package store

import "time"

type Database struct {
	ID         int64     `json:"id"`
	ProjectID  int64     `json:"project_id"`
	ServerID   int64     `json:"server_id"`
	Name       string    `json:"name"`
	Slug       string    `json:"slug"`
	Engine     string    `json:"engine"`
	Version    string    `json:"version"`
	User       string    `json:"db_user"`
	Password   string    `json:"db_password"`
	DBName     string    `json:"db_name"`
	PublicPort int       `json:"public_port"`
	BackupCron string    `json:"backup_cron"`
	BackupKeep int       `json:"backup_keep"`
	BackupS3   bool      `json:"backup_s3"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

type Backup struct {
	ID         int64     `json:"id"`
	DatabaseID int64     `json:"database_id"`
	File       string    `json:"file"`
	Size       int64     `json:"size"`
	Status     string    `json:"status"`
	Message    string    `json:"message"`
	CreatedAt  time.Time `json:"created_at"`
}

type Service struct {
	ID        int64     `json:"id"`
	ProjectID int64     `json:"project_id"`
	ServerID  int64     `json:"server_id"`
	Template  string    `json:"template"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Domain    string    `json:"domain"`
	Variables string    `json:"variables"` // JSON object of generated secrets
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type MailDomain struct {
	ID         int64     `json:"id"`
	Domain     string    `json:"domain"`
	DKIMRecord string    `json:"dkim_record"`
	CreatedAt  time.Time `json:"created_at"`
}

type Mailbox struct {
	ID        int64     `json:"id"`
	DomainID  int64     `json:"domain_id"`
	Address   string    `json:"address"`
	CreatedAt time.Time `json:"created_at"`
}

type MailAlias struct {
	ID          int64  `json:"id"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type Notification struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`   // discord | slack | telegram | webhook | email
	Target  string `json:"target"` // webhook URL, telegram bot token, or email address
	Extra   string `json:"extra"`  // telegram chat id
	Events  string `json:"events"`
	Enabled bool   `json:"enabled"`
}

// ---- databases ----

const dbCols = `id, project_id, server_id, name, slug, engine, version, db_user, db_password, db_name, public_port,
	backup_cron, backup_keep, backup_s3, status, created_at`

func scanDatabase(sc interface{ Scan(...any) error }) (*Database, error) {
	d := &Database{}
	err := sc.Scan(&d.ID, &d.ProjectID, &d.ServerID, &d.Name, &d.Slug, &d.Engine, &d.Version, &d.User,
		&d.Password, &d.DBName, &d.PublicPort, &d.BackupCron, &d.BackupKeep, &d.BackupS3, &d.Status, &d.CreatedAt)
	return d, notFound(err)
}

func (s *Store) CreateDatabase(d *Database) error {
	res, err := s.DB.Exec(`INSERT INTO databases (project_id, server_id, name, slug, engine, version, db_user,
		db_password, db_name, public_port, backup_cron, backup_keep, backup_s3) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ProjectID, d.ServerID, d.Name, d.Slug, d.Engine, d.Version, d.User, d.Password, d.DBName, d.PublicPort,
		d.BackupCron, d.BackupKeep, b2i(d.BackupS3))
	if err != nil {
		return err
	}
	d.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateDatabase(d *Database) error {
	_, err := s.DB.Exec(`UPDATE databases SET name = ?, public_port = ?, backup_cron = ?, backup_keep = ?,
		backup_s3 = ? WHERE id = ?`, d.Name, d.PublicPort, d.BackupCron, d.BackupKeep, b2i(d.BackupS3), d.ID)
	return err
}

func (s *Store) SetDatabaseStatus(id int64, status string) {
	s.DB.Exec(`UPDATE databases SET status = ? WHERE id = ?`, status, id)
}

func (s *Store) Database(id int64) (*Database, error) {
	return scanDatabase(s.DB.QueryRow(`SELECT `+dbCols+` FROM databases WHERE id = ?`, id))
}

func (s *Store) Databases(projectID int64) ([]*Database, error) {
	q := `SELECT ` + dbCols + ` FROM databases`
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
	out := []*Database{}
	for rows.Next() {
		d, err := scanDatabase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) DeleteDatabase(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM databases WHERE id = ?`, id)
	return err
}

func (s *Store) CreateBackup(databaseID int64) (*Backup, error) {
	res, err := s.DB.Exec(`INSERT INTO backups (database_id) VALUES (?)`, databaseID)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Backup{ID: id, DatabaseID: databaseID, Status: "running"}, nil
}

func (s *Store) FinishBackup(b *Backup) {
	s.DB.Exec(`UPDATE backups SET file = ?, size = ?, status = ?, message = ? WHERE id = ?`,
		b.File, b.Size, b.Status, b.Message, b.ID)
}

func (s *Store) Backups(databaseID int64) ([]*Backup, error) {
	rows, err := s.DB.Query(`SELECT id, database_id, file, size, status, message, created_at FROM backups
		WHERE database_id = ? ORDER BY id DESC`, databaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Backup{}
	for rows.Next() {
		b := &Backup{}
		if err := rows.Scan(&b.ID, &b.DatabaseID, &b.File, &b.Size, &b.Status, &b.Message, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) Backup(id int64) (*Backup, error) {
	b := &Backup{}
	err := s.DB.QueryRow(`SELECT id, database_id, file, size, status, message, created_at FROM backups WHERE id = ?`, id).
		Scan(&b.ID, &b.DatabaseID, &b.File, &b.Size, &b.Status, &b.Message, &b.CreatedAt)
	return b, notFound(err)
}

func (s *Store) DeleteBackup(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM backups WHERE id = ?`, id)
	return err
}

// ---- services ----

const svcCols = `id, project_id, server_id, template, name, slug, domain, variables, status, created_at`

func scanService(sc interface{ Scan(...any) error }) (*Service, error) {
	v := &Service{}
	err := sc.Scan(&v.ID, &v.ProjectID, &v.ServerID, &v.Template, &v.Name, &v.Slug, &v.Domain, &v.Variables,
		&v.Status, &v.CreatedAt)
	return v, notFound(err)
}

func (s *Store) CreateService(v *Service) error {
	res, err := s.DB.Exec(`INSERT INTO services (project_id, server_id, template, name, slug, domain, variables)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, v.ProjectID, v.ServerID, v.Template, v.Name, v.Slug, v.Domain, v.Variables)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateServiceDomain(id int64, domain string) error {
	_, err := s.DB.Exec(`UPDATE services SET domain = ? WHERE id = ?`, domain, id)
	return err
}

func (s *Store) SetServiceStatus(id int64, status string) {
	s.DB.Exec(`UPDATE services SET status = ? WHERE id = ?`, status, id)
}

func (s *Store) Service(id int64) (*Service, error) {
	return scanService(s.DB.QueryRow(`SELECT `+svcCols+` FROM services WHERE id = ?`, id))
}

func (s *Store) Services(projectID int64) ([]*Service, error) {
	q := `SELECT ` + svcCols + ` FROM services`
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
	out := []*Service{}
	for rows.Next() {
		v, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) DeleteService(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM services WHERE id = ?`, id)
	return err
}

// ---- mail ----

func (s *Store) CreateMailDomain(domain string) (*MailDomain, error) {
	res, err := s.DB.Exec(`INSERT INTO mail_domains (domain) VALUES (?)`, domain)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.MailDomain(id)
}

func (s *Store) SetMailDomainDKIM(id int64, record string) {
	s.DB.Exec(`UPDATE mail_domains SET dkim_record = ? WHERE id = ?`, record, id)
}

func (s *Store) MailDomain(id int64) (*MailDomain, error) {
	d := &MailDomain{}
	err := s.DB.QueryRow(`SELECT id, domain, dkim_record, created_at FROM mail_domains WHERE id = ?`, id).
		Scan(&d.ID, &d.Domain, &d.DKIMRecord, &d.CreatedAt)
	return d, notFound(err)
}

func (s *Store) MailDomains() ([]*MailDomain, error) {
	rows, err := s.DB.Query(`SELECT id, domain, dkim_record, created_at FROM mail_domains ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MailDomain{}
	for rows.Next() {
		d := &MailDomain{}
		if err := rows.Scan(&d.ID, &d.Domain, &d.DKIMRecord, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) DeleteMailDomain(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM mail_domains WHERE id = ?`, id)
	return err
}

func (s *Store) CreateMailbox(domainID int64, address string) error {
	_, err := s.DB.Exec(`INSERT INTO mailboxes (domain_id, address) VALUES (?, ?)`, domainID, address)
	return err
}

func (s *Store) Mailboxes() ([]*Mailbox, error) {
	rows, err := s.DB.Query(`SELECT id, domain_id, address, created_at FROM mailboxes ORDER BY address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Mailbox{}
	for rows.Next() {
		m := &Mailbox{}
		if err := rows.Scan(&m.ID, &m.DomainID, &m.Address, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Mailbox(id int64) (*Mailbox, error) {
	m := &Mailbox{}
	err := s.DB.QueryRow(`SELECT id, domain_id, address, created_at FROM mailboxes WHERE id = ?`, id).
		Scan(&m.ID, &m.DomainID, &m.Address, &m.CreatedAt)
	return m, notFound(err)
}

func (s *Store) DeleteMailbox(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM mailboxes WHERE id = ?`, id)
	return err
}

func (s *Store) CreateMailAlias(source, dest string) error {
	_, err := s.DB.Exec(`INSERT INTO mail_aliases (source, destination) VALUES (?, ?)`, source, dest)
	return err
}

func (s *Store) MailAliases() ([]*MailAlias, error) {
	rows, err := s.DB.Query(`SELECT id, source, destination FROM mail_aliases ORDER BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MailAlias{}
	for rows.Next() {
		a := &MailAlias{}
		if err := rows.Scan(&a.ID, &a.Source, &a.Destination); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) MailAlias(id int64) (*MailAlias, error) {
	a := &MailAlias{}
	err := s.DB.QueryRow(`SELECT id, source, destination FROM mail_aliases WHERE id = ?`, id).
		Scan(&a.ID, &a.Source, &a.Destination)
	return a, notFound(err)
}

func (s *Store) DeleteMailAlias(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM mail_aliases WHERE id = ?`, id)
	return err
}

// ---- notifications ----

func (s *Store) Notifications() ([]*Notification, error) {
	rows, err := s.DB.Query(`SELECT id, name, kind, target, extra, events, enabled FROM notifications ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Notification{}
	for rows.Next() {
		n := &Notification{}
		if err := rows.Scan(&n.ID, &n.Name, &n.Kind, &n.Target, &n.Extra, &n.Events, &n.Enabled); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) CreateNotification(n *Notification) error {
	res, err := s.DB.Exec(`INSERT INTO notifications (name, kind, target, extra, events, enabled) VALUES (?, ?, ?, ?, ?, ?)`,
		n.Name, n.Kind, n.Target, n.Extra, n.Events, b2i(n.Enabled))
	if err != nil {
		return err
	}
	n.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateNotification(n *Notification) error {
	_, err := s.DB.Exec(`UPDATE notifications SET name = ?, kind = ?, target = ?, extra = ?, events = ?, enabled = ?
		WHERE id = ?`, n.Name, n.Kind, n.Target, n.Extra, n.Events, b2i(n.Enabled), n.ID)
	return err
}

func (s *Store) DeleteNotification(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM notifications WHERE id = ?`, id)
	return err
}
