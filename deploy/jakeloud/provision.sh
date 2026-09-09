#!/bin/sh

set -eu

if [ "$(id -u)" -ne 0 ]; then
  printf 'Run this script as root (for example, with sudo).\n' >&2
  exit 1
fi
if [ "$#" -ne 1 ]; then
  printf 'Usage: %s https://poll.example.com\n' "$0" >&2
  exit 1
fi

public_url=${1%/}
case "$public_url" in
  https://*) ;;
  *)
    printf 'The public URL must use HTTPS.\n' >&2
    exit 1
    ;;
esac
case "$public_url" in
  *[[:space:]?#]*)
    printf 'The public URL must be an origin without whitespace, query, or fragment.\n' >&2
    exit 1
    ;;
esac
authority=${public_url#https://}
case "$authority" in
  ""|*/*|*@*)
    printf 'The public URL must contain only an HTTPS origin.\n' >&2
    exit 1
    ;;
esac

command -v docker >/dev/null 2>&1 || {
  printf 'docker is required.\n' >&2
  exit 1
}
command -v openssl >/dev/null 2>&1 || {
  printf 'openssl is required.\n' >&2
  exit 1
}

config_dir=/etc/tvpoll
app_env="$config_dir/app.env"
postgres_env="$config_dir/postgres.env"
install -d -m 700 "$config_dir"

if { [ -e "$app_env" ] && [ ! -e "$postgres_env" ]; } || { [ ! -e "$app_env" ] && [ -e "$postgres_env" ]; }; then
  printf 'Refusing partial configuration: both app.env and postgres.env must exist together.\n' >&2
  exit 1
fi

if [ ! -e "$app_env" ]; then
  umask 077
  database_password=$(openssl rand -hex 24)
  admin_token=$(openssl rand -hex 32)
  vote_token_secret=$(openssl rand -hex 32)

  cat >"$postgres_env" <<EOF
POSTGRES_USER=tvpoll
POSTGRES_PASSWORD=$database_password
POSTGRES_DB=tvpoll
EOF

  cat >"$app_env" <<EOF
PUBLIC_URL=$public_url
HTTP_ADDR=:8080
METRICS_ADDR=127.0.0.1:9090
FRONTEND_DIR=/app/frontend
DATABASE_URL=postgres://tvpoll:$database_password@tvpoll-postgres:5432/tvpoll?sslmode=disable
REDIS_ADDR=tvpoll-redis:6379
REDIS_DB=0
ADMIN_TOKEN=$admin_token
VOTE_TOKEN_SECRET=$vote_token_secret
VOTE_TOKEN_SECRET_PREV=
CORS_ALLOWED_ORIGINS=$public_url
TRUST_PROXY_HEADERS=true
COOKIE_SECURE=true
DEFAULT_VOTING_WINDOW_SECONDS=300
RATE_LIMIT_RPS=6
RATE_LIMIT_BURST=60
LOG_LEVEL=warn
EOF
  chmod 600 "$app_env" "$postgres_env"
else
  configured_url=$(sed -n 's/^PUBLIC_URL=//p' "$app_env")
  if [ "$configured_url" != "$public_url" ]; then
    printf 'Existing PUBLIC_URL is %s, not %s; update %s deliberately.\n' "$configured_url" "$public_url" "$app_env" >&2
    exit 1
  fi
fi
chmod 600 "$app_env" "$postgres_env"

docker network inspect tvpoll >/dev/null 2>&1 || docker network create tvpoll >/dev/null
docker volume inspect tvpoll-postgres-data >/dev/null 2>&1 || docker volume create tvpoll-postgres-data >/dev/null
docker volume inspect tvpoll-redis-data >/dev/null 2>&1 || docker volume create tvpoll-redis-data >/dev/null

if ! docker container inspect tvpoll-postgres >/dev/null 2>&1; then
  docker run --detach \
    --name tvpoll-postgres \
    --restart unless-stopped \
    --network tvpoll \
    --env-file "$postgres_env" \
    --volume tvpoll-postgres-data:/var/lib/postgresql/data \
    postgres:16-alpine >/dev/null
elif [ "$(docker inspect -f '{{.State.Running}}' tvpoll-postgres)" != true ]; then
  docker start tvpoll-postgres >/dev/null
fi

if ! docker container inspect tvpoll-redis >/dev/null 2>&1; then
  docker run --detach \
    --name tvpoll-redis \
    --restart unless-stopped \
    --network tvpoll \
    --volume tvpoll-redis-data:/data \
    redis:7-alpine \
    redis-server --appendonly yes --appendfsync everysec --maxmemory-policy noeviction >/dev/null
elif [ "$(docker inspect -f '{{.State.Running}}' tvpoll-redis)" != true ]; then
  docker start tvpoll-redis >/dev/null
fi

attempt=0
until docker exec tvpoll-postgres pg_isready -U tvpoll -d tvpoll >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    printf 'PostgreSQL did not become ready.\n' >&2
    exit 1
  fi
  sleep 1
done

attempt=0
until [ "$(docker exec tvpoll-redis redis-cli ping 2>/dev/null)" = PONG ]; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 30 ]; then
    printf 'Redis did not become ready.\n' >&2
    exit 1
  fi
  sleep 1
done

printf 'TV Poll dependencies are ready.\n'
printf 'Application environment: %s\n' "$app_env"
printf 'Read ADMIN_TOKEN on the host with: sudo sed -n "s/^ADMIN_TOKEN=//p" %s\n' "$app_env"
