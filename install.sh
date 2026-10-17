#!/usr/bin/env bash
# SelfDeploy installer: curl -fsSL https://raw.githubusercontent.com/99proteam/selfdeploy/main/install.sh | sudo bash
# Re-run it at any time to upgrade to the latest version. Your data is kept.
set -euo pipefail

IMAGE="${SELFDEPLOY_IMAGE:-ghcr.io/99proteam/selfdeploy:latest}"
DATA_DIR="/data/selfdeploy"
PORT="${SELFDEPLOY_PORT:-8000}"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "please run as root (sudo bash install.sh)"
[ "$(uname -s)" = "Linux" ] || die "SelfDeploy needs a Linux server"

if ! command -v docker >/dev/null 2>&1; then
  info "Installing Docker..."
  curl -fsSL https://get.docker.com | sh
fi
systemctl enable --now docker >/dev/null 2>&1 || true
docker info >/dev/null 2>&1 || die "Docker is installed but not running"

for p in 80 443; do
  if ss -ltnH "sport = :$p" 2>/dev/null | grep -q . && ! docker ps --format '{{.Names}}' | grep -qx selfdeploy-proxy; then
    echo "warning: port $p is already in use (nginx/apache?). Stop it so SelfDeploy's proxy can serve your domains."
  fi
done

info "Preparing $DATA_DIR..."
mkdir -p "$DATA_DIR/proxy"
chmod 700 "$DATA_DIR"
docker network inspect selfdeploy >/dev/null 2>&1 || docker network create selfdeploy >/dev/null

info "Pulling $IMAGE..."
docker pull "$IMAGE"

info "Starting SelfDeploy..."
docker rm -f selfdeploy >/dev/null 2>&1 || true
# The data directory is mounted at the same path as on the host so that
# compose files and the proxy configuration resolve to identical paths.
docker run -d --name selfdeploy --restart unless-stopped \
  --network selfdeploy \
  -p "$PORT:8000" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$DATA_DIR:$DATA_DIR" \
  -e SELFDEPLOY_DATA="$DATA_DIR" \
  ${SELFDEPLOY_PUBLIC_IP:+-e SELFDEPLOY_PUBLIC_IP="$SELFDEPLOY_PUBLIC_IP"} \
  "$IMAGE" >/dev/null

IP="${SELFDEPLOY_PUBLIC_IP:-$(curl -fsS --max-time 5 https://api.ipify.org || hostname -I | awk '{print $1}')}"
cat <<EOF

  SelfDeploy is running!

  Open http://$IP:$PORT and create your admin account.

  Next steps:
   - Open ports 80 and 443 (apps, HTTPS) and 25/465/587/993 (mail) in your firewall.
   - Settings -> set a dashboard domain to get HTTPS for the dashboard itself.

EOF
