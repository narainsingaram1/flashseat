#!/usr/bin/env bash
# One-time setup for a fresh Ubuntu 24.04 server. Run as root:
#   curl -fsSL https://raw.githubusercontent.com/narainsingaram1/flashseat/main/scripts/server-setup.sh | bash
# or copy it over and run `bash server-setup.sh`.
set -euo pipefail

REPO=${REPO:-https://github.com/narainsingaram1/flashseat.git}
DIR=${DIR:-/opt/flashseat}

if ! command -v docker >/dev/null; then
  curl -fsSL https://get.docker.com | sh
fi
apt-get install -y git make openssl >/dev/null

# Only SSH and web traffic in.
if command -v ufw >/dev/null; then
  ufw allow OpenSSH && ufw allow 80/tcp && ufw allow 443/tcp && ufw allow 443/udp
  ufw --force enable
fi

[ -d "$DIR/.git" ] || git clone "$REPO" "$DIR"
cd "$DIR"

if [ ! -f .env ]; then
  read -rp "Domain pointing at this server (e.g. flashseat.example.com): " domain </dev/tty
  cat > .env <<ENV
DOMAIN=$domain
POSTGRES_PASSWORD=$(openssl rand -hex 32)
ADMISSION_SECRET=$(openssl rand -hex 32)
ENV
  chmod 600 .env
  echo "Wrote $DIR/.env"
fi

make deploy
echo "Done. Open https://$(grep ^DOMAIN= .env | cut -d= -f2) (first certificate can take ~30 s)."
