# Docker-Sentinel — Dev Testing Branch

[![Dev Build](https://github.com/Will-Luck/Docker-Sentinel/actions/workflows/dev-testing.yml/badge.svg?branch=dev-testing)](https://github.com/Will-Luck/Docker-Sentinel/actions/workflows/dev-testing.yml)

This branch contains features and fixes that are being tested before merging into the stable release. If you'd like to help test, pull the dev image:

```bash
docker pull ghcr.io/will-luck/docker-sentinel:dev
```

For the stable release, see the [`main` branch](https://github.com/Will-Luck/Docker-Sentinel/tree/main).

---

## Changes in this build (not yet on main)

### Bug Fixes

- **Auth disabled state no longer locks out the GUI** ([#7](https://github.com/Will-Luck/Docker-Sentinel/issues/7))
  - When authentication was disabled (via the Security tab toggle or `SENTINEL_AUTH_ENABLED=false`), the Security tab disappeared entirely — there was no GUI path to re-enable it without restarting the container
  - The Security tab now stays visible when auth is off, with the auth toggle always accessible
  - User Management, Create User, and OIDC sections are greyed out until auth is re-enabled
  - The "Auth: Off" badge in the nav bar is now a clickable link that takes you straight to the Security tab
  - Improved confirmation dialog when disabling auth — warns about reduced UI and mentions the re-enable path
  - Setup wizard now mentions that auth can be disabled later in Settings > Security

---

## Known Issues

- **Swarm update timeouts** ([#42](https://github.com/Will-Luck/Docker-Sentinel/issues/42)) — Multiple swarm service updates can time out when applied in bulk

---

## Reporting Issues

If you find any issues with this dev build, please [open an issue](https://github.com/Will-Luck/Docker-Sentinel/issues/new) or start a [discussion](https://github.com/Will-Luck/Docker-Sentinel/discussions).
