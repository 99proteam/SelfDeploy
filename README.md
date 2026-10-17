<p align="center">
  <img src="web/static/favicon.svg" width="72" alt="SelfDeploy logo">
</p>

<h1 align="center">SelfDeploy</h1>

<p align="center">
  <b>Open-source, self-hosted PaaS.</b> Git auto-deploy, automatic SSL, custom domains, databases, your own email server and one-click apps, all on your own server.
  <br>
  A self-hosted alternative to Heroku, Vercel, Netlify, Render and Coolify.
</p>

<p align="center">
  <a href="#-quick-start">Quick start</a> ·
  <a href="#-features">Features</a> ·
  <a href="#-deploying-apps">Deploying apps</a> ·
  <a href="#-email-server">Email server</a> ·
  <a href="#-api">API</a>
</p>

---

SelfDeploy turns any Linux VPS into your own Platform-as-a-Service. Push to Git and your app is built, deployed with zero downtime and served over HTTPS. You can also run PostgreSQL, MySQL, MongoDB and Redis with scheduled backups, host email for your domains, and launch WordPress, Ghost, n8n and more in one click. It ships as a single Go binary in one Docker container.

## 🚀 Quick start

On a fresh Ubuntu/Debian server (1 GB RAM minimum, 2 GB+ recommended), run:

```bash
curl -fsSL https://raw.githubusercontent.com/99proteam/selfdeploy/main/install.sh | sudo bash
```

Then open `http://YOUR_SERVER_IP:8000` and create your admin account. Docker is installed for you if it's missing. Re-run the same command to upgrade; your data is kept.

<details>
<summary>Install with Docker Compose instead</summary>

```bash
git clone https://github.com/99proteam/selfdeploy && cd selfdeploy
docker compose up -d
```
</details>

## ✨ Features

| | |
|---|---|
| **Git auto-deploy** | Push to GitHub, GitLab, Gitea/Forgejo or Bitbucket and SelfDeploy deploys automatically via signed webhooks. Public and private repos (access token or SSH deploy key), monorepos (base directory). |
| **Any stack** | Dockerfile, **Nixpacks** (Node.js, Next.js, Python, Django, Go, PHP/Laravel, Ruby/Rails, Rust, Java, .NET, Elixir, Deno, Bun …), static sites and SPAs (Vite, React, Vue, Astro …), Docker Compose, or any Docker image. |
| **Automatic SSL** | Free Let's Encrypt certificates for every domain, issued and renewed automatically, with HTTP→HTTPS redirects. |
| **Temporary domains** | Every app gets a working URL instantly (`app.203.0.113.7.sslip.io`), with no DNS setup. |
| **Custom & wildcard domains** | Multiple domains per app. Set a wildcard domain once and new apps get `name.apps.example.com`. |
| **Zero-downtime deploys** | The new version starts next to the old one and must pass its health check before traffic switches. |
| **One-click rollback** | Every build is kept as an image; roll back to any previous deployment in seconds. |
| **Preview deployments** | Each pull/merge request gets its own URL (`pr12.app.example.com`), removed automatically when it closes. |
| **Databases** | PostgreSQL, MySQL, MariaDB, MongoDB and Redis in one click, with generated credentials and optional public access. |
| **Backups** | Scheduled (cron) and on-demand database backups, retention, one-click restore, download, and upload to any S3-compatible storage (AWS S3, Cloudflare R2, Backblaze B2, Wasabi …). |
| **Email server** | Your own mail server for custom domains: SMTP, IMAP, DKIM, SPF, DMARC, spam filtering and fail2ban, with the exact DNS records shown for you. Apps can send mail without credentials. |
| **One-click services** | WordPress, Ghost, n8n, Uptime Kuma, Gitea, Vaultwarden, RustFS (S3), Umami, Nextcloud, Roundcube webmail, Grafana, Adminer. |
| **Multiple servers** | Manage remote servers over SSH from one dashboard. |
| **Environment variables** | Runtime and build-time variables, with `.env` import. |
| **Logs & monitoring** | Live build logs, runtime logs, CPU/memory/disk and per-container stats. |
| **Notifications** | Discord, Slack, Telegram, email or any webhook, for deploy and backup events. |
| **Teams & API** | Multiple users, admin roles, API tokens and a REST API for CI/CD. |
| **Lightweight** | One Go binary with an embedded dashboard and SQLite. SelfDeploy itself uses about 20 MB of RAM. |

## 📦 Deploying apps

1. **Create a project**, then **+ App**.
2. Paste your repository URL (for private repos, add an access token) or a Docker image name.
3. Pick a build pack. *Auto-detect* chooses: `Dockerfile` → Docker Compose → static `index.html` → Nixpacks.
4. Set the port your app listens on. SelfDeploy also sets `PORT` and `HOST=0.0.0.0` for you.
5. Click **Create**. Your app is live at a temporary `sslip.io` URL within a minute or two.

### Custom domains and HTTPS

Point an `A` record at your server's IP, then add the domain to the app (e.g. `https://app.example.com`). The certificate is issued automatically on the next deploy. You can list several domains, separated by commas. Prefix a domain with `http://` to serve it without TLS.

To give every new app a domain automatically, create a wildcard record `*.apps.example.com → your IP` and set **Settings → Wildcard domain** to `apps.example.com`.

To serve the dashboard over HTTPS, set **Settings → Dashboard domain**.

### Auto deploy on push

