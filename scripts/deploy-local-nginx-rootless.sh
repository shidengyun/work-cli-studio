#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
DEPLOY_DIR="${DEPLOY_DIR:-${HOME}/.multica/local-deploy}"
NGINX_PREFIX="${NGINX_PREFIX:-${HOME}/.multica/nginx}"
BACKEND_PORT="${BACKEND_PORT:-18080}"
WEB_PORT="${WEB_PORT:-13000}"
NGINX_PORT="${NGINX_PORT:-18000}"
NGINX_LISTEN_HOST="${NGINX_LISTEN_HOST:-0.0.0.0}"
PUBLIC_HOST="${PUBLIC_HOST:-}"
PUBLIC_ORIGIN="${PUBLIC_ORIGIN:-}"
ENV_FILE="${ENV_FILE:-${ROOT}/.env}"
BACKEND_LABEL="ai.multica.backend"
WEB_LABEL="ai.multica.web"
NGINX_LABEL="ai.multica.nginx"
BACKEND_PLIST="${HOME}/Library/LaunchAgents/${BACKEND_LABEL}.plist"
WEB_PLIST="${HOME}/Library/LaunchAgents/${WEB_LABEL}.plist"
NGINX_PLIST="${HOME}/Library/LaunchAgents/${NGINX_LABEL}.plist"
NGINX_BIN="${NGINX_BIN:-$(command -v nginx || true)}"
MIME_TYPES="${MIME_TYPES:-/opt/homebrew/etc/nginx/mime.types}"

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
require_cmd curl

append_no_proxy_hosts() {
  local current="${NO_PROXY:-${no_proxy:-}}"
  local host

  current="${current%,}"
  for host in "$@"; do
    case ",${current}," in
      *",${host},"*) ;;
      *) current="${current:+${current},}${host}" ;;
    esac
  done

  export NO_PROXY="$current"
  export no_proxy="$current"
}

append_no_proxy_hosts localhost 127.0.0.1 ::1

detect_lan_ip() {
  local ip

  ip="$(ipconfig getifaddr en0 2>/dev/null || true)"
  if [ -z "$ip" ]; then
    ip="$(ipconfig getifaddr en1 2>/dev/null || true)"
  fi
  if [ -z "$ip" ]; then
    ip="$(route -n get default 2>/dev/null | awk '/interface:/{print $2; exit}' | xargs -I{} ipconfig getifaddr {} 2>/dev/null || true)"
  fi
  if [ -z "$ip" ]; then
    ip="localhost"
  fi

  printf '%s\n' "$ip"
}

if [ -z "$PUBLIC_HOST" ]; then
  PUBLIC_HOST="$(detect_lan_ip)"
fi

if [ -z "$PUBLIC_ORIGIN" ]; then
  PUBLIC_ORIGIN="http://${PUBLIC_HOST}:${NGINX_PORT}"
fi

if [ "$PUBLIC_HOST" != "localhost" ] && [ "$PUBLIC_HOST" != "127.0.0.1" ]; then
  append_no_proxy_hosts "$PUBLIC_HOST"
fi

if [ -z "$NGINX_BIN" ]; then
  echo "nginx not found in PATH" >&2
  exit 1
fi

if [ ! -f "$MIME_TYPES" ]; then
  echo "nginx mime.types not found: $MIME_TYPES" >&2
  echo "Override it with MIME_TYPES=/path/to/mime.types $0" >&2
  exit 1
fi

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

log "Stop existing user launch agents"
stop_launch_agent "$NGINX_LABEL" "$NGINX_PLIST"
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

log "Update ${ENV_FILE} for rootless local nginx deployment"
cp "$ENV_FILE" "${ENV_FILE}.bak.$(date +%Y%m%d%H%M%S)"
write_env_value "PORT" "$BACKEND_PORT" "$ENV_FILE"
write_env_value "FRONTEND_PORT" "$WEB_PORT" "$ENV_FILE"
write_env_value "FRONTEND_ORIGIN" "$PUBLIC_ORIGIN" "$ENV_FILE"
write_env_value "MULTICA_APP_URL" "$PUBLIC_ORIGIN" "$ENV_FILE"
write_env_value "GOOGLE_REDIRECT_URI" "${PUBLIC_ORIGIN}/auth/callback" "$ENV_FILE"
write_env_value "NEXT_PUBLIC_API_URL" "" "$ENV_FILE"
write_env_value "NEXT_PUBLIC_WS_URL" "" "$ENV_FILE"
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
export NEXT_PUBLIC_API_URL=""
export NEXT_PUBLIC_WS_URL=""
export NEXT_PUBLIC_APP_VERSION="local"
pnpm --filter @multica/web build

