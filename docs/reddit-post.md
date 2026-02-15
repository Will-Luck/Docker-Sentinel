# I built a Watchtower replacement with per-container update policies and a web dashboard

I've been running Watchtower since 2021. It's great for what it does, but I kept wanting more control — not just "update or don't update" per container, but the ability to review what's changed before approving, automatic rollback if something breaks, and a dashboard to see what's going on. Watchtower's label filtering lets you pick which containers it touches, but once it touches them, it just pulls and restarts. No approval step, no snapshots, no undo.

So I built Docker-Sentinel.

## How it works

You set a policy per container — `auto`, `manual`, or `pinned` — either through Docker labels or the web UI. Auto containers update on their own. Manual ones get queued for you to review and approve. Pinned ones are left alone completely.

Before every update it takes a full snapshot of the container config. If the container fails health checks after updating, it rolls back automatically. I've had this save me twice already with containers that broke on a new version.

## The dashboard

![Docker-Sentinel Dashboard](https://raw.githubusercontent.com/Will-Luck/Docker-Sentinel/main/docs/screenshots/dashboard.png)

Everything runs through a web UI — you can see what's running, what has updates available, approve or reject from a queue, check history, see who did what in the activity log. It groups containers by Compose stack automatically. SSE-powered so it updates live without refreshing.

## Other stuff

- Checks Docker Hub, GHCR, LSCR, Gitea registries — shows where each image comes from
- 7 notification providers (Gotify, Slack, Discord, Ntfy, Telegram, Pushover, webhooks)
- Auth with multi-user support and optional passkey/WebAuthn login
- Lifecycle hooks — run commands before/after updates (handy for database backups)
- Prometheus metrics if you're into that
- Single Go binary, ~30MB Docker image, BoltDB for storage — no external dependencies

## Running it

```bash
docker run -d \
  --name docker-sentinel \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v sentinel-data:/data \
  -p 8080:8080 \
  willluck/docker-sentinel:latest
```

Open `localhost:8080`, create an admin account, done.

## Links

- **GitHub:** https://github.com/Will-Luck/Docker-Sentinel
- **Wiki:** https://github.com/Will-Luck/Docker-Sentinel/wiki
- **GHCR:** `ghcr.io/will-luck/docker-sentinel`

It's Apache 2.0 licensed. Written in Go. Been running it on my own server for a few weeks now and it's been solid, but it's still young — feedback and issues welcome.
