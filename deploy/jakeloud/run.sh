#!/bin/sh

set -eu

: "${PORT:?JakeLoud must provide PORT}"

config_dir=${TVPOLL_CONFIG_DIR:-/etc/tvpoll}
env_file="$config_dir/app.env"
if [ ! -r "$env_file" ]; then
  printf 'Missing readable environment file: %s\n' "$env_file" >&2
  printf 'Run deploy/jakeloud/provision.sh on the JakeLoud host first.\n' >&2
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
