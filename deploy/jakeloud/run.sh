#!/bin/sh

set -eu

: "${PORT:?JakeLoud must provide PORT}"

config_dir=${TVPOLL_CONFIG_DIR:-/etc/tvpoll}
env_file="$config_dir/app.env"

# JakeLoud runs release commands as root. Supplying the non-secret public URL
# lets the first release provision its persistent dependencies without a
# separate SSH session; later releases only re-check the idempotent setup.
if [ -n "${PUBLIC_URL:-}" ]; then
  ./deploy/jakeloud/provision.sh "$PUBLIC_URL"
fi

if [ ! -r "$env_file" ]; then
  printf 'Missing readable environment file: %s\n' "$env_file" >&2
  printf 'Set PUBLIC_URL in the JakeLoud command or run provision.sh on the host.\n' >&2
  exit 1
fi

public_url=$(sed -n 's/^PUBLIC_URL=//p' "$env_file")
if [ -z "$public_url" ]; then
  printf 'PUBLIC_URL is required in %s\n' "$env_file" >&2
  exit 1
fi

image="tv-poll:${PORT}"

docker build \
  --build-arg "BUN_PUBLIC_API_URL=$public_url" \
  --tag "$image" \
  .

docker run --rm \
  --network tvpoll \
  --env-file "$env_file" \
  --entrypoint /usr/local/bin/migrate \
  "$image" up

exec docker run --rm --init \
  --network tvpoll \
  --env-file "$env_file" \
  --publish "127.0.0.1:${PORT}:8080" \
  "$image"
