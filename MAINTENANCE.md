# Maintenance Mode

As of 2026-05-08, Docker-Sentinel is in maintenance mode. v2.12.2 is the
final feature release.

## What this means

- Bug-fix PRs and security issues are still reviewed and merged.
- New features will not be added.
- Issues remain open. Replies may be slower than during active development.
- The repo is not archived, and this state is reversible.

## Why

Sentinel was built around a manual-approval queue for container image
updates plus per-container policy labels. After several months of
production use, the differentiating features (`sentinel.policy` labels,
manual approval queue, web dashboard) saw zero adoption in the
maintainer's own deployment. The simpler watchtower approach covers the
remaining functionality (auto-update with notifications) at a fraction
of the maintenance cost.

## Recommended alternatives

- [nicholas-fedor/watchtower](https://github.com/nicholas-fedor/watchtower)
  for auto-updating Docker containers with notifications. Active fork
  of the original containrrr/watchtower (archived 2025-12-17).
- For Kubernetes-style orchestration with native rollback, see k3s or
  full Kubernetes.

## Reporting issues

- Bugs: open an issue on this repo.
- Security: see SECURITY.md for the responsible disclosure path.

The maintainer continues to use Sentinel passively until the watchtower
migration is complete on the production fleet, so bug reports are
welcome.
