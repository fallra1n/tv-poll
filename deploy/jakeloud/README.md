# JakeLoud demo deployment

This deployment runs the API and static frontend in one application container.
PostgreSQL and Redis remain persistent host-level containers outside JakeLoud's
release lifecycle. It is intended for demonstrating the take-home flow, not for
the 75k votes/s production model described in `docs/ai/02-load-model.md`.

## 1. Configure the project

Use these values in the JakeLoud UI:

```text
Name: tv-poll
Repository: git@github.com:fallra1n/tv-poll.git
Domain: tv-poll.158.160.165.223.sslip.io
Liveness timeout: 5 minutes
Build and start command:
PUBLIC_URL=https://tv-poll.158.160.165.223.sslip.io exec ./deploy/jakeloud/run.sh
```

JakeLoud runs the release command as root and supplies a different `$PORT` to
each candidate release. On the first run, `run.sh` invokes the idempotent
provisioner. It creates:

- `/etc/tvpoll/app.env` and `/etc/tvpoll/postgres.env` with mode `0600`;
- private Docker network `tvpoll`;
- PostgreSQL 16 with volume `tvpoll-postgres-data`;
- Redis 7 with AOF, `noeviction`, and volume `tvpoll-redis-data`.

Database ports are not published on the host. Re-running the script preserves
the existing secrets and data.

The release then builds the root image, applies idempotent Goose migrations,
and binds the app only to `127.0.0.1:$PORT`; JakeLoud's Nginx is the sole
public ingress.

## 2. Optional manual provisioning

If SSH access is available, the same setup can be performed before the first
release from a current repository checkout:

```bash
sudo ./deploy/jakeloud/provision.sh https://tv-poll.158.160.165.223.sslip.io
```

## 3. Verify before promotion

On the host, use the candidate port shown in JakeLoud:

```bash
curl -fsS http://127.0.0.1:<PORT>/healthz
```

Then confirm the candidate in JakeLoud and verify:

```text
https://tv-poll.158.160.165.223.sslip.io/admin
https://tv-poll.158.160.165.223.sslip.io/polls/<poll-id>
```

Run the complete flow: create, schedule, automatic open, vote, duplicate vote,
results, and automatic close.

## Operational limits

- Do not deploy or restart during an open voting window.
- Keep `/etc/tvpoll` out of Git and backups restricted; it contains secrets.
- Back up the PostgreSQL volume before destructive host maintenance.
- This single-host setup does not provide Redis HA, horizontal API scaling, or
  the CDN capacity required by the production load model.