log "Copy artifacts to ${DEPLOY_DIR}"
mkdir -p "$DEPLOY_DIR/backend" "$DEPLOY_DIR/web" "$DEPLOY_DIR/data/uploads"
cp "$ROOT/dist/server/multica-server" "$DEPLOY_DIR/backend/"
cp "$ROOT/dist/server/multica-migrate" "$DEPLOY_DIR/backend/"
cp "$ENV_FILE" "$DEPLOY_DIR/.env"
rsync -a --delete "$ROOT/apps/web/.next/standalone/" "$DEPLOY_DIR/web/"
mkdir -p "$DEPLOY_DIR/web/apps/web/.next/static" "$DEPLOY_DIR/web/apps/web/public"
rsync -a --delete "$ROOT/apps/web/.next/static/" "$DEPLOY_DIR/web/apps/web/.next/static/"
rsync -a --delete "$ROOT/apps/web/public/" "$DEPLOY_DIR/web/apps/web/public/"

log "Write user nginx config"
mkdir -p \
  "$NGINX_PREFIX/conf" \
  "$NGINX_PREFIX/logs" \
  "$NGINX_PREFIX/client_body_temp" \
  "$NGINX_PREFIX/proxy_temp" \
  "$NGINX_PREFIX/fastcgi_temp" \
  "$NGINX_PREFIX/uwsgi_temp" \
  "$NGINX_PREFIX/scgi_temp"

cat > "$NGINX_PREFIX/conf/nginx.conf" <<NGINX
worker_processes 1;
error_log logs/error.log;
pid logs/nginx.pid;

events {
    worker_connections 1024;
}

http {
    include ${MIME_TYPES};
    default_type application/octet-stream;
    access_log logs/access.log;
    sendfile on;
    keepalive_timeout 65;
    client_body_temp_path ${NGINX_PREFIX}/client_body_temp;
    proxy_temp_path ${NGINX_PREFIX}/proxy_temp;
    fastcgi_temp_path ${NGINX_PREFIX}/fastcgi_temp;
    uwsgi_temp_path ${NGINX_PREFIX}/uwsgi_temp;
    scgi_temp_path ${NGINX_PREFIX}/scgi_temp;

    map \$http_upgrade \$connection_upgrade {
        default upgrade;
        '' close;
    }

    server {
        listen ${NGINX_LISTEN_HOST}:${NGINX_PORT};
        server_name _;

        client_max_body_size 100m;

        location /ws {
            proxy_pass http://127.0.0.1:${BACKEND_PORT}/ws;
            proxy_http_version 1.1;
            proxy_set_header Upgrade \$http_upgrade;
            proxy_set_header Connection \$connection_upgrade;
            proxy_set_header Host \$http_host;
            proxy_set_header X-Forwarded-Host \$http_host;
            proxy_set_header X-Real-IP \$remote_addr;
            proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto \$scheme;
            proxy_read_timeout 3600;
        }

        location /api/ {
            proxy_pass http://127.0.0.1:${BACKEND_PORT}/api/;
            proxy_set_header Host \$http_host;
            proxy_set_header X-Forwarded-Host \$http_host;
            proxy_set_header X-Real-IP \$remote_addr;
            proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto \$scheme;
        }

        location /auth/ {
            proxy_pass http://127.0.0.1:${BACKEND_PORT}/auth/;
            proxy_set_header Host \$http_host;
            proxy_set_header X-Forwarded-Host \$http_host;
            proxy_set_header X-Real-IP \$remote_addr;
            proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto \$scheme;
        }

        location /uploads/ {
            proxy_pass http://127.0.0.1:${BACKEND_PORT}/uploads/;
            proxy_set_header Host \$http_host;
            proxy_set_header X-Forwarded-Host \$http_host;
            proxy_set_header X-Real-IP \$remote_addr;
            proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto \$scheme;
        }

        location / {
            proxy_pass http://127.0.0.1:${WEB_PORT};
            proxy_http_version 1.1;
            proxy_set_header Host \$http_host;
            proxy_set_header X-Forwarded-Host \$http_host;
            proxy_set_header X-Real-IP \$remote_addr;
            proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto \$scheme;
        }
    }
}
NGINX

