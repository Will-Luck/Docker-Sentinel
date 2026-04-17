# Docker-Sentinel

Docker container update manager with multi-host cluster support, web dashboard, and configurable update policies.

## Build & Run

```bash
# Prerequisites: Go 1.24+, esbuild (auto-installed via Makefile)

# Build (bundles frontend JS/CSS via esbuild, then compiles Go binary)
make build              # -> bin/sentinel

# Run locally
SENTINEL_DB_PATH=./sentinel.db ./bin/sentinel

# Tests
make test               # go test -v -race ./...
make test-ci            # go test -v -count=1 ./...

# Lint
make lint               # golangci-lint run ./... (config: .golangci.yml)

# Docker image
make docker             # multi-stage build, tagged docker-sentinel:<version>

# Frontend only
make frontend           # bundles JS + CSS via esbuild

# Protobuf / dev deploy
make proto              # requires protoc + go-grpc plugin
make dev-deploy         # deploy to test cluster (.60)
```

## Architecture

```
cmd/sentinel/main.go          Entry point
internal/
  auth/                       Auth: passwords, sessions, TOTP, WebAuthn, OIDC, CSRF
  backup/                     DB backup + S3 upload + retention scheduler
  cluster/                    Multi-host cluster (hub/agent, gRPC, mTLS CA)
    agent/                    Agent node (connects to hub, syncs state)
    server/                   Hub node (registry, auto-update, gRPC server)
  compose/                    Docker Compose file discovery + parsing
  config/                     Env-based configuration
  deps/                       Container dependency graph
  docker/                     Docker client wrapper (containers, images, Swarm)
  engine/                     Core update engine (scan, digest, queue, policy, rollback)
  events/                     Event bus (SSE push to frontend)
  hooks/                      Pre/post-update webhook hooks
  logging/                    Structured logger
  metrics/                    Prometheus textfile exporter
  notify/                     Notification providers (Discord, Gotify, Slack, Telegram,
                              ntfy, MQTT, Pushover, SMTP, Apprise, webhook, HA discovery)
  npm/                        Nginx Proxy Manager integration
  portainer/                  Portainer API client + scanner
  registry/                   Registry digest checker, GHCR, cloud auth (ECR/GCR/ACR)
  scanner/                    Container scan orchestration
  store/                      BoltDB persistence (containers, auth, cluster, hooks, notify)
  verify/                     Post-update health verification
  web/                        HTTP server, REST API, SSE, dashboard, settings UI
    static/src/js/            Frontend JS (vanilla, esbuild-bundled)
    static/src/css/           Frontend CSS (esbuild-bundled)
  webhook/                    Inbound webhook triggers
```

## Conventions

- **Storage:** BoltDB at `$SENTINEL_DB_PATH` (default `/data/sentinel.db`)
- **Frontend:** Vanilla JS + CSS bundled by esbuild into `static/app.js` and `static/style.css`. No framework. Run `make frontend` after editing `src/` files.
- **SSE:** Real-time events pushed to browser via `/api/events` endpoint
- **Cluster:** Hub/agent model over gRPC with mTLS. Hub is the single source of truth.
- **Config:** Environment variables prefixed `SENTINEL_`. See `internal/config/config.go`.
- **Module:** `github.com/Will-Luck/Docker-Sentinel`
- **CI:** `.github/workflows/ci.yml` (lint + test), `release.yml` (Docker image on tag)
- **Labels:** Containers use `sentinel.*` labels for policy, schedule, and hook config
- **No docker-compose on host.** Containers deployed via `docker run` or Portainer.

## Release workflow

Gitea is the squash authority. All merges happen via Gitea PRs. Never squash-merge on GitHub -- squashing the same branch twice (once per remote) produces two different commits with identical content, and the next PR conflicts on shared files (`CHANGELOG.md` is a reliable offender).

Remotes here: `origin` = Gitea, `github` = GitHub.

1. Open the PR on Gitea (`origin`), get CI green, merge (squash).
2. Smoke-test the merged `main` -- for Sentinel this usually means deploying to the .60/.61 test cluster via `/lucknet-ops:cluster-test` or an isolated container with demo targets.
3. Fast-forward GitHub: `git push github origin/main:main`. No GitHub PR needed.
4. Cut the release tag: move `[Unreleased]` -> `[X.Y.Z] - YYYY-MM-DD` in `CHANGELOG.md`, commit on `main`, `git tag -a vX.Y.Z -m "..."`, push tag to both remotes.
5. Post-deploy: promote the production Sentinel on `.57` and run `/lucknet-ops:post-deploy`.

External GitHub contributor? Pull their branch, push to Gitea, open a Gitea PR, squash there, fast-forward GitHub. Their GitHub PR auto-closes as "merged".
