# Contributing to Docker-Sentinel

Thanks for your interest in contributing. This guide covers everything you need to get started.

## Prerequisites

- **Go 1.24+**
- **Docker** (for integration tests and building images)
- **Node.js** (for esbuild frontend bundling)

## Building

```bash
make build          # Build the Go binary
make frontend       # Bundle frontend assets (esbuild)
make test           # Run tests
make lint           # Run golangci-lint
```

## Project Structure

The web frontend is modular. Source files live in `static/src/js/` and `static/src/css/`, and esbuild bundles them into `static/app.js` and `static/style.css`.

**Never edit `static/app.js` or `static/style.css` directly.** These are build artefacts. Edit the source modules and run `make frontend` to rebuild.

## Code Style

- UK English throughout (code comments, UI strings, documentation)
- Run `make lint` before every commit. Formatting failures are the most common CI issue.
- All code is licensed under Apache 2.0. By contributing, you agree your contributions are under the same licence.

## Pull Request Process

1. Fork the repository
2. Create a feature branch from `dev`
3. Make your changes
4. Include tests for new features or bug fixes
5. Run `make test` and `make lint` to verify everything passes
6. Open a PR against the `dev` branch

PRs against `main` will be closed. All changes go through `dev` first.

## Commit Messages

Use conventional commits:

```
feat: add webhook retry logic
fix: correct image tag parsing for GHCR
docs: update configuration examples
test: add coverage for policy engine
refactor: extract registry client into separate package
```

Keep the subject line under 72 characters. Add a body if the change needs explanation.

## Reporting Bugs

Open a GitHub issue with:

- Docker-Sentinel version
- Docker version and host OS
- Steps to reproduce
- Expected vs actual behaviour
- Relevant log output (with sensitive data redacted)

## Questions

Open a GitHub Discussion or issue. We are happy to help.
