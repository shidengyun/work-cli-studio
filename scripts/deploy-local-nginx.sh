#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
DEPLOY_DIR="${DEPLOY_DIR:-/opt/multica}"
NGINX_CONF="${NGINX_CONF:-/opt/homebrew/etc/nginx/servers/multica.conf}"
BACKEND_PORT="${BACKEND_PORT:-18080}"
WEB_PORT="${WEB_PORT:-13000}"
NGINX_PORT="${NGINX_PORT:-18000}"
PUBLIC_ORIGIN="${PUBLIC_ORIGIN:-http://localhost:${NGINX_PORT}}"
ENV_FILE="${ENV_FILE:-${ROOT}/.env}"
BACKEND_LABEL="ai.multica.backend"
WEB_LABEL="ai.multica.web"
BACKEND_PLIST="${HOME}/Library/LaunchAgents/${BACKEND_LABEL}.plist"
WEB_PLIST="${HOME}/Library/LaunchAgents/${WEB_LABEL}.plist"

log() {
  printf '\n==> %s\n' "$1"
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

port_is_busy() {
  nc -z 127.0.0.1 "$1" >/dev/null 2>&1
}

stop_launch_agent() {
  local label="$1"
  local plist="$2"

  if launchctl list 2>/dev/null | grep -q "[[:space:]]${label}$"; then
    launchctl stop "$label" >/dev/null 2>&1 || true
  fi

  if [ -f "$plist" ]; then
    launchctl unload "$plist" >/dev/null 2>&1 || true
  fi
}

write_env_value() {
  local key="$1"
  local value="$2"
  local file="$3"

  if grep -qE "^${key}=" "$file"; then
    perl -0pi -e "s|^${key}=.*$|${key}=${value}|m" "$file"
  else
    printf '\n%s=%s\n' "$key" "$value" >> "$file"
  fi
}

require_cmd pnpm
require_cmd go
require_cmd node
require_cmd nginx
require_cmd nc
require_cmd rsync

if [ -z "${HTTP_PROXY:-}" ] && [ -z "${HTTPS_PROXY:-}" ]; then
  for proxy_port in 7890 7897 7899; do
    if nc -z 127.0.0.1 "$proxy_port" >/dev/null 2>&1; then
      export HTTP_PROXY="http://127.0.0.1:${proxy_port}"
      export HTTPS_PROXY="http://127.0.0.1:${proxy_port}"
      export ALL_PROXY="socks5://127.0.0.1:${proxy_port}"
      echo "Using local proxy on 127.0.0.1:${proxy_port} for dependency/font downloads."
      break
    fi
  done
fi

if [ ! -f "$ENV_FILE" ]; then
  echo "Missing env file: $ENV_FILE" >&2
  echo "Create it from .env.example first." >&2
  exit 1
fi

log "Stop existing Multica launch agents if present"
stop_launch_agent "$BACKEND_LABEL" "$BACKEND_PLIST"
stop_launch_agent "$WEB_LABEL" "$WEB_PLIST"

for pair in "${BACKEND_PORT}:backend" "${WEB_PORT}:web" "${NGINX_PORT}:nginx"; do
  port="${pair%%:*}"
  name="${pair#*:}"
  if port_is_busy "$port"; then
    echo "Port ${port} for ${name} is already in use." >&2
    echo "Override ports, for example:" >&2
    echo "  BACKEND_PORT=18081 WEB_PORT=13001 NGINX_PORT=18001 $0" >&2
    exit 1
  fi
done

log "Update ${ENV_FILE} for local nginx deployment"
cp "$ENV_FILE" "${ENV_FILE}.bak.$(date +%Y%m%d%H%M%S)"
write_env_value "PORT" "$BACKEND_PORT" "$ENV_FILE"
write_env_value "FRONTEND_PORT" "$WEB_PORT" "$ENV_FILE"
write_env_value "FRONTEND_ORIGIN" "$PUBLIC_ORIGIN" "$ENV_FILE"
write_env_value "MULTICA_APP_URL" "$PUBLIC_ORIGIN" "$ENV_FILE"
write_env_value "GOOGLE_REDIRECT_URI" "${PUBLIC_ORIGIN}/auth/callback" "$ENV_FILE"
write_env_value "NEXT_PUBLIC_API_URL" "$PUBLIC_ORIGIN" "$ENV_FILE"
write_env_value "NEXT_PUBLIC_WS_URL" "ws://localhost:${NGINX_PORT}/ws" "$ENV_FILE"
write_env_value "LOCAL_UPLOAD_BASE_URL" "$PUBLIC_ORIGIN" "$ENV_FILE"

# shellcheck disable=SC1090
set -a
. "$ENV_FILE"
set +a

log "Install dependencies"
cd "$ROOT"
pnpm install

log "Build backend binaries"
mkdir -p "$ROOT/dist/server"
cd "$ROOT/server"
go build -ldflags "-s -w" -o "$ROOT/dist/server/multica-server" ./cmd/server
go build -ldflags "-s -w" -o "$ROOT/dist/server/multica-migrate" ./cmd/migrate

log "Run database migrations"
cd "$ROOT"
"$ROOT/dist/server/multica-migrate" up

log "Build frontend standalone"
export STANDALONE=true
export REMOTE_API_URL="http://127.0.0.1:${BACKEND_PORT}"
export NEXT_PUBLIC_API_URL="$PUBLIC_ORIGIN"
export NEXT_PUBLIC_WS_URL="ws://localhost:${NGINX_PORT}/ws"
export NEXT_PUBLIC_APP_VERSION="local"
pnpm --filter @multica/web build

log "Copy artifacts to ${DEPLOY_DIR}"
sudo mkdir -p "$DEPLOY_DIR/backend" "$DEPLOY_DIR/web" "$DEPLOY_DIR/data/uploads"
sudo cp "$ROOT/dist/server/multica-server" "$DEPLOY_DIR/backend/"
sudo cp "$ROOT/dist/server/multica-migrate" "$DEPLOY_DIR/backend/"
sudo cp "$ENV_FILE" "$DEPLOY_DIR/.env"
sudo rsync -a --delete "$ROOT/apps/web/.next/standalone/" "$DEPLOY_DIR/web/"
sudo mkdir -p "$DEPLOY_DIR/web/apps/web/.next/static" "$DEPLOY_DIR/web/apps/web/public"
sudo rsync -a --delete "$ROOT/apps/web/.next/static/" "$DEPLOY_DIR/web/apps/web/.next/static/"
sudo rsync -a --delete "$ROOT/apps/web/public/" "$DEPLOY_DIR/web/apps/web/public/"

log "Write launchd services"
mkdir -p "${HOME}/Library/LaunchAgents"
cat > "$BACKEND_PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
 "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>${BACKEND_LABEL}</string>
    <key>WorkingDirectory</key>
    <string>${DEPLOY_DIR}</string>
    <key>ProgramArguments</key>
    <array>
      <string>/bin/zsh</string>
      <string>-lc</string>
      <string>set -a; . ${DEPLOY_DIR}/.env; set +a; exec ${DEPLOY_DIR}/backend/multica-server</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/multica-backend.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/multica-backend.err.log</string>
  </dict>
</plist>
PLIST

cat > "$WEB_PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
 "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>${WEB_LABEL}</string>
    <key>WorkingDirectory</key>
    <string>${DEPLOY_DIR}/web</string>
    <key>ProgramArguments</key>
    <array>
      <string>/bin/zsh</string>
      <string>-lc</string>
      <string>export NODE_ENV=production PORT=${WEB_PORT} HOSTNAME=127.0.0.1; exec node apps/web/server.js</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/multica-web.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/multica-web.err.log</string>
  </dict>
</plist>
PLIST

launchctl load "$BACKEND_PLIST"
launchctl load "$WEB_PLIST"

log "Write nginx config"
sudo mkdir -p "$(dirname "$NGINX_CONF")"
sudo tee "$NGINX_CONF" >/dev/null <<NGINX
server {
    listen ${NGINX_PORT};
    server_name localhost;

    client_max_body_size 100m;

    location /ws {
        proxy_pass http://127.0.0.1:${BACKEND_PORT}/ws;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_read_timeout 3600;
    }

    location /api/ {
        proxy_pass http://127.0.0.1:${BACKEND_PORT}/api/;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }

    location /auth/ {
        proxy_pass http://127.0.0.1:${BACKEND_PORT}/auth/;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }

    location /uploads/ {
        proxy_pass http://127.0.0.1:${BACKEND_PORT}/uploads/;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }

    location / {
        proxy_pass http://127.0.0.1:${WEB_PORT};
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }
}
NGINX

log "Reload nginx"
sudo nginx -t
if brew services list 2>/dev/null | grep -q '^nginx'; then
  brew services restart nginx
else
  sudo nginx -s reload 2>/dev/null || sudo nginx
fi

log "Smoke test"
for i in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${BACKEND_PORT}/health" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -fsS "http://127.0.0.1:${BACKEND_PORT}/health" >/dev/null
curl -fsSI "http://127.0.0.1:${WEB_PORT}" >/dev/null
curl -fsSI "$PUBLIC_ORIGIN" >/dev/null

cat <<DONE

✓ Multica deployed locally behind nginx.

  Public URL:  ${PUBLIC_ORIGIN}
  Backend:     http://127.0.0.1:${BACKEND_PORT}
  Frontend:    http://127.0.0.1:${WEB_PORT}
  Deploy dir:  ${DEPLOY_DIR}
  Nginx conf:  ${NGINX_CONF}

Logs:
  tail -f /tmp/multica-backend.log /tmp/multica-backend.err.log
  tail -f /tmp/multica-web.log /tmp/multica-web.err.log

Stop services:
  launchctl unload ${BACKEND_PLIST}
  launchctl unload ${WEB_PLIST}

CLI reconfigure:
  multica setup self-host --server-url ${PUBLIC_ORIGIN} --app-url ${PUBLIC_ORIGIN}

DONE
