package services

// Template is a one-click service. The compose file uses {{PLACEHOLDERS}}:
// URL, HOST, SCHEME, NAME, MAIL_HOST, plus any PASSWORD* / SECRET_KEY*
// token, which is generated once per service and shown as a credential.
// Routing labels are added for Main:Port by an override file, which also
// makes the main service reachable as NAME on the selfdeploy network, so
// templates stay plain compose files and must not set container_name.
type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Main        string `json:"-"`
	Port        int    `json:"-"`
	Compose     string `json:"-"`
	Notes       string `json:"notes,omitempty"`
}

var Templates = []Template{
	{ID: "wordpress", Name: "WordPress", Category: "CMS", Description: "The world's most popular website builder and blog.",
		Main: "wordpress", Port: 80, Compose: `services:
  wordpress:
    image: wordpress:6-apache
    restart: unless-stopped
    environment:
      WORDPRESS_DB_HOST: db
      WORDPRESS_DB_USER: wordpress
      WORDPRESS_DB_PASSWORD: "{{PASSWORD_DB}}"
      WORDPRESS_DB_NAME: wordpress
    volumes: [wp-data:/var/www/html]
    depends_on: [db]
  db:
    image: mariadb:11
    restart: unless-stopped
    environment:
      MARIADB_DATABASE: wordpress
      MARIADB_USER: wordpress
      MARIADB_PASSWORD: "{{PASSWORD_DB}}"
      MARIADB_RANDOM_ROOT_PASSWORD: "1"
    volumes: [db-data:/var/lib/mysql]
volumes: {wp-data: {}, db-data: {}}
`},
	{ID: "ghost", Name: "Ghost", Category: "CMS", Description: "Modern publishing platform for blogs and newsletters.",
		Main: "ghost", Port: 2368, Notes: "Open {{URL}}/ghost to create the admin account.", Compose: `services:
  ghost:
    image: ghost:5-alpine
    restart: unless-stopped
    environment:
      url: "{{URL}}"
      database__client: mysql
      database__connection__host: db
      database__connection__user: root
      database__connection__password: "{{PASSWORD_DB}}"
      database__connection__database: ghost
    volumes: [ghost-content:/var/lib/ghost/content]
    depends_on: [db]
  db:
    image: mysql:8.4
    restart: unless-stopped
    environment:
      MYSQL_ROOT_PASSWORD: "{{PASSWORD_DB}}"
      MYSQL_DATABASE: ghost
    volumes: [db-data:/var/lib/mysql]
volumes: {ghost-content: {}, db-data: {}}
`},
	{ID: "n8n", Name: "n8n", Category: "Automation", Description: "Workflow automation with 400+ integrations.",
		Main: "n8n", Port: 5678, Compose: `services:
  n8n:
    image: n8nio/n8n:latest
    restart: unless-stopped
    environment:
      N8N_HOST: "{{HOST}}"
      N8N_PROTOCOL: "{{SCHEME}}"
      N8N_SECURE_COOKIE: "{{SECURE}}"
      WEBHOOK_URL: "{{URL}}/"
      N8N_ENCRYPTION_KEY: "{{SECRET_KEY}}"
      GENERIC_TIMEZONE: UTC
    volumes: [n8n-data:/home/node/.n8n]
volumes: {n8n-data: {}}
`},
	{ID: "uptime-kuma", Name: "Uptime Kuma", Category: "Monitoring", Description: "Self-hosted uptime monitoring with status pages.",
		Main: "uptime-kuma", Port: 3001, Compose: `services:
  uptime-kuma:
    image: louislam/uptime-kuma:1
    restart: unless-stopped
    volumes: [kuma-data:/app/data]
volumes: {kuma-data: {}}
`},
	{ID: "gitea", Name: "Gitea", Category: "Development", Description: "Lightweight self-hosted Git service, a GitHub alternative.",
		Main: "gitea", Port: 3000, Compose: `services:
  gitea:
    image: gitea/gitea:1.22
    restart: unless-stopped
    environment:
      USER_UID: "1000"
      USER_GID: "1000"
      GITEA__server__ROOT_URL: "{{URL}}/"
      GITEA__server__DOMAIN: "{{HOST}}"
      GITEA__server__SSH_DOMAIN: "{{HOST}}"
    volumes: [gitea-data:/data]
volumes: {gitea-data: {}}
`},
	{ID: "vaultwarden", Name: "Vaultwarden", Category: "Security", Description: "Bitwarden-compatible password manager.",
		Main: "vaultwarden", Port: 80, Notes: "Use an HTTPS domain: Bitwarden clients require it. Admin panel: {{URL}}/admin",
		Compose: `services:
  vaultwarden:
    image: vaultwarden/server:latest
    restart: unless-stopped
    environment:
      DOMAIN: "{{URL}}"
      SIGNUPS_ALLOWED: "true"
      ADMIN_TOKEN: "{{PASSWORD_ADMIN}}"
    volumes: [vw-data:/data]
volumes: {vw-data: {}}
`},
	{ID: "rustfs", Name: "RustFS (S3 storage)", Category: "Storage", Description: "S3-compatible object storage, a MinIO alternative, with a web console.",
		Main: "rustfs", Port: 9001, Notes: "Console: {{URL}}/rustfs/console/ (login admin / PASSWORD). Apps reach the S3 API at http://{{NAME}}:9000 on the selfdeploy network.",
		Compose: `services:
  rustfs:
    image: rustfs/rustfs:latest
    restart: unless-stopped
    environment:
      RUSTFS_ACCESS_KEY: admin
      RUSTFS_SECRET_KEY: "{{PASSWORD}}"
      RUSTFS_CONSOLE_ENABLE: "true"
    volumes: [rustfs-data:/data]
volumes: {rustfs-data: {}}
`},
	{ID: "umami", Name: "Umami", Category: "Analytics", Description: "Privacy-friendly Google Analytics alternative.",
		Main: "umami", Port: 3000, Notes: "Default login: admin / umami. Change it right away.", Compose: `services:
  umami:
    image: ghcr.io/umami-software/umami:postgresql-latest
    restart: unless-stopped
    environment:
      DATABASE_URL: "postgresql://umami:{{PASSWORD_DB}}@db:5432/umami"
      APP_SECRET: "{{SECRET_KEY}}"
    depends_on: [db]
  db:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB: umami
      POSTGRES_USER: umami
      POSTGRES_PASSWORD: "{{PASSWORD_DB}}"
    volumes: [db-data:/var/lib/postgresql/data]
volumes: {db-data: {}}
`},
	{ID: "nextcloud", Name: "Nextcloud", Category: "Productivity", Description: "Files, calendar, contacts and office in your own cloud.",
		Main: "nextcloud", Port: 80, Notes: "Admin login: admin / PASSWORD_ADMIN.", Compose: `services:
  nextcloud:
    image: nextcloud:apache
    restart: unless-stopped
    environment:
      MYSQL_HOST: db
      MYSQL_DATABASE: nextcloud
      MYSQL_USER: nextcloud
      MYSQL_PASSWORD: "{{PASSWORD_DB}}"
      NEXTCLOUD_ADMIN_USER: admin
      NEXTCLOUD_ADMIN_PASSWORD: "{{PASSWORD_ADMIN}}"
      NEXTCLOUD_TRUSTED_DOMAINS: "{{HOST}}"
      OVERWRITEPROTOCOL: "{{SCHEME}}"
      TRUSTED_PROXIES: "172.16.0.0/12 10.0.0.0/8 192.168.0.0/16"
    volumes: [nc-data:/var/www/html]
    depends_on: [db]
  db:
    image: mariadb:11
    restart: unless-stopped
    command: --transaction-isolation=READ-COMMITTED --log-bin=binlog --binlog-format=ROW
    environment:
      MARIADB_DATABASE: nextcloud
      MARIADB_USER: nextcloud
      MARIADB_PASSWORD: "{{PASSWORD_DB}}"
      MARIADB_RANDOM_ROOT_PASSWORD: "1"
    volumes: [db-data:/var/lib/mysql]
volumes: {nc-data: {}, db-data: {}}
`},
	{ID: "roundcube", Name: "Roundcube Webmail", Category: "Email", Description: "Webmail for the built-in mail server.",
		Main: "roundcube", Port: 80, Notes: "Enable the mail server first. Log in with a mailbox address and password.",
		Compose: `services:
  roundcube:
    image: roundcube/roundcubemail:latest
    restart: unless-stopped
    environment:
      ROUNDCUBEMAIL_DEFAULT_HOST: "ssl://{{MAIL_HOST}}"
      ROUNDCUBEMAIL_DEFAULT_PORT: "993"
      ROUNDCUBEMAIL_SMTP_SERVER: "tls://{{MAIL_HOST}}"
      ROUNDCUBEMAIL_SMTP_PORT: "587"
      ROUNDCUBEMAIL_DB_TYPE: sqlite
      ROUNDCUBEMAIL_SKIN: elastic
    volumes: [rc-db:/var/roundcube/db]
volumes: {rc-db: {}}
`},
	{ID: "grafana", Name: "Grafana", Category: "Monitoring", Description: "Dashboards and alerting for your metrics.",
		Main: "grafana", Port: 3000, Notes: "Login: admin / PASSWORD.", Compose: `services:
  grafana:
    image: grafana/grafana-oss:latest
    restart: unless-stopped
    environment:
      GF_SERVER_ROOT_URL: "{{URL}}"
      GF_SECURITY_ADMIN_PASSWORD: "{{PASSWORD}}"
    volumes: [grafana-data:/var/lib/grafana]
volumes: {grafana-data: {}}
`},
	{ID: "adminer", Name: "Adminer", Category: "Database", Description: "Web UI for your databases (use the internal host names).",
		Main: "adminer", Port: 8080, Compose: `services:
  adminer:
    image: adminer:latest
    restart: unless-stopped
    command: ["php", "-S", "0.0.0.0:8080", "-t", "/var/www/html"]
`},
}

func FindTemplate(id string) (Template, bool) {
	for _, t := range Templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}