Each app has a **Git & webhooks** tab with ready-to-paste webhook URLs and a secret:

| Provider | Webhook URL | Secret |
|---|---|---|
| GitHub | `/webhooks/github/<app-id>` | Webhook secret (HMAC SHA-256 verified) |
| GitLab | `/webhooks/gitlab/<app-id>` | "Secret token" |
| Gitea / Forgejo | `/webhooks/gitea/<app-id>` | Webhook secret (HMAC verified) |
| Bitbucket | `/webhooks/bitbucket/<app-id>?secret=…` | in the URL |
| Any CI | `/webhooks/deploy/<app-id>?secret=…` | in the URL |

Enable **preview deployments** for an app to get a URL for each pull request.

### Health checks and rollbacks

Set a health check path (e.g. `/health`) and a new version only goes live once it returns 2xx. If it fails, the previous version keeps serving and you get notified. Roll back from the **Deployments** tab.

## 🗄️ Databases & backups

Create a database from a project. Apps on the same server connect with the internal URL shown on the database page (e.g. `postgres://selfdeploy:…@main-db:5432/app`). Add it to your app's environment as `DATABASE_URL`.

Backups run on a cron schedule (`0 3 * * *` = daily at 03:00 UTC), keep the last *N* copies, and can be restored or downloaded from the dashboard. Configure **Settings → Backup storage** and tick *Upload to S3* to keep copies off-server. The bucket is created for you if it doesn't exist.

## ✉️ Email server

1. Create DNS record `A mail.example.com → your server IP`, and set the reverse DNS (PTR) for the IP to `mail.example.com` at your VPS provider.
2. Go to **Mail server**, enter `mail.example.com` and install. A TLS certificate is issued automatically.
3. Add your domain, create a mailbox, then click **Generate DKIM key**.
4. Add the DNS records SelfDeploy shows: MX, SPF, DKIM and DMARC.

Use any mail client (IMAP `993`, SMTP `587`/`465`) or deploy **Roundcube** webmail in one click. Apps send mail through `smtp://selfdeploy-mail:25` with no credentials needed.

> Many cloud providers block outbound port 25 by default. Ask them to unblock it, or relay through an SMTP provider.

## 🖥️ Multiple servers

Under **Servers**, copy SelfDeploy's SSH public key into `~/.ssh/authorized_keys` on the remote server (Docker must be installed there), then add the server by IP. Apps, databases and services can then be deployed to it. Builds, logs and backups work the same as locally.

## 🔌 API

Create a token under **Settings → API tokens**, then:

```bash
# Deploy an app (e.g. from GitHub Actions)
curl -X POST -H "Authorization: Bearer $SELFDEPLOY_TOKEN" https://deploy.example.com/api/apps/1/deploy

# List apps
curl -H "Authorization: Bearer $SELFDEPLOY_TOKEN" https://deploy.example.com/api/apps
```

Main endpoints: `/api/projects`, `/api/apps`, `/api/apps/{id}/deploy|stop|start|restart|logs|env|deployments`, `/api/deployments/{id}/rollback`, `/api/databases`, `/api/databases/{id}/backup`, `/api/services`, `/api/templates`, `/api/mail`, `/api/servers`, `/api/notifications`.

## ⚙️ Configuration

| Variable | Default | Description |
|---|---|---|
| `SELFDEPLOY_DATA` | `/data/selfdeploy` | Database, builds, backups and SSH keys |
| `SELFDEPLOY_ADDR` | `:8000` | Dashboard listen address |
| `SELFDEPLOY_PUBLIC_IP` | auto-detected | Public IP used for sslip.io domains and DNS instructions |
| `SELFDEPLOY_BUILD_WORKERS` | `2` | Parallel builds |

Forgot your password? Run `docker exec selfdeploy selfdeploy reset-password you@example.com`.

## 🏗️ How it works

```
             ┌──────────────── your server ─────────────────┐
 git push ──►│ SelfDeploy (Go, SQLite, dashboard)            │
             │   ├─ git clone → docker build / nixpacks      │
             │   ├─ docker run (new) → health check → swap   │
             │   └─ docker CLI (local or over SSH)           │
 browser ───►│ Traefik :80/:443 ── Let's Encrypt ──► apps    │
 mail ──────►│ docker-mailserver :25/:587/:993               │
             └───────────────────────────────────────────────┘
```

SelfDeploy drives Docker through the Docker CLI, so remote servers only need Docker and SSH. Traefik routes domains to containers using labels and handles certificates. All state lives in one SQLite file in `/data/selfdeploy`.

## 🧑‍💻 Development

```bash
go test ./...
go run ./cmd/selfdeploy   # needs Docker; dashboard on http://localhost:8000
```

The dashboard is plain HTML/CSS/JS in `web/static`, embedded into the binary. There's no Node build step.

## 🗺️ Roadmap

- Web terminal for containers
- Scheduled tasks (cron jobs) for apps
- GitHub App integration (repo picker, commit statuses)
- Docker Swarm / multi-node scaling
- Restore backups from S3

Contributions are welcome. Open an issue or a pull request.

## 📄 License

[MIT](LICENSE)

---

<sub>Keywords: self-hosted PaaS, open source Heroku alternative, Vercel alternative, Netlify alternative, Coolify alternative, Dokku alternative, git push deploy, auto deploy from GitHub, docker deployment platform, self-hosted email server, SMTP server custom domain, free SSL Let's Encrypt, VPS control panel.</sub>