"$NGINX_BIN" -p "$NGINX_PREFIX/" -c conf/nginx.conf -t

log "Write and start user launchd services"
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
    <string>${DEPLOY_DIR}/backend.log</string>
    <key>StandardErrorPath</key>
    <string>${DEPLOY_DIR}/backend.err.log</string>
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
    <string>${DEPLOY_DIR}/web.log</string>
    <key>StandardErrorPath</key>
    <string>${DEPLOY_DIR}/web.err.log</string>
  </dict>
</plist>
PLIST

cat > "$NGINX_PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
 "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>${NGINX_LABEL}</string>
    <key>WorkingDirectory</key>
    <string>${NGINX_PREFIX}</string>
    <key>ProgramArguments</key>
    <array>
      <string>${NGINX_BIN}</string>
      <string>-p</string>
      <string>${NGINX_PREFIX}/</string>
      <string>-c</string>
      <string>conf/nginx.conf</string>
      <string>-g</string>
      <string>daemon off;</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>${NGINX_PREFIX}/logs/stdout.log</string>
    <key>StandardErrorPath</key>
    <string>${NGINX_PREFIX}/logs/stderr.log</string>
  </dict>
</plist>
PLIST

launchctl load "$BACKEND_PLIST"
launchctl load "$WEB_PLIST"
launchctl load "$NGINX_PLIST"

log "Smoke test"
# Disable keep-alive (-H "Connection: close") so each probe makes a fresh TCP
# connection and avoids a race where Next.js drops the idle socket between
# probes and nginx returns an empty reply (curl exit 52).
SMOKE_OK=0
for i in $(seq 1 60); do
  if curl -fsS --noproxy "*" --max-time 5 -H "Connection: close" \
       "http://127.0.0.1:${BACKEND_PORT}/health" >/dev/null 2>&1 \
    && curl -fsS --noproxy "*" --max-time 5 -H "Connection: close" \
       "http://127.0.0.1:${WEB_PORT}/" >/dev/null 2>&1 \
    && curl -fsS --noproxy "*" --max-time 5 -H "Connection: close" \
       "$PUBLIC_ORIGIN/" >/dev/null 2>&1; then
    SMOKE_OK=1
    break
  fi
  sleep 1
done

if [ "$SMOKE_OK" -ne 1 ]; then
  echo "✗ Smoke test failed after 60s." >&2
  echo "  Inspect logs:" >&2
  echo "    tail -50 ${DEPLOY_DIR}/backend.err.log" >&2
  echo "    tail -50 ${DEPLOY_DIR}/web.err.log" >&2
  echo "    tail -50 ${NGINX_PREFIX}/logs/error.log" >&2
  exit 1
fi

cat <<DONE

✓ Multica deployed rootlessly behind user nginx.

  Public URL:   ${PUBLIC_ORIGIN}
  Listen:       ${NGINX_LISTEN_HOST}:${NGINX_PORT}
  Backend:      http://127.0.0.1:${BACKEND_PORT}
  Frontend:     http://127.0.0.1:${WEB_PORT}
  Deploy dir:   ${DEPLOY_DIR}
  Nginx prefix: ${NGINX_PREFIX}
  Nginx conf:   ${NGINX_PREFIX}/conf/nginx.conf

Logs:
  tail -f ${DEPLOY_DIR}/backend.log ${DEPLOY_DIR}/backend.err.log
  tail -f ${DEPLOY_DIR}/web.log ${DEPLOY_DIR}/web.err.log
  tail -f ${NGINX_PREFIX}/logs/error.log ${NGINX_PREFIX}/logs/stderr.log

Stop services:
  launchctl unload ${BACKEND_PLIST}
  launchctl unload ${WEB_PLIST}
  launchctl unload ${NGINX_PLIST}

CLI reconfigure:
  multica setup self-host --server-url ${PUBLIC_ORIGIN} --app-url ${PUBLIC_ORIGIN}

Open:
  ${PUBLIC_ORIGIN}/login

DONE
