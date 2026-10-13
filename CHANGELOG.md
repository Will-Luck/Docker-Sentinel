# Changelog

All notable changes to Docker-Sentinel are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security

- Importing a config export dropped the signature verification settings
  (`verify_mode`, `cosign_path`, `cosign_keyless`, `cosign_key_path`), so
  an enforce mode came back disabled. A config import now keeps them.
- Signature verification checked the tag as the registry served it at
  check time, so the image could change between the check and the
  create. Updates now check the digest they pulled, in the image's own
  repository.
- Keyless cosign verification accepted a signature from anyone with a
  Sigstore certificate. It now checks the certificate identity and OIDC
  issuer set in `cosign_identity` and `cosign_oidc_issuer` (or
  `SENTINEL_COSIGN_IDENTITY` and `SENTINEL_COSIGN_OIDC_ISSUER`; a saved
  setting wins), matched exactly or, with `cosign_identity_regexp` /
  `SENTINEL_COSIGN_IDENTITY_REGEXP` set to true, as regular expressions.
  Without them keyless checks fail: in enforce mode each refused update
  is recorded as failed in the history and a scan sends one notification
  for them, and startup logs a warning naming the two settings. They
  apply without a restart; switching keyless on or off, the key path and
  the cosign path still need one.
- With signature verification set to enforce, updates went ahead without
  any signature check when cosign was not available (the official image
  ships none), with only an info line at startup. Such updates are now
  refused and recorded as failed ("signature verification is set to
  enforce but cosign is not available"), each scan sends one notification
  for them, and startup logs a warning; warn mode still updates and logs
  each one. A mode saved in Settings now applies without a restart, and a
  `sentinel.verify` label is honoured when the global mode is disabled.
- Swarm service updates never had their image signature checked. They
  now go through the same gate as containers: enforce blocks an image
  that fails the check (or any update when cosign is not available), warn
  logs and updates, and a `sentinel.verify` label on the service wins
  over the global mode. A digest-only service update checks the exact
  digest it rolls out.
- A cluster host being revoked could keep a valid certificate: a
  certificate renewal arriving during the revoke was recorded just before
  the host was removed, and its certificate was never revoked. Revoking
  now revokes the current and any pending certificate and removes the
  host in one step. The server also accepted any certificate its CA had
  signed for a host's ID, including superseded renewals; agents must now
  present the host's current certificate or its pending renewal.
- The server signed certificate renewals whenever an agent asked, for any
  key. It now signs one only when the agent's certificate has less than
  half its life left, only for a new key, at most once per host every
  five minutes, and only for ECDSA P-256 or P-384 keys.
- One connection to the cluster port could open any number of streams,
  each allowed a 64 MB message before any certificate was checked. The
  server now allows 64 streams per connection.
- An agent enrolling trusted whatever answered at `SENTINEL_SERVER_ADDR`,
  so anything on the network path could take the one-time token and
  become that agent's server. Enrolment tokens now end with the
  fingerprint of the server's CA (`<secret>.<fingerprint>`), the server
  sends its CA in the TLS handshake, and the agent checks the two match
  before it sends the token, and again on the CA it receives. Copy the
  whole token. A token without the fingerprint (from an older server)
  still enrols, with a warning that the server could not be checked;
  agents before v3.0.0 can use the new tokens.
- `GET /api/settings` returned the OIDC client secret, the Nginx Proxy
  Manager password, the inbound webhook secret and the agent enrolment
  token in plaintext to the Viewer role and to tokens with `settings.view`.
  They are now left out, and the response carries `<key>_set: "true"` for
  each one that is configured. The same secret list now drives the settings
  read and the config export, so an export made without secrets no longer
  contains the NPM password. A test fails if a stored setting that ends in
  `_secret`, `_password` or `_token` is missing from the list.
- `GET /api/settings` also returned environment-sourced URLs unmasked, such
  as `SENTINEL_WEBHOOK_URL` and `SENTINEL_GOTIFY_URL`, which often carry a
  token in the path or query. URL values now lose their credentials (the
  webhook and Gotify URLs keep only scheme and host), and a test fails if a
  configuration value exposes a credential. `GET /api/settings/webhook-info`
  returned the whole inbound webhook secret when it was 12 characters or
  shorter; it now returns at most a four-character prefix.
- After an account lockout expired, the failed-attempt count was not reset, so
  one wrong password locked the account again for another 30 minutes. The
  count now starts again when a lockout expires.
- Webhook notification channels now mask every header that looks like a
  credential (Basic auth, cookies, passwords as well as tokens and keys) and
  the secret path of the webhook URL when channels are shown to the Viewer
  role or exported without secrets.
- Single sign-on could take over a local account. Sentinel matched an SSO
  login to an existing account by username (or email), so anyone who could
  set their username at the identity provider to `admin` was signed in as the
  local admin, skipping 2FA. Accounts are now bound to the provider's issuer
  and subject, and login matches on that pair only. A new SSO identity whose
  username clashes with an account it is not linked to is refused with a
  clear message instead of being logged in as that account.
  - Signed-in users link their SSO identity from the Account page; an
    administrator can remove the link from the user list.
  - Upgrade note: nothing links to an existing account by username on its
    own any more. After upgrading, a user who signed in with SSO under an
    older version is told "This account is not linked to your sign-in
    provider yet" and needs one of three things: an admin clicks "Allow SSO
    link" on the user list (the next SSO login with that username links the
    account, once, within 24 hours; both steps are written to the activity
    log), the user signs in with a password and links from the Account page,
    or you switch on `SENTINEL_OIDC_LINK_EXISTING=true` (or the matching
    toggle in Settings, which wins over the variable), which links unlinked
    non-admin accounts by username. Admin accounts, and accounts with 2FA
    or a passkey, are never linked by that switch.
  - Recovery for instances whose admins only ever signed in with SSO (their
    password is unknown, so none of the routes above works): set
    `SENTINEL_OIDC_LINK_ADMIN=<username>` and restart. The next SSO login
    with that username links that one account, admin or not, once; Sentinel
    logs a warning and writes the activity log, and remembers it has been
    used for that account. Remove the variable afterwards.
- Settings > Signature Verification now warns when cosign is not found at
  the configured path inside the container (the Sentinel image does not
  include it), and says what enforce and warn do without it. Approving an
  update that the engine refused (signature check or pre-update hook) left
  two failed rows in the history; it now leaves one.
- Settings > System Settings has a History retention field (days of update
  history and activity log to keep; successful updates and rollbacks are kept
  for good; 0 keeps everything). A value saved there overrides
  `SENTINEL_HISTORY_RETENTION_DAYS`, and the page says which one is in effect.
- The first-run setup wizard is served over HTTPS when TLS is configured
  (`SENTINEL_TLS_AUTO` or a certificate), instead of plain HTTP.
- The Sentinel container's own row shows its real state instead of always
  "Running". Fonts are now bundled and served by Sentinel, because the
  page's CSP blocked Google Fonts. The Cluster page's Server card no longer
  overflows on a long version string.
- A 2FA code could be used again within its 30-second window, and a password
  success cleared the sign-in throttle, so someone holding a password got
  many more code guesses than intended. A code is now accepted once,
  entering the password no longer resets the throttle, and a pending 2FA
  sign-in is thrown away after five wrong codes.
- Request bodies are capped (64 KB on the public sign-in endpoints, 8 MB
  elsewhere), closing a container log no longer leaks a goroutine, and the
  inbound webhook help says the secret goes in the `X-Webhook-Secret` header.
- Failed stop, start and restart now show an error, settings toggles no longer
  report success when the save failed, History rows open their details, and
  a remote container's notification setting is kept under its host.
- Settings shows whether Trivy is available, has a form for cloud registry
  logins (ECR, ACR, GCR) and marks registries whose stored login was
  rejected.
- Behind a reverse proxy, five failed logins from one attacker blocked
  sign-in for everyone, because every client shared the proxy's address. Set
  `SENTINEL_TRUSTED_PROXIES` to the proxy's IP or CIDR range (comma
  separated) and Sentinel takes the client from `X-Forwarded-For`. With it
  unset nothing changes.
- After the first connection, changing the NPM URL or login, or switching NPM
  off, changed nothing until a restart, and Test connection reported success
  against the old target. Test now rebuilds from the saved settings and the
  old connection is dropped.
- Signature Verification has Signer identity, Identity is a pattern and OIDC
  issuer fields, shown in keyless mode, because keyless verification now
  refuses unless the expected signer is set.
- The 2FA QR code renders again (its library was blocked by the page's CSP),
  `escapeHTML` escapes quotes, and a refused image removal is counted as
  failed in Images bulk Remove.
- A channel's Test button now tests what is on screen (a channel just added or
  edited but not saved too); it used to test the saved copy or fail with
  "channel not found".
- Several small dashboard and settings fixes: adding or removing a
  notification channel, registry login or release source no longer wipes what
  was typed on the others; a failed stop, start, restart or update clears its
  spinner; failed policy and cluster changes are put back as saved; the
  Cluster page no longer reloads away a new enrolment token; in cluster mode
  collapsing a stack no longer changes the stat cards and a stack's
  select-all works; the queue's Enter key opens the focused row; All Registry
  Tags shows errors properly.
- A manual Check for updates on a local container reported a new image behind the same tag (for example :latest) as "up to date (newer versions ignored)" and did not queue it. It is now queued, as a scan does.
- Saving other OIDC settings no longer pins "Link existing accounts by
  username" over `SENTINEL_OIDC_LINK_EXISTING`: the toggle is stored only
  when you change it. The opt-in also skips accounts with 2FA or a passkey.
- NPM Test connection uses the same settings as startup (the environment,
  then saved settings), and the NPM connection built at startup stops
  syncing when it is replaced or switched off.
- An abandoned "Link single sign-on" no longer breaks the next SSO sign-in,
  and only messages that begin with an action failure show as errors.
- "Update to version" and the GHCR switch on a container on another host left no history row (the notification still went out). Both now record success or failure under the host-scoped key, like Update now.
- Approving a local update that failed no longer risks a second failed row in the history: local updates record their own history in the engine for every failure, and only remote updates are recorded by the web layer.
- Settings > Scanning showed the poll interval and grace period as a blank "Custom..." and Show stopped as Off while the environment set 10m, 30s and true, so saving there could overwrite the real values. The page now shows the value in effect for every setting the environment can set, says whether it is saved or from the environment, and the scanner, verifier and notification retry forms send only the fields you changed.
- At phone width (390 px) the dashboard no longer squeezes container names away: Image, Policy and Ports collapse properly and the Status column keeps only the width its badge needs.
- Cluster host cards showed an agent's version as "vv3.0.0-rc4" and let the commit hash overflow the card; the version is now shown once and the full text is in a tooltip.
- The page of a container on another host showed a blank Container ID and did not say which host it is on; it now shows both.
- The service page's Policy and Notification Preferences sections were unstyled and named their options differently from the container page; they now match it. A Swarm task on a registry with a port showed its image tag cut to "5443/lh/app:1.0.1".
- The Logs page showed raw host IDs ("Container stopped on 37770888f2...") and one line named a user by ID. Activity-log lines and failure messages now name the host, and the SSO unlink line names the user.
- The history's scan summary counts did not add up (165 checked against 66 + 96 + 18): containers whose registry check failed, ones skipped for a swap in progress, ones whose newer versions were all ignored, rolled back before, or held back by a dry run were in no count. Each now lands in exactly one, and "check failed" is shown as its own figure.
- The queue page kept a row for an entry removed elsewhere (approved from another client, the API, an auto-update or a scan finding it up to date) until you reloaded; it now refreshes on any queue change.
- The container and service pages now show the pending-updates count in the navigation, like the other pages.
- At phone width the dashboard's Name column now takes the free width instead of a fixed 20%, so container names are no longer cut at about eight characters.
- An untouched Apply on the poll interval or Save on scan concurrency (and the other Apply and Save buttons on Settings) still sent a request, turning a value set by the environment into a saved override. They now send nothing while they show what was loaded, and the toggles the environment can also set say when they now override it.

### Added

- `DELETE /api/swaps/{name}` (admin only) discards the record of an update
  Sentinel could not finish on its own, once you have fixed the containers
  by hand. Sentinel tells you when that is needed, after three failed
  attempts. There is no dashboard button for it yet.
- Self-update works when Sentinel publishes a host port, for the hub, a
  standalone instance and an agent. A running container cannot free its own
  port or database lock before the new one starts, so every earlier attempt
  rolled back. Sentinel now sets the replacement up and starts a short-lived
  helper container from the new image (the hidden `self-update-helper`
  subcommand, with the Docker socket mounted). The helper stops the old
  container, starts the new one, waits for it to stay up (and healthy, if it
  has a health check), puts the old one back if it does not, and leaves the
  outcome in the data directory; the new instance logs it on startup. The
  replacement is built the same way as every other recreate, so it keeps its
  networks, anonymous volumes and the new image's defaults. If the Docker
  socket is not mounted in the Sentinel container (a TCP Docker host), the
  old in-process swap is used, which still needs no published ports.
  Leftover `-old-<timestamp>` containers and helpers are removed at start,
  for agents too.
- The new Sentinel image goes through the signature gate before a
  self-update: in enforce mode it is refused when cosign is missing or the
  signature does not verify, and in warn mode the failure is logged. Agents
  rely on the hub's check of the target image.
- Sentinel sends the registry credentials it has stored to the Docker daemon
  (`X-Registry-Auth`) for every pull and digest lookup, so a private registry
  no longer needs `docker login` on the host, and for Swarm service updates.
  For remote hosts the hub sends the credential for the target image's
  registry with the update request (new optional `registry_auth` field,
  number 20, on `UpdateContainerRequest`; older agents ignore it).
- AWS ECR, Azure ACR and Google GCR/Artifact Registry logins, advertised
  since v2.9 but never connected, now work. Configure a provider by saving
  JSON under the setting `cloud_auth_ecr` (`region`, `access_key`,
  `secret_key`), `cloud_auth_acr` (`tenant_id`, `client_id`,
  `client_secret`, `login_server`) or `cloud_auth_gcr`
  (`service_account_json`). Tag listing, digest lookups and pulls for a
  matching host use it when no login is stored for that host.

### Changed

- Built with Go 1.27.2 (was 1.26.9). Two effects of Go 1.27: the macOS
  binaries need macOS 13 or later, and Sentinel exits at startup if
  `GODEBUG` sets one of the settings Go 1.27 removed to its old value
  (`tlsrsakex`, `tls10server`, `tls3des`, `tlsunsafeekm`, `x509keypairleaf`,
  `asynctimerchan`, `x509sha1`). Remove such a setting before upgrading.
  Building from source still works with Go 1.26 or later, which fetches
  go1.27.2 through `GOTOOLCHAIN=auto`.
- Sentinel now keeps a journal of every container it is replacing, so
  that after a crash, a shutdown or a reboot it can finish or undo the
  replacement exactly; each scan retries anything still unfinished. Give
  the Sentinel container time to finish an update when it is stopped:
  `stop_grace_period: 2m` in compose, or `--stop-timeout 120` with
  `docker run` (it waits up to two minutes for updates in flight).
  Replacement containers carry a `sentinel.swap` label so Sentinel can
  recognise its own replacement after a crash; it is safe to ignore.
- An update that was tried and rolled back (its new container would not
  start or failed validation) is not tried again, under auto policy,
  until a newer version or a different image digest appears, so a fixed
  build pushed under the same tag or version is picked up. Manual
  containers still see the update.
- `remove_volumes` (and the `sentinel.remove-volumes=true` label) now
  works like `docker compose up -V`: the updated container starts on
  fresh, empty anonymous volumes and the old ones are deleted with the old
  container once the new one passes validation. With it off (the default)
  the new container keeps the old anonymous volumes and their data. Named
  volumes and bind mounts are never touched.
- A container that had no hostname of its own now gets a new one when it
  is updated (its own short ID, as with docker compose) instead of keeping
  the first container's ID for good. Apps that key data on the hostname
  see it change once.
- Compose `depends_on` dependents are restarted after their dependency is
  updated only when the entry has `restart: true`, as compose does. The
  dependency graph still shows every edge.
- The cluster wire format gains two optional fields: an error on the
  container list an agent sends, and a list of server features
  ("cert-renewal", "keepalive") in the reply to an agent's state report.
  Servers and agents on v2.16.x ignore them, so either side keeps working
  with the other.
- Agents keep their swap journal in their database (`SENTINEL_DB_PATH`, in
  the data directory), next to the settings of their web UI.
- The cluster wire format gains optional fields: the images a container
  ran before and the server's hooks with an update request, a host name in
  an agent's state report, and remove volumes and image backup in the
  settings the server sends. Servers and agents before v3.0.0 ignore them.
- Enrolment tokens are longer: they end with `.<fingerprint>` of the
  server's CA.
- An agent reported an update as done as soon as the new container
  started, and removed the old one: an image that exits right after start
  left the service down and "Restarting". Agents now validate a new
  container as the server does for its own: they wait the grace period
  (the server's, or the agent's `SENTINEL_GRACE_PERIOD`, or the
  container's `sentinel.grace-period` label), check that it is still
  running and, with a healthcheck, healthy, and otherwise put the old
  container back and report a rollback. The server then notifies the
  failure and the rollback and applies its rollback policy to the
  container, as for a local rollback.
- An agent updated by "Auto-update agents" left no history row, so
  Rollback of that agent's own container found nothing to go back to, and
  a Rollback of a remote container from the dashboard left no row either.
  The server now records the history of a remote update or rollback that
  nothing else records (the agent's result, or why it got none).
- Setting the rollback policy back to "No change" on the server never
  reached agents, which kept showing the previous policy.
- When signature verification refused a remote update because the image
  could not be checked at all (enforce without cosign, or keyless without
  an identity), the server sent "update started" and "update failed" for
  that container on its own. Refused remote updates now join the one
  notification a scan sends for all the updates it refused, each with a
  failed history row, as local and Swarm ones do; one refused from the
  dashboard sends no notification, as a local one does.
- One precedence rule for image backup, remove volumes and the rollback
  policy: a value saved on the Settings page overrides the environment
  (`SENTINEL_IMAGE_BACKUP`, `SENTINEL_REMOVE_VOLUMES`,
  `SENTINEL_ROLLBACK_POLICY`), at startup and as soon as it is saved; with
  nothing saved, the environment value applies. A saved rollback policy of
  "No change" also overrides the environment. Settings shows the value in
  effect and whether it comes from the saved setting or the environment.
  The UI rollback now applies the same policy as the engine. If saving the
  Image backup or Remove volumes toggle fails, the toggle now flips back and
  shows the server's error instead of reporting success.
- `POST /api/bulk/policy` accepts `targets`, a list of `{name, host}` pairs
  (empty host means this instance). The `containers` list of bare names
  still works.
- Ignore now works on updates for `:latest` and other tags with no version
  list. It previously failed with "digest-only update". It stores the
  version the tag resolves to, or the new digest when none is known, so the
  ignore covers that one update only. The queue and dashboard rows show the
  resolved version, or a "new digest" label, instead of "build".
- History showed `hostID::name` for a container on another host and linked
  it to a page that returned 404. Rows now show the container's own name next
  to the host badge and link to its page on that host.
- "Update now" on a container on another host left no history row. The
  result, success or failure, is now recorded.
- The Cluster page warns on a host card when the same Docker engine is also
  enrolled as another host, so double-counted containers have a visible
  cause.
- History and the activity log are no longer kept for ever: records older
  than 90 days are pruned daily (at start-up too). Set
  `SENTINEL_HISTORY_RETENTION_DAYS`, or the `history_retention_days`
  setting, which wins over the variable; 0 keeps everything. Records of
  applied updates and rollbacks are never pruned.

### Removed

- The Portainer connector. Sentinel no longer scans or updates containers
  through the Portainer API, and the Portainer pages, settings and
  `/api/portainer/*` endpoints are gone. To keep monitoring those hosts, run
  a Sentinel agent on each one (see Cluster Mode). Saved Portainer settings
  stay in the database untouched, so downgrading restores them; queued
  Portainer updates are dropped at startup, and `SENTINEL_PORTAINER_URL` /
  `SENTINEL_PORTAINER_TOKEN` are ignored with a warning.

### Fixed

- The scan summary did not add up ("165 checked" against 162 in the
  counts): Swarm task containers were counted in the total but in no
  count, and remote containers whose registry check failed, whose update
  was ignored or that a dry run passed over were in none either. Every
  container and service a scan looks at now lands in exactly one count,
  the total is their sum, and Swarm task containers are left to their
  service.
- A local container on a mutable tag such as `latest` that ran an older
  image than its tag was queued without the versions remote containers
  show, so ignoring it stored a digest. Its queue entry now carries the
  current and target versions, resolved from the image it runs.
- A rollback started from the dashboard or the API wrote nothing to the
  update history and sent no notification. A successful one is now
  recorded as a rollback with both images and notified.
- When a new container failed validation (an image that exits straight
  after start), the log and the notification said "validation failed:
  <nil>" and the history kept only a rollback row without images. They
  now give the reason (exit code, restart count or health status), the
  history records the failed update with its old and new image, and the
  rollback row names the images too.
- When an update failed and the daemon would not remove the new
  container, the service stayed down until a later recovery. The failed
  container is now renamed aside (`<name>-sentinel-failed`) with its
  restart policy off, the old container gets its name back and starts at
  once, and recovery removes the set-aside container later. Agents do the
  same.
- With image backup on, image cleanup deleted the backed-up image (taking
  the backup tag with it) for mutable tags, while for versioned tags the
  backups piled up for good. A backed-up image is now kept by cleanup, so
  a rollback can use it, and only the newest three backups of each
  repository are kept.
- The history recorded an update as rolled back even when the rollback
  itself failed, and a failed final step wrote a second, contradictory
  row. A failed rollback is now recorded as failed, with the reason and
  the rollback error, in one row.
- When Sentinel was down through an interrupted update, recovery put the
  old container back without a trace in the update history. Each
  interrupted update, rollback or recreate that recovery resolves now
  gets a history row saying what it did (finished it, restored or rebuilt
  the old container, or kept the replacement).
- Post-update hooks ran in the updated container just before Sentinel
  replaced it to remove the maintenance label, so anything a hook did
  inside the container (and a hook still running) was lost. They now run
  in the container that stays, after that step.
- Validation gave up on a healthcheck after a fixed 60 seconds and rolled
  back containers that were still starting, and every stop Sentinel made
  cut a container's own longer stop timeout to 30 or 10 seconds. The
  health wait now follows the healthcheck (start period, interval,
  timeout and retries, at least 60 seconds and at most 30 minutes), and a
  container's stop timeout is used when it is longer than Sentinel's,
  with the time to wait it out in full. If a stop still does not return,
  the old container is started again and recovery watches it.
- Maintenance windows: a weekly window over several days (such as
  `Fri 22:00-Mon 06:00`) was closed on the days between, a `Sat
  22:00-02:00` window ran on the wrong night, and a window shorter than
  the poll interval could be missed altogether because scans ran only at
  their usual times. Weekly windows are now evaluated across days, and the
  scheduler also wakes when a closed window opens.
- Containers updated outside Sentinel, or removed, stayed in every digest
  for good. A container found up to date now leaves the digest, and the
  digest state of removed containers and services is pruned at each scan.
- Compose sync turned an image from a registry with a port, such as
  `registry.local:5000/app:1.0`, into an invalid reference. The tag is now
  taken after the last path segment, digest-pinned images are left alone
  and quotes are kept.
- With a digest interval under a day, the digest fired every minute from
  the day's last slot until midnight. The next slot is now always ahead.
- A scan counted an update that a pre-update hook skipped (exit 75), or
  whose pull found the image already running, as updated, and the scan
  summary in the history printed the rate-limited count as "skipped".
  Hook skips now count as skipped, unchanged images as up to date, and
  the summary prints skipped and rate-limited counts separately.
- A scan updated containers in the order the daemon listed them, so a
  container could be recreated before the container it depends on
  (`sentinel.depends-on`, compose `depends_on`, a `container:` network
  mode) had its own update. Scans now update dependencies first. A
  dependency cycle is broken in name order and logged as a warning.
- Containers that an older Sentinel had updated kept the environment,
  labels and command of the first image they ran, because that Sentinel
  copied the whole config and only the current image's defaults were
  left out on the next update. Sentinel now also leaves out values equal
  to a default of any image the container ran before, found through its
  update history (the last 10 updates that changed its image, same
  repository only) and read from the host or, when the image is gone,
  from its registry with the stored credentials. An image it cannot read
  is skipped.
- Updating a container could delete it. Sentinel stopped and removed the
  old container before it knew the new one could be created, so a create
  the daemon rejected (or a missing image) left the service gone when the
  rollback failed the same way. Updates now swap containers: the old one
  is renamed to `<name>-sentinel-old`, the new one is created under the
  real name while the old one still runs, the old one is stopped and the
  new one started. The old container is removed only after the new one
  passes validation; on any failure it gets its name back and is started
  again, with its own image and volumes. A stop that fails while the old
  container keeps running fails the update rather than starting a second
  copy next to it, and the old container's restart policy is set to `no`
  while it is stopped so a reboot cannot start both.
- Updating or rolling back a container that shares another container's
  network (`network_mode: container:X` or `service:X`, for example
  qbittorrent behind gluetun) deleted it: the daemon rejected the create
  because Sentinel sent back the provider's hostname and the image's
  exposed ports. Those fields are now dropped for that network mode.
- After a network provider such as gluetun was updated, rolled back or
  restored after a failed update, the containers sharing its network were
  left with no network while the update reported success. They are now
  recreated on (or restarted with) the provider container.
- Containers that keep their data in an image `VOLUME` or a bare
  `-v /path` came back from an update on an empty volume (postgres re-ran
  initdb) and the old data was orphaned, or deleted with `remove_volumes`
  on. The new container now takes over the old anonymous volumes, as
  docker compose does.
- After an update the container kept the old image's environment, labels,
  entrypoint, healthcheck and similar defaults, so the new image's values
  never applied and the dashboard kept showing the old image version.
  Values that came from the old image are now dropped so the new image's
  apply; values you set yourself are kept, including an `--entrypoint`
  that clears the image's command. One limit: an environment variable or
  label you set to exactly the old image's value cannot be told apart from
  the image's own and follows the new image. Rollbacks still restore the
  snapshot exactly, and post-update hooks declared as image labels now
  run.
- A dashboard rollback stopped and deleted the running container before
  checking that the old image was still there; with image cleanup on, a
  rollback after a version update failed and left the container deleted.
  After a `:latest` update the rollback recreated the new image and
  reported success. Rollbacks now check the snapshot's image first (it
  must be on the host, or its tag must pull back to the same image),
  recreate exactly that image on the running container's current
  volumes, and keep the running container if anything fails. A rollback
  that would have to move a tag another running container uses refuses
  and says which container. A failed dashboard rollback is now shown on
  the dashboard and notified instead of only being logged.
- A broken `:latest` update was pulled, started, failed and rolled back
  again on every scan, with a notification each time. It is now tried
  once (see Changed).
- A dashboard rollback after a later failed update attempt restored the
  version already running, because every attempt replaced the rollback
  snapshot. The snapshot is now saved only when an update succeeds.
- Stopping Sentinel or rebooting the host during an update could leave
  the container stopped, deleted, doubled or stuck with the maintenance
  label. An interrupted update (including one started from the dashboard)
  now puts the old container back, and at startup Sentinel waits briefly
  for Docker, then finishes or undoes whatever the journal lists and
  clears stale maintenance flags. A container is never left as two copies:
  a failed restore is reported rather than rebuilt next to a container
  that still exists. When Sentinel was killed while Docker was still
  stopping the old container, Docker finished that stop after the old
  container was put back and nothing started it again; Sentinel now
  watches a container it put back until that stop has timed out, starts
  it again if it stops, and notes this in the update history.
- Containers started with `--rm` were deleted by an update, because the
  daemon removed them on stop and the update then gave up. They are now
  replaced like any other container.
- Containers that depend on an updated container (`sentinel.depends-on`
  or compose `depends_on`) were never restarted after it, whatever
  `SENTINEL_DEPS` said. They are now restarted, compose `depends_on`
  entries are matched to their service in the same project (every
  container of a scaled service), and a dependent that is being updated
  itself is left alone.
- Containers on `:latest` or another mutable tag such as `:alpine` were
  recreated pinned to the version the tag pointed at (`nginx:latest`
  became `nginx:1.29.2`), so they quietly stopped following the tag.
  Updates to a mutable tag now re-pull the same tag. The version the tag
  resolves to is still shown for information.
- A container on a stable tag (`gitea:1.24`, `app:1.2.3`) was offered a
  release candidate such as `1.25.0-rc0` as its update, and on the auto
  policy was moved onto it. Pre-release tags (rc, beta, alpha, dev and
  similar) are now only offered to a container that is itself on a
  pre-release.
- Pre-release tags were ordered as text, so `rc10` counted as older than
  `rc9`. They are now compared by number.
- Containers pinned to a build-counter tag such as `4.3.3-ls190` or
  `7.2.4-debian-12-r9` were shown as up to date forever, because the
  trailing counter made every tag its own variant. Trailing `ls` and `r`
  counters are now ignored when matching variants and used to order tags
  within one.
- When two containers ran the same mutable tag and one was updated, the
  other was reported up to date, and from the next scan Docker reported
  its image as an ID, so it was skipped as a local image for good. Pull-only
  mode dropped every container the same way after its first pull. The scan
  now follows the image a container was created with, reports a container
  that runs an older image than its tag as having an update, and pull-only
  no longer pulls a tag it has already pulled.
- Swarm services on a mutable tag never rolled out a digest-only update:
  the old digest pin was resubmitted, so Sentinel reported a false success
  or waited ten minutes and reported a timeout, on every scan. The update
  now drops the old pin, re-pins to the digest the registry serves and
  forces a rollout, and the rollout check ignores a status left over from
  an earlier rollout.
- Self-update on a hub or standalone install never took effect, because
  the old Sentinel container was left running: the replacement either could
  not open the locked database and crash-looped, or never started. The old
  container is now stopped once the replacement is running, a replacement
  that cannot start is removed and the old container gets its name back
  (with a hint when published ports are the cause), a failed pull no longer
  blocks an update to an image already on the host, and leftover exited
  `-old-<timestamp>` containers are removed at startup. The replacement
  waits up to a minute for the database lock instead of exiting after five
  seconds, and if the old container cannot be stopped the replacement is
  removed and the old one gets its name back.
- A Swarm digest-only update now fails with a clear error, leaving the
  service unchanged, when the new digest cannot be looked up (private
  registry or outage), instead of resubmitting it without its digest pin.
- An ignored update on `:latest` or another mutable tag was notified and
  auto-applied again on the next scan. It now stays ignored until the tag
  moves to a new version or digest.
- A pull-only container that was already pulled but still behind its tag
  showed as an update and counted as automatic on every scan. It is now
  skipped.
- Version checks failed against registries that answer with a Bearer
  challenge (lscr.io, registry.gitlab.com). Sentinel now follows the
  challenge to the registry's token endpoint.
- Images with many tags (linuxserver/* on GHCR) lost their newest versions
  because tag listing stopped after 10,000 tags, and registries with pages
  under 1,000 tags were read only to the first page. Listing now follows the
  `Link` header and stops after 100,000 tags.
- An expired or revoked Docker Hub or GHCR token made every version check
  for that registry stop quietly. The rejection is logged as a warning and
  the token request is retried anonymously.
- Registry failures (rate limit, outage) for official Docker Hub images such
  as `nginx` were logged at debug level and counted as local images. They
  now record a failed check. Swarm service lookups report their failure the
  same way.
- A container on a floating tag such as `nginx:1.27` was offered the pinned
  `nginx:1.29.2` instead of `nginx:1.29`, which stopped it floating. A
  floating tag now gets the tag of its own precision for a version line
  first, when the registry publishes one.
- The scanner settings (mode, threshold, Trivy path) only applied at startup,
  and in the stock image, which has no Trivy, "Pre-update" scanning checked
  nothing and said nothing. The saved settings are now read when each update
  runs, Trivy is looked for again at the saved path, and a pre-update scan
  that cannot run is logged as a warning.
- The new Sentinel instance could delete the old container within seconds
  of starting, while the self-update helper was still deciding whether to
  keep the new one. If the new version then crashed, nothing was left to
  restore. The helper now marks the swap as in progress, removes the old
  container itself on success, and startup cleanup leaves old containers
  alone while a helper is active or the swap is in progress, and otherwise
  only removes one whose replacement is running and healthy.
- The self-update helper ran the target image, so rolling an agent back to
  a v2.16 image (or updating to any image without the helper subcommand)
  started a full Sentinel instead and the rollback never happened. The
  helper now runs the image the running instance uses.
- An agent told the hub its self-update succeeded when the helper started.
  If the swap was rolled back, the agent that comes back now sends a failed
  result for it, so the hub's history and notifications show what happened.
- A network error or a 5xx from a registry's token endpoint marked a valid
  login as rejected on the settings page. Only a 401 or 403 does, and the
  mark clears when the login works again.
- Starting a self-update no longer force-removes a container that merely
  has the helper's name, and the helper ignores SIGTERM until the swap is
  settled. The hub now sends agents cloud (ECR, ACR, GCR) registry logins
  with updates as well as stored ones.
- A host restart between the self-update helper stopping the old Sentinel
  and starting the new one left no Sentinel running. The helper now sets the
  old container's restart policy to `always` for the swap (putting the
  original back on rollback), so the old instance returns with the daemon;
  finding itself parked as `-old-<timestamp>` with a replacement that never
  started, it removes the replacement, takes its name and restart policy
  back, and an agent tells the hub.
- A self-update helper that was created but never started no longer blocks
  the cleanup of old containers for ever; after 15 minutes it is removed.
- A self-update's outcome was never reported on the instance that came back:
  the helper writes it about twenty seconds after the new instance starts, so
  the new instance saw "in progress" at boot and logged nothing, and after a
  rollback the old agent never told the hub. The instance now waits in the
  background (up to two minutes) for the final outcome and reports it once;
  an agent sends a failure to the hub at once instead of at the next
  reconnect.
- If the helper was killed during the settle period and the daemon then
  restarted, the old and the new Sentinel both came back and the new one
  crash-looped on the database lock. The replacement now has no restart
  policy until the swap is committed (it gets its real policy then, and the
  old container is set not to return), and an old instance that finds a
  running or crash-looping replacement next to it with its own unfinished
  marker and no helper alive removes it and takes its name back.
- Startup cleanup on an agent removed another Sentinel's `-old-` leftover on
  the same host; it now touches only containers named after itself. After an
  upgrade from a pre-v3 agent the old container lingered until the next
  restart; the cleanup now runs again two minutes after start.
- With two Sentinel containers on one host, a self-update picked the first
  container labelled `sentinel.self`, swapped that one, and the cleanup that
  followed removed it entirely. A Sentinel now finds the container it runs
  in by its own container ID, for the scheduled self-update, the dashboard's
  self-update and update-to-version, and an agent's self-update; if it
  cannot tell which container it is, it refuses and changes nothing. An agent
  asked to update a Sentinel container updates its own, whatever name the hub
  sent.
- The self-update no longer removes anything on the strength of the helper
  having exited: it follows the helper's outcome marker. An unfinished or
  unknown outcome leaves both containers as they are and logs it loudly.
- A Sentinel finds its own container by the ID in `/proc/self/mountinfo`, then
  in `/proc/self/cgroup` (cgroup v1 `docker/<id>`, v2 `docker-<id>.scope`),
  before falling back to the hostname. If none of these works, for example
  with a custom hostname on a runtime that hides both, set the new
  environment variable `SENTINEL_SELF_CONTAINER` to this container's name or
  ID. It is used only when the ID cannot be read from the system, and is
  checked against the Docker API: the container must exist and carry the
  `sentinel.self=true` label, or the self-update is refused with an error
  that names the variable.
- When the signature gate refused Sentinel's own self-update (enforce mode
  without cosign, or an image that fails the check), it sent no notification
  and left no history row, only a log line. A refused self-update now records
  a failed update in the history and sends one failure notification.
- Cluster agents dropped out of the cluster for good a year after they
  enrolled: an agent certificate lasts 365 days and nothing renewed it, so
  the only way back was a re-enrolment that gave the host a new ID. Once
  less than a third of its certificate's life is left, an agent now asks
  the server for a new one over its live connection and switches to it.
  The server keeps the old certificate valid until the agent connects with
  the new one, and never drops the reply. Agents ask only a server that
  says it renews safely, so a v3.0.0 agent keeps its working certificate
  (and warns) when its server is older. The server also renews its own
  certificate before it expires, so a server left running for a year
  keeps accepting agents. Agents older than v3.0.0 do not renew; update
  them before their first year is up (with "Auto-update agents" on, the
  server does this for you).
- An agent whose clock ran more than an hour behind the server could not
  connect after a server restart, and was then told to re-enrol, which
  would not have helped. Certificates are now valid from 24 hours back,
  and an expired or not-yet-valid certificate gets its own explanation
  in the agent log, pointing at the clocks where that is the cause.
- Turning Cluster Mode off while any agent was connected left the settings
  request hanging for ever, cluster mode could not be turned back on until
  a restart, and every shutdown of the server ran into Docker's stop
  timeout and was killed. The server now ends the agents' connections
  itself when it stops; agents go back to reconnecting.
- When a connected agent could not list its containers (its Docker daemon
  restarting, a socket proxy down), it sent no reply and the server's
  scheduled scan waited on that host for ever, so no host was updated
  again until a restart. The agent now always replies, with the error, and
  every call the server makes to an agent has a time limit. A server
  older than v3.0.0 shows such a host with no containers until its next
  successful list.
- A host with many containers or large labels sends a container list over
  gRPC's 4 MB default; the server refused it, so the host dropped out of
  the cluster. The limit is now 64 MB, and the server log names a host
  that goes over it.
- A network partition took about a minute and a half to notice. Both ends
  now send keepalive pings on a quiet connection and drop it after 40
  seconds without an answer. An agent pings that often only a server that
  says it accepts it; with an older server it pings every five minutes,
  which gRPC's default policy allows.
- A remote update whose new container could not be created or started (a
  bad mount, a port clash, a broken image) deleted the container. The agent
  now builds the replacement while the old container is parked under
  `<name>-sentinel-old`, removes the old one only after the new one has
  started, and puts the old one back on any failure. A `--rm` container is
  recreated from its old configuration and image.
- Remote updates could leave work for the user: a stopped
  `<name>-sentinel-old` from an interrupted update blocked later updates
  (it is now removed when the container under the real name has run;
  otherwise the update still stops and names it), a replacement that
  could not be removed kept the container's name (it is now moved aside
  to `<name>-sentinel-failed`), a failed removal of the old container was
  reported as plain success, and parked containers showed on the
  dashboard as containers of their own while their update ran. A parked
  container left behind with no update running is still listed, under
  its own name, since it may be the only copy.
- A stopped remote container that was started during its update could be
  force-removed while running. The agent now checks the container's state
  once it has parked it and only removes a container it stopped itself.
- When the server retried a slow remote update, the agent could run two
  recreates of the same container at once and leave the live container
  parked. A second update of the same container is now refused while the
  first runs, and a result that arrives after the server stopped waiting
  is saved to the history.
- Containers on several networks came back from a remote update with
  their static IP, aliases and MAC address on one network only, chosen at
  random. Every network now keeps its settings.
- Remote updates now build the replacement container exactly as local
  updates do. Before, an updated container on an agent host kept the old
  image's command, environment and labels (a new release's defaults never
  applied), kept the old container's short ID as its hostname, and
  collected old IDs in its network aliases. Anonymous volumes (an image
  VOLUME or a bare `-v /path`) were replaced by new, empty ones on every
  update, so a database without a named volume came back empty and its
  data was left in a detached volume; they are now carried over and kept
  when the old container is removed. A container sharing another's
  network stack (`network_mode: container:X`) could not be updated at all.
- Updating a container on an agent host that other containers share the
  network of (`network_mode: container:<it>`, compose `service:<it>`) cut
  those containers off: they were left with only loopback, still pointing
  at the removed container, and could not even be restarted. As on the
  server, a consumer that pointed at the old container is now recreated
  on the new one, and one that names it is restarted; the same happens
  when a failed update brings the old container back.
- On an agent host, `sentinel.remove-volumes=true` on a container, or
  `SENTINEL_REMOVE_VOLUMES=true` on the agent itself, now starts the
  updated container on fresh anonymous volumes and removes the old ones
  with the old container, as on the server. The server's own Remove
  volumes setting does not reach agents yet.
- Same-tag updates (a new image behind the tag a remote container already
  uses) always failed on the agent with "invalid reference format", both
  when approved and when applied by the scheduled scan.
- Remote updates started containers that had been stopped, or one-shot
  jobs that had exited. A stopped container now stays stopped.
- Queued updates for a remote host were deleted whenever the host was not
  connected at scan time, for example just after a server restart. They
  are now kept while the host is still in the cluster, and pruned once it
  lists without them, once it is removed, or when cluster mode is off.
  Approving a remote update whose host is gone (or with cluster mode off)
  is refused instead of being run against the local Docker host.
- A queued update for a remote Sentinel agent could never be applied: its
  row offered "Update Sentinel", which updated the server instead, and
  approving it was refused. The row now sends the update to that agent.
  Restart, stop, start, update, rollback, policy and GHCR actions also
  protect only the server's own Sentinel container now, not any remote
  container with the same name.
- Remote update history was stored under three different keys, so
  History showed a raw host ID for scheduled updates and Rollback found
  no history after an approved update. All remote history now carries the
  host; rows saved before this release are moved to the same key when the
  cluster starts. A rollback that finished after the server stopped
  waiting is saved as a rollback, so the next Rollback does not undo it.
- After an agent host crashed or lost its network and reconnected quickly,
  the dashboard showed the host offline while it was working, and remote
  actions in flight at that moment failed with "agent disconnected".
- A removed or revoked cluster host could reappear after a restart, when a
  save that was already under way wrote it back to the database. One
  slow save also no longer holds up every other host.
- "Auto-update agents" never updated an agent: it asked for an image tag
  such as `:v2.16.1`, but releases publish `:2.16.1`.
- Enrolling the same Docker daemon twice gave two hosts with the same
  containers and racing approvals, unnoticed. The server now logs a
  warning naming both hosts (it does not remove either).
- An agent whose certificate the server did not recognise (for example a
  server restored from a backup taken before the agent renewed) logged
  only a bare error. It now explains the cause and that re-enrolling is
  the fix.
- An agent killed part way through an update (a restart, a crash, a host
  reboot) could leave the container stopped, or renamed to
  `<name>-sentinel-old`, with nothing to bring it back. Agents now run
  every update through the same journalled swap as the server's own
  containers, with the journal in the agent's database: an interrupted
  update is finished or undone when the agent starts again, and retried
  every minute until it is resolved. Until then the agent refuses other
  updates of that container. One it cannot resolve on its own is
  discarded from the server, once the containers are right on the host,
  with `DELETE /api/swaps/<name>?host=<hostID>` (agents before v3.0.0
  cannot, and the server says so). After an update, an agent also restarts the
  container's dependents when dependency awareness is on, as the server
  does.
- Signature verification (enforce, warn, or a container's `sentinel.verify`
  label) applied only to the server's own containers: an update dispatched
  to an agent went ahead unchecked. The server now checks the image before
  it asks an agent to update, with the same rules, whether the update
  comes from a scan, the dashboard or the API. A rollback is not checked.
- A container on an agent that an older Sentinel had updated kept the
  environment, labels and command of the first image it ran, through every
  later update. The server now sends the images a container ran before
  (from its update history) with each update, and the agent leaves out
  whatever came from any of them that it can read, so the new container
  takes the new image's defaults. An earlier image no longer on the agent's
  host is fetched from its registry (with the credentials the server sends
  for the update); one that cannot be fetched is skipped.
- Lifecycle hooks never ran for containers on agents: neither the
  `sentinel.hook.*` labels nor the hooks saved on the server for a remote
  container. They now run around a remote update exactly as around a local
  one: the hooks the server stores for that container win over its labels,
  the pre-update hook runs in the old container before anything changes
  (exit 75 skips the update, which is then counted as skipped and leaves
  no history row; under a server before v3.0.0 the agent reports the skip
  as a success with a note, as that server would count it as a failure;
  any other failure aborts it), and the post-update hook
  runs in the new container (a failure is reported with the result). They
  follow the hooks setting, which the server now sends to its agents. A
  rollback runs no hooks.
- Hooks saved for a remote container were also listed for a local
  container whose name equals that host's ID.
- Agents never received the server's settings, so remote updates ignored
  image cleanup, image backup, lifecycle hooks and dependency awareness as
  set on the server. The server now sends them
  when an agent connects and again within a heartbeat (30 seconds) of a
  change, and agents keep the last ones received for when the server is
  away (and follow them from the start, including when they finish an
  interrupted update). Rule: a setting given in an agent's own environment
  (`SENTINEL_REMOVE_VOLUMES`, `SENTINEL_IMAGE_CLEANUP`,
  `SENTINEL_IMAGE_BACKUP`, `SENTINEL_HOOKS`, `SENTINEL_DEPS`) wins on that
  agent's host; the cluster settings page says so. Remove volumes is the
  exception: agents before v3.0.0 never removed volumes, so the server
  sends it only when the new cluster setting "Remove volumes on agents
  too" is on (off by default). Check it before turning it on: an agent's
  containers then start on new anonymous volumes and the old ones are
  deleted. Update delay, rollback
  policy and the update policy itself stay with the server, which decides
  when a remote update runs.
- Remote updates left the old image on the agent's host. Agents now remove
  it after an update when image cleanup is on, as the server does, and
  tag it as a backup first when image backup is on.
- `SENTINEL_CLUSTER_PORT` was ignored: the cluster listener used only the
  port saved in the settings (saved with every change to the cluster
  settings) or 9443. Set in the environment, it now wins, as
  `SENTINEL_CLUSTER_ENABLED` does.
- An agent started its web UI even with `SENTINEL_WEB_ENABLED=false`.
- Stopping an agent logged an error ("context canceled") and exited with
  status 1. A stopping agent now waits up to 90 seconds for commands it is
  running (an update half way through) before it closes its database, and
  exits cleanly.
- One enrolment token could enrol more than one agent when they used it
  at the same moment, and a refused second use left nothing in the
  server's log. The token is now checked and used in one step, and every
  refused token (used, expired, not matching) is logged with the host name
  and address that presented it.
- An agent restarted with a new `SENTINEL_HOST_NAME` kept its old name in
  the cluster. Agents now send their name with their state report and the
  server takes it.
- Agent versions were never stored, so the cluster page and the hosts API
  showed none, and after a server restart "Auto-update agents" waited for
  the scan after next: the version came only with an agent's first
  heartbeat. The version in an agent's state report is now stored with the
  host and used at once.
- An image pushed to more than one repository showed a same-tag update
  that was not there, again and again: Sentinel compared the digest of
  whichever repository Docker listed first with the registry of the
  container's own. It now uses the digest of the container's repository
  (agents and the server alike).
- An update that finished on an agent while its connection to the server
  was down was lost from the history: its result could not be sent, and
  the offline journal meant to carry it was never written. The agent now
  keeps such a result and sends it when it reconnects; the server records
  it in the history, in place of the "agent disconnected" failure recorded
  when the connection dropped (a late result of an update the server had
  stopped waiting for also replaces that failure, instead of adding a
  second row; and if the result arrives first, the failure is not written
  over it).
- A scan that ran while a remote update was in progress saw the old image
  and queued (or started) the update again, leaving a queue entry for an
  update that had already happened. A container whose update the server
  is waiting for is now left to it.
- A queued remote update stayed in the queue after its container became
  up to date, or was filtered out or pinned. The scan now removes it, as
  it does for a local container, and counts up-to-date remote containers.
- Remote containers did not follow the rules local containers do. A scan
  now applies, to a container on an agent: the update delay (remote auto
  updates with a delay never ran), the maintenance window and the
  `sentinel.schedule` label, pull-only (global or `sentinel.pull-only`:
  the agent pulls the image, with the server's registry credentials, and
  reports how it went, and the container is left alone; remote containers
  were recreated. An agent before v3.0.0 is asked to pull but cannot say
  how it went, so nothing is recorded), and "Ignore version" (saved for the remote
  container instead of a local container of the same name).
- Remote updates were silent: notifications are now sent when an update
  of a remote container is available, starts, succeeds or fails, named
  "container on host". A failed remote auto update is in the history with
  its error (it was missing, or recorded without the error), and the scan
  no longer sends a second, host-less event that refreshed a local row.
- Pausing a host did nothing, the cluster page's Drain button called an
  endpoint that does not exist, and a paused host could not be resumed. A
  paused host now gets no new updates from any path (a rollback still
  goes through), the button pauses it, and a Resume button ends the pause.
- Turning cluster mode off waited behind any remote update, action or log
  fetch in progress, and every cluster page request waited behind that.
- "Check for updates" on a remote container compared the server's own
  copy of the image, which it usually does not have (an error), instead of
  the digest the agent reported, and a new image behind the same tag was
  reported as "up to date (newer versions ignored)" and not queued. Both
  now behave as the scan does.
- The dashboard showed remote containers under the server's default
  policy, while the scan used the cluster's default remote policy and the
  `:latest` rule; the cluster settings showed "manual" as the remote
  default when none was saved. They now show the policy the scan applies.
- After an approved or "update to version" remote update, the dashboard
  row stayed on "Updating" until the page was reloaded.
- When the cluster CA could not be read, the server replaced it with a new
  one without a word, locking out every agent. It still creates a new one
  (the cluster cannot run without), but keeps the old files aside and logs
  how to restore them.
- An agent whose enrolment was cut short after the key was written could
  not start ("read host id"). The host ID is now written before the key,
  and an agent without the file takes it from its certificate.
- An enrolment whose answer never reached the agent used up the token, so
  the agent could not enrol again without a new one. For 10 minutes after
  a token is used, the same agent (the same key, which it now keeps until
  enrolment finishes) may enrol with it again and gets the same host; the
  server logs the retry. Any other agent is refused as before.
- Autonomous mode (while the server is unreachable) was stopped before
  every reconnect attempt and started again after it, logging "entering
  autonomous mode" every 30 seconds and never reaching its own poll. It now
  runs until the agent connects again, and counts stopped containers too.
- The server's view of an agent's containers went stale between scans (6
  hours by default): a container stopped or started by hand kept its old
  state on the dashboard. Agents now send their container list every 5
  minutes, and two lists sent close together can no longer arrive out of
  order and leave the older one in place.
- Remote updates started from the dashboard or the API (approve, "Update
  now", update to version) now send the same notifications as local ones:
  started, then succeeded or failed.
- A Rollback of a remote container from the dashboard or the API sent no
  notification. It now sends "rollback succeeded" or "rollback failed" for
  "name on host", as a local rollback does, and records one rollback row
  from the image the container ran to the one it went back to.
- An agent's update of itself was recorded as a success, and notified as
  one, as soon as its helper took over, before the swap; a self-update
  that was then rolled back added a failed row and no notification. The
  server now waits for the outcome, which the agent that comes back (the
  new version, or the old one put back) reports, and records one row and
  sends one notification with it. After 10 minutes without an outcome it
  records the update as failed: "the agent did not come back on the new
  version". Servers before v3.0.0 are told "success" as before.
- Remote rollbacks were recorded as updates (outcome success, type
  rollback), so History showed them as Updated and its Rollback filter
  left them out. They are now recorded as local ones are: rolled back, or
  failed with "rollback failed: <why>". Rows from before v3.0.0 still
  count when Rollback looks for the update to undo.
- A request to update another Sentinel container on an agent's host
  updates the agent's own container, but the history row named the
  container requested, which did not change. It now names the container
  that changed, from the image it ran.
- When an agent rolled back an update because the new container did not
  stay up, the reason was only "the new container did not stay up". It now
  says why, as a local update does: the exit code, the restart count, or
  the healthcheck's output.
- A remote update whose agent dropped its connection failed with "agent
  37770888... disconnected". Errors from a request to an agent now name
  its host.
- Saving Settings > Notifications overwrote a webhook channel's real header
  (for example `Authorization: Bearer ...`) with its masked form, so the
  channel started failing with 401. Masked values, including those inside
  webhook headers, now keep the stored secret.
- Importing a configuration exported without secrets replaced every
  notification channel's real token with a masked value and still reported
  success. The import now keeps the stored secret for channels that already
  exist, and skips (and reports) imported channels that only hold masked
  values.
- Turning on 2FA showed eight 64-character hashes as recovery codes, and none
  of them could sign in. Confirming 2FA now generates the recovery codes,
  stores their hashes and shows the plain codes once. If you turned on 2FA
  before this release, your codes are unusable: turn 2FA off and on again
  to get working ones.
- A wrong password at login reloaded the login page with no message, a wrong
  2FA code dropped you back to the username step, and a wrong current
  password on the account page navigated away. Sentinel now redirects to the
  login page only when the session is gone.
- With authentication disabled, saving Web Port, TLS mode, Log Format or
  Self-update mode, or switching role, always showed "Failed to save".
- In cluster mode, ticking an agent's container in Manage mode and choosing
  Restart, Stop or Start acted on the hub's container with the same name.
  Bulk policy changes had the same problem. Selections are now keyed by host
  and name, the confirm dialogs show the host, and a ticked row stays ticked
  only on its own host.
- Settings showed Remove volumes and Image backup as Off while the engine
  had them On from the environment, and the UI rollback ignored
  `SENTINEL_ROLLBACK_POLICY`.
- A failed notification was recorded as delivered whenever the built-in
  log record succeeded, so if the only real channel (Gotify, say) failed
  on the scan that first saw an update, Sentinel never tried again and you
  were never alerted. Only real channels now decide whether an update was
  notified about. Batched notifications still count as sent once queued;
  a failed flush is logged.
- Batched notifications could be postponed for ever: the batch window
  restarted on every new event, so a steady trickle of failures never
  flushed. The window now runs from the first queued event. The first
  retry delay was also unbounded; it is now capped at 30 seconds like
  the later ones.
- The daily digest and batched summaries named one container (the digest
  named only the alphabetically first one, a batch said "3 containers")
  on every provider but webhook. They now list every container, and MQTT
  payloads carry a `container_names` list.
- Telegram rejected failure messages whose error text held an unmatched
  `_` or `*` (a Swarm service called `stack_web`, for example), so the
  alert never arrived. The error now goes in a code span. Combined
  failure errors are capped at 1000 characters, Discord fields at 1024
  and Pushover messages at 1024, which those services reject otherwise.
- Custom notification templates saved in Settings validated and previewed
  but every real notification used the built-in text. Templates are now
  loaded at start-up and after each save or reset, and rendered by every
  provider. A template that fails to run falls back to the built-in text.
  Telegram shows a custom template as plain code text. `{{.ContainerNames}}`
  is available for digests and batched summaries.
- An SMTP server that accepted the connection and then never answered
  (port 465 with TLS left off, for example) blocked the scan for well past
  the advertised 30 seconds. The socket now carries the deadline.
- The per-container "Notify on every scan" mode behaved like the default
  and went quiet after the first alert. It now alerts on every scan that
  finds the update, and those containers are left out of the scheduled
  digest, as the Settings text says.
- With `SENTINEL_WEB_ENABLED=false` the daily digest never ran (so
  containers in digest-only mode produced no notifications at all), a
  fresh install still served the setup wizard on the web port, and after
  it finished no scan ever started. The digest now runs headless, a fresh
  headless install skips the wizard and takes the `SENTINEL_MODE` role
  (server if unset), and scans start straight away.
- The Home Assistant discovery switch in Settings only took effect after
  a restart, and a sensor that had gone ON stayed ON after its update was
  applied or dismissed. Discovery now connects and disconnects when the
  setting changes and sets sensors back to OFF when a container leaves the
  queue. After a restart Sentinel does not know which sensors it left ON,
  so a stale ON clears only the next time that container is pending.
- Home Assistant discovery gave containers whose names differ only in
  `-`, `.` or `_` the same topic and unique ID, so one hid the other. Names
  with such characters now carry a short suffix. Their Home Assistant
  entities are created again under the new ID once, and the old entities
  can be deleted in Home Assistant.
- History "Load more" repeated rows (time zones east of UTC) or skipped
  hours of records (west of UTC) from the third page on when `TZ` was not
  UTC. The cursor is now normalised to UTC before the lookup.
- Deleting a user left their passkeys behind, so the login page kept
  offering passkey sign-in with nothing that could use it. Their
  credentials and indexes are now removed with the user.
- Notification channels migrated from the pre-2.x single-config format got
  a new random ID on every read, so the first save from the UI stored the
  masked Gotify token as the real one. They keep fixed IDs now.
- Environment values that were set but could not be parsed fell back to
  the default without a word: `SENTINEL_POLL_INTERVAL=86400` became six
  hours and `SENTINEL_WEB_ENABLED=maybe` stayed on. Sentinel still starts
  with the default, but now logs an error at start-up naming the variable,
  the value it could not read and the default in use (values of tokens,
  passwords, secrets, keys and URLs with credentials are never logged).
  `yes`, `no`, `on` and `off` are accepted as booleans.
- A headless agent (`SENTINEL_WEB_ENABLED=false` with
  `SENTINEL_ENROLL_TOKEN` and `SENTINEL_SERVER_ADDR`) became a standalone
  server and never joined its hub. Those variables now select the agent
  role on a fresh start; an explicit `SENTINEL_MODE` still wins.
- If the tag moved to a new digest while every notification channel was
  down, the update was never reported: the earlier notification time was
  kept with the new digest, so the next scan thought it was announced.
  It is now retried until it is delivered.
- History retention also pruned `partial` update records, which are
  applied updates. They are kept like successful updates.
- Telegram rejected a failure whose error was over about 3,900
  characters, because the cut left its code span open. The error is now
  trimmed inside the span.
- Discord ignored custom notification templates. A saved template now
  fills the embed description.
- Saving the Home Assistant discovery settings twice in quick succession
  (on, then off) could leave discovery connected, and each save waited on
  the broker connection. Saves now hand the change to one background
  worker, so the last save always decides the final state and the page
  never waits on the broker.
- When a pre-update hook exited 75 to skip an update, "update started"
  had already gone out and nothing followed, locally or on a cluster
  host. A new "Update Skipped" notification now closes it, naming the
  container (and host) and the reason. It is on for channels that receive
  all events and can be switched per channel under the channel's event
  list; a channel that already has a saved event list needs "Update
  Skipped" ticked to get it. It also appears in the template editor.
- The scheduled digest listed far more containers than were queued (173
  against 88 in a test cluster): every remote update appeared twice, once
  by bare name, states of removed containers and hosts were never cleared,
  and remote names showed raw host IDs. Each remote update is now listed
  once as "name on host", and a scan clears the stored state of remote
  containers that are gone, whose host left the cluster, or that were
  updated outside Sentinel (a host that is only offline keeps its own).
- "Update available" notifications carried no new image, so a custom
  template's `{{.NewImage}}` rendered empty. It is now the target image
  (the newer tag, or the same tag for a digest-only update), for local,
  Swarm and remote updates.
- An update you ignored left the queue but stayed in the scheduled
  digest, so the digest could list one more than the queue held. Ignoring
  an update (from the queue, or when a scan finds every newer version
  already ignored, locally or on a cluster host) and dropping a stale
  remote queue row now also clear its stored notification state.

## [2.16.1] - 2026-10-13

### Security

- Built with Go 1.26.9. Go 1.25 left support when Go 1.27.0 shipped on
  2026-08-19, so v2.16.0 (built with go1.25.14) lacks the 2026-10-08
  standard library fixes in `net/http`, `html/template`, `net/textproto` and
  `crypto/tls`. Among them are HTTP/2 server bugs, including a crash, that a
  client can reach on the dashboard port before logging in when TLS is on
  (`SENTINEL_TLS_CERT`/`SENTINEL_TLS_KEY` or `SENTINEL_TLS_AUTO`). Go 1.26.9
  carries the same fixes as Go 1.27.2 without 1.27's platform changes, so
  macOS 12 binaries and existing `GODEBUG` settings keep working.
- Bumped `google.golang.org/grpc` v1.79.3 to v1.84.0. Two of the four
  advisories it clears (GHSA-vp52-pcj8-j9qc, heap exhaustion, and
  GHSA-hrxh-6v49-42gf, HTTP/2 rapid reset bypass) are reachable on the
  cluster gRPC port before a client certificate is checked, when cluster
  mode is on. The other two (GHSA-2v4p-qf9q-27wj, GHSA-qc2q-p7wx-3px3) affect
  only xDS servers, which Sentinel does not run. A v2.16.0 agent still
  connects to a v2.16.1 server, and the other way round.
- Bumped `golang.org/x/net` to v0.61.0, `golang.org/x/text` to v0.43.0 and
  `golang.org/x/crypto` to v0.58.0. None of their advisories is called from
  Sentinel's code; the bumps clear module-level scanner findings.
- The image now uses `alpine:3.24` (3.21 reaches end of support on
  2026-11-01) and runs `apk upgrade` at build time, so OpenSSL and zlib carry
  their latest security revisions. The binary is static (`CGO_ENABLED=0`) and
  links neither.

### Fixed

- A pull that failed after the daemon started streaming progress (a layer
  download or extract error, a dropped connection, a full disk) was treated
  as a success, because Docker client v0.2.2 ignored errors reported inside
  the stream. An unchanged tag was then recorded as identical and its digest
  cached, hiding the update until the remote digest changed again. Updated
  the Docker client to v0.6.2, which returns the error.

### Changed

- Sentinel now checks the Docker Engine API version at startup and exits
  with a clear message below API 1.44 (Docker Engine 25.0). Older engines
  never worked: the previous client sent every request at a newer API
  version, which they rejected. The new client would talk to them, but they
  refuse to create a container on several networks at once, which would
  leave such a container removed after a failed update.
- Building from source needs Go 1.26 or later (`go.mod` asks for toolchain
  go1.26.9, which `GOTOOLCHAIN=auto` downloads).
- CI: golangci-lint v2.14.0, GitHub Actions moved off the removed Node 20
  runtime, every Go build pinned to the toolchain in `go.mod`, and a
  govulncheck job on every push plus a weekly scheduled run on GitHub.

## [2.16.0] - 2026-10-12

### Changed

- A failing pre-update hook now aborts the update, as the Lifecycle Hooks
  wiki page documents. Exit 0 continues, exit 75 skips the update, and any
  other exit code, an exec error or a timeout stops it before the image
  backup and pull and clears the maintenance flag. An aborted update is
  recorded as failed in the container's update history and sends the
  `update_failed` notification, as a failed validation does. A skip with exit
  75 records nothing and sends no failure notification. Previously the failure
  was logged as a warning and the update went ahead. Post-update hook
  failures are still only logged (GitHub #86).

### Fixed

- Lifecycle hooks set with the `sentinel.hook.pre-update`,
  `sentinel.hook.post-update` and `sentinel.hook.timeout` labels now run.
  Only hooks saved through the REST API were ever read, so label hooks were
  silently ignored. A hook saved through the API still wins for its phase.
  Sentinel now logs each hook it runs with its phase and source (label or
  store), logs at Debug when a container has none, and warns when a hook
  label has an empty value (the compose list form without `=value`) or the
  timeout label is not a whole number of seconds (GitHub #86).
- `SENTINEL_HOOKS_WRITE_LABELS` (Settings > Write hook labels) was saved but
  never used. It now writes a container's API-saved hooks onto the container
  Sentinel creates during an update, as `sentinel.hook.*` labels
  (GitHub #86).
- A hook that outlives its timeout no longer holds up the update: Sentinel
  stops waiting when the timeout expires. The command itself is not killed
  and may keep running inside the container (GitHub #86).
- `POST /api/hooks/{container}` accepts `command` as a shell string, run with
  `/bin/sh -c` like a label, as well as an array of arguments. The
  documented string form returned 400 (GitHub #86).
- The Settings page shows the value in effect for Lifecycle hooks, Write
  hook labels, Image cleanup, Dependency-aware updates and Auto-update
  :latest containers. It showed only a value saved in the UI, so an env var
  such as `SENTINEL_HOOKS=true` appeared as Off until the toggle was saved
  (GitHub #86).

## [2.15.3] - 2026-07-15

### Security

- Bumped `golang.org/x/crypto` v0.51.0 to v0.52.0, clearing the July 2026
  batch of 13 advisories (CVE-2026-39827 through CVE-2026-46598) in the
  `x/crypto/ssh` package. Sentinel does not import the SSH package -- only
  `bcrypt`, `blowfish`, and `ocsp` are compiled into the binary -- so none of
  the vulnerable code paths are reachable and this is a precautionary bump to
  clear the module-level alerts.

### Fixed

- Fixed a flaky scheduler test (`TestMaybeSelfUpdate_AutoModeWhenIdle`) that
  raced the self-update goroutine's failure re-queue on slow runners; it now
  asserts the atomic trigger flag instead of queue length. Test-only change,
  no runtime behaviour affected.
- Disabled the golangci-lint analysis cache in CI (`skip-cache: true`): a
  cache entry restored across the x/crypto bump poisoned staticcheck's
  cross-package facts, producing false SA5011 nil-dereference findings in
  test files guarded by `t.Fatal`.

## [2.15.2] - 2026-07-14

### Security

- The cluster gRPC server now disables TLS session resumption
  (`SessionTicketsDisabled`). Session resumption skips `VerifyPeerCertificate`,
  so a revoked agent certificate could have reconnected without hitting the
  CRL check for the lifetime of a session ticket. Agent connections are
  long-lived, so the extra handshake cost is negligible. Found while triaging
  gosec G123; the v2.15.1 spot-check misread this finding as being about the
  documented `VerifyClientCertIfGiven` enrollment design.
  ([#85](https://github.com/Will-Luck/Docker-Sentinel/issues/85))

### Fixed

- MQTT notifier QoS bounds check now runs before the int-to-byte conversion.
  Previously a QoS value above 255 wrapped at the byte boundary (257 became
  QoS 1) and slipped past the guard; out-of-range values now always fall back
  to QoS 0. Found while triaging gosec G115.
  ([#85](https://github.com/Will-Luck/Docker-Sentinel/issues/85))

### Changed

- Completed the gosec analyzer triage deferred at v2.15.1. G115 and G123 are
  fixed in code (above). G117 (credential persistence and masked-secret
  marshaling), G703 (compose paths sourced from Docker labels), and G705
  (embedded static assets) carry targeted `#nosec` annotations at the seven
  flagged sites. Only G118 (fire-and-forget update goroutines that must
  outlive the request) and G124 (deploy-dependent cookie `Secure` flag,
  CSRF double-submit cookie) remain globally excluded, each with a documented
  rationale in `.golangci.yml`. The blanket gosec exclusion for test files
  narrowed to G101/G704 (fixture tokens and test-server requests).
  ([#85](https://github.com/Will-Luck/Docker-Sentinel/issues/85))

## [2.15.1] - 2026-07-09

### Security
- Bumped `golang.org/x/net` v0.49.0 to v0.55.0, clearing GHSA-5cv4-jp36-h3mw (Go HTML parser denial of service). The vulnerable package is an indirect dependency and the HTML parser is not on any Sentinel code path, so this is a precautionary bump. Transitive `golang.org/x/*` modules (crypto, sync, sys, text) moved forward with it.

### Changed
- Toolchain raised to Go 1.25 (required by x/net v0.55.0): go.mod, CI workflows, and the Docker builder image now use Go 1.25.
- golangci-lint upgraded v1.64.8 to v2.12.2 (v1 cannot lint go 1.25 modules). The `.golangci.yml` was migrated to the v2 format at v1 parity: same effective linter set (gosimple is now part of staticcheck, gofmt runs as a formatter), with v1's default exclusions restored via presets. gosec analyzers introduced after v1.64.8 (G115, G117, G118, G123, G124, G703, G705) are excluded pending triage; the flagged sites were spot-checked as false positives (bounds-guarded conversion, documented TLS enrollment design, deploy-dependent cookie Secure flag, static asset writes).

### Changed
- Version comparison is now variant-aware. A bare tag (`nginx:1.24.0`) only competes against bare and pre-release tags, and a variant tag (`1.24.0-alpine`) only against tags with the same variant suffix. Previously every flavour an image publishes (`-alpine`, `-perl`, `-bookworm`, `-otel`, ...) counted as a distinct higher version, inflating the Available Versions list and the beyond-scope hint count by a variant multiplier -- `nginx:1.24.0` reported "499 beyond scope" where the real number of higher bare version lines was a handful. Pre-release suffixes (`-rc1`, `-beta.2`, `-dev`, ...) still participate in version ordering, so a container on `2.0.0-rc1` continues to see `2.0.0-rc2` and the final `2.0.0` as updates. ([#84](https://github.com/Will-Luck/Docker-Sentinel/issues/84))

## [2.14.1] - 2026-06-01

### Security
- Bumped `go.opentelemetry.io/otel` (and `otel/metric`, `otel/trace`) to v1.41.0 to clear CVE-2026-29181 / GHSA-mh2q-q3fh-2475 (multi-value baggage header extraction, a DoS amplification). The package is a transitive dependency and the vulnerable path is not reachable in Sentinel (no otel propagator or baggage extraction is wired), so this is a precautionary bump. ([#81](https://github.com/Will-Luck/Docker-Sentinel/pull/81))

## [2.14.0] - 2026-06-01

### Added
- The container detail page now shows a hint when the Version Scope is hiding higher releases, instead of rendering an empty "Available Versions" section. Under the relaxed default scope a 3-part semver tag only counts higher same-minor patches as updates, so a container whose minor line has no further patches (e.g. `nginx:1.24.0`, or an app pinned to an older minor) would silently show nothing even though newer minors and majors exist upstream. The section now reads "N higher version(s) exist on the registry beyond your current Version Scope" and points to the per-container `sentinel.semver=minor` label (or a floating tag) to surface them. Backed by a new `NewerVersionsScopedWithBeyond` helper and a `CheckResult.HigherVersionsBeyondScope` count. ([#83](https://github.com/Will-Luck/Docker-Sentinel/issues/83))

## [2.13.2] - 2026-05-15

### Fixed
- `Checker.Check` no longer masks DistributionDigest errors as `IsLocal=true` for registry-prefixed images. Bare names (e.g. `myapp:v1` — no slash, no dot) still fall through silently as before, but images with a registry prefix (`ghcr.io/owner/app`, `library/nginx`, `localhost:5000/repo:v1`) now surface their registry failures via `result.Error` so the scan loop's existing error branch reports them at Warn instead of incrementing `Skipped` silently. Same false-negative class as discussion [#82](https://github.com/Will-Luck/Docker-Sentinel/discussions/82) but on the digest path: PAT rotations, DNS blips, and 5xx storms no longer turn into invisible "no update" reports. Tag-stripping is slash-aware so `host:port/repo:tag` refs are not misclassified as bare names.
- `finaliseContainer` now nil-checks `inspect.HostConfig` before passing it to `CreateContainer`. Previously a malformed daemon response (container deleted between stop and inspect, or transient daemon glitch) would silently recreate the container with a default host config — losing port bindings, mounts, restart policy, and capabilities. The check returns a clean `finaliseError{stage: "inspect"}` instead.
- All six `apiUpdate`/`apiUpdateVersion`/`apiSelfUpdate` background goroutines now wrap their bodies in `defer recover()` blocks. Previously a nil-deref in the cluster adapter, Portainer client, or self-updater would take the whole web process down because these goroutines ran detached with `context.Background()` and no recovery. Panics are now logged via `s.deps.Log.Error` and (for `apiSelfUpdate`) an SSE `EventContainerUpdate` is published so the dashboard surfaces the failure instead of hanging on a stale "Updating" badge.

### Changed
- Dashboard no longer hard-reloads on SSE reconnect. The previous `window.location.reload()` on each `connected` event after a transient network blip was wiping expanded host groups, scroll position, selection, and unsaved input — and on a flapping connection could reload-loop. Replaced with a 500 ms debounced, 6-way concurrency-capped catch-up that re-fetches container rows via `updateContainerRow`. Per-event handlers (`container_update`, `container_state`, `cluster_host`) keep state in sync without nuking the page.

### Build / CI
- Pinned `golangci-lint` from `@latest` to `v1.64.8` in `.gitea/workflows/ci.yml` to match the GitHub workflow. Removes the "Gitea build fails on Monday after Friday's golangci-lint release" flake.
- Pinned `esbuild` from `@latest` to `v0.24.2` in the Dockerfile, both CI YAMLs, and the Makefile `$(ESBUILD)` rule. Frontend bundle hashes are now reproducible across rebuilds.
- `make dev-deploy` SSH key hygiene: the file-scope `DEV_SSH_KEY := $(shell mktemp)` was running at parse time on every Make target (build, test, lint, clean), leaking empty `/tmp/tmp.*` files. The recipe also relied on a separate `@rm -f` step that did not run on failure, leaving 600-perm 1Password-sourced SSH private keys on disk until reboot. Now wrapped in a single recipe shell with `trap 'rm -f "$$TMP_KEY"' EXIT`.

## [2.13.1] - 2026-05-09

### Fixed
- Floating-tag detection now treats less-specific tags as equivalent to their more-specific children. A container running `2026.4` no longer permanently flags `2026.4.x` patches as "newer versions"; only `2026.5.x` and beyond count. Same logic for 1-part tags like `nginx:1`. ([#82](https://github.com/Will-Luck/Docker-Sentinel/discussions/82))
- `NewerVersions` no longer lists semantically equivalent versions as separate entries (e.g. `2026.5` and `2026.5.0` collapse to a single entry, keeping the more-specific representation). Pre-release suffixes remain distinct.
- Reworded "Auto" policy description to reference the Version Scope setting that actually controls cross-version updates.
- Reworded "Pinned" policy description to clarify that update checks and notifications still run; only the queue/update step is skipped.

## [2.13.0] - 2026-04-10

### Security
- **Bumped `google.golang.org/grpc` 1.79.1 → 1.79.3** to close
  [CVE-2026-33186](https://github.com/advisories/GHSA-p77j-4mvh-x3m3) (gRPC-Go
  authorization bypass via missing leading slash in HTTP/2 `:path`).
  Docker-Sentinel's cluster gRPC server uses mTLS with no path-based authz
  interceptor, so the bypass has nothing to bypass — upgrade is for
  supply-chain hygiene.
- **Bumped `github.com/go-jose/go-jose/v4` 4.1.3 → 4.1.4** (indirect, via
  `coreos/go-oidc/v3`) to close
  [CVE-2026-34986](https://github.com/advisories/GHSA-78h2-9frx-2jm8) (panic in
  JWE decryption with KW algorithms and empty `encrypted_key`). Sentinel's
  OIDC flow uses JWS-signed ID tokens, not JWE, so the exploit path is low
  practical risk.
- **Webhook secret is header-only.** `POST /api/webhook` now requires
  `X-Webhook-Secret`; the query-string fallback (`?secret=...`) has been
  removed because proxies and access logs record URLs verbatim, leaking
  the secret into log aggregators and browser history. Webhook clients
  that previously used `?secret=` must migrate to the header.
- **Atom feed accepts `Authorization: Bearer` and never echoes its
  token.** `GET /api/history/feed` now prefers the `Authorization`
  header for credentials; the legacy `?token=` query parameter still
  works but the rendered feed's `<link rel="self">` is now a token-free
  URL so cached feed bodies no longer leak credentials to other
  subscribers.
- **OIDC login uses nonce + PKCE.** The OIDC authorization request
  now includes a 32-byte nonce and an S256 PKCE challenge; the
  callback verifies the ID token's `nonce` claim matches (constant
  time) and sends the `code_verifier` on the token exchange. This
  closes two standard OAuth 2.1 replay classes: ID token replay
  across sessions and code interception. All three OIDC transient
  secrets (state/nonce/verifier) are stored in short-lived HttpOnly
  cookies and cleared on every callback exit path.
- **Logout is POST-only and CSRF-protected.** `POST /logout` is now
  wrapped by the `authed()` middleware chain so the existing
  double-submit CSRF token on HTML form submissions is validated.
  `GET /logout` has been removed; the previous handler allowed any
  cross-site `<img src="/logout">` to force a sign-out.
- **OIDC callback error redirect is a fixed slug.** The callback no
  longer reflects IdP-controlled error strings or Go `err.Error()`
  text into `/login?error=...`; it now redirects to
  `/login?error=sso_failed` and logs the full detail server-side.
  This eliminates a reflected-XSS vector if the login template ever
  rendered `{{.Error}}` in an unsafe context.

### Fixed
- **Graceful shutdown timeout.** `srv.Shutdown` now uses a 10-second
  bounded context instead of `context.Background()`. The unbounded
  version could hang indefinitely on SIGTERM if any SSE client was
  connected, because `http.Server.Shutdown` waits for all active
  connections to drain but does not cancel request contexts.
- **NPM client body close.** Replaced `defer resp.Body.Close()` inside
  the retry loop with an immediate close before return.
- **Frontend `authFetchJSON` helper.** New wrapper in `auth.js` checks
  `r.ok` before parsing JSON. SSE `updateContainerRow` (the highest-
  frequency dashboard fetch) now rejects on non-2xx instead of
  silently parsing error bodies as data.

### Documentation
- **CSP comment corrected.** The `securityHeaders` comment previously
  attributed `'unsafe-inline'` to htmx; in reality htmx isn't using
  `hx-on*` attributes anywhere in the templates — the real reason
  is ~160 inline `onclick=` handlers and ~180 inline `style=`
  attributes. Updated the comment to describe the real constraint
  and reference the follow-up hardening issue.

## [2.12.0] - 2026-03-13

### Added
- **Source deduplication.** When the same Docker host is reachable via multiple
  sources (local socket, cluster agent, Portainer connector), Sentinel now
  detects the overlap using Docker Engine IDs and auto-blocks the lower-priority
  source. Priority order: local > cluster agent > Portainer. Blocked endpoints
  show the overlap reason on the Connectors page with a "Force Enable" button
  for user overrides. Cluster agents report their Engine ID in the StateReport
  proto; Portainer endpoints are probed via the Docker info API on Test
  Connection. An SSE `source_overlap` event notifies the dashboard in real time.
- **Multi-instance Portainer support.** Connect to multiple Portainer servers,
  each with per-endpoint enable/disable toggles. Local Docker socket endpoints
  are auto-detected and blocked to prevent duplicate monitoring.
- **Portainer containers on dashboard.** Portainer endpoint containers appear
  as host groups on the dashboard, with full policy, queue, severity, and
  maintenance support (same as cluster hosts).
- **Connectors page redesigned.** Portainer tab now shows instance cards with
  add/remove/test/configure workflow, replacing the single URL+token form.
- **Portainer self-update via portainer-updater.** Portainer containers detected
  by Sentinel can now be safely updated without crashing the Portainer API.
  Uses the official `portainer/portainer-updater` helper container, which mounts
  the Docker socket directly and survives the Portainer stop/recreate cycle.
  Works for both queue approvals and manual "update to version" actions.

### Improved
- **Agent CA mismatch detection.** When the server's TLS CA has changed (volume
  deleted, host migration), agents now detect the `x509` certificate error and
  log a single clear ERROR message with fix steps: stop the agent, delete the
  cluster data directory, and re-enroll with a fresh token. Subsequent reconnect
  attempts log only at WARN level to avoid flooding.
- **Cluster page troubleshooting for all disconnected hosts.** The troubleshoot
  section now appears for every disconnected host, not just those with a known
  disconnect category. A new generic fallback covers the three common causes:
  agent not running, network issue, and CA certificate mismatch after server
  volume recreation.

### Fixed
- **Stopped swarm tasks vanish on page refresh (#65).** When a swarm service is
  scaled to 0, Docker removes all tasks within seconds. On page refresh, the
  server-side template received no task data and showed a generic "Service
  scaled to 0" placeholder instead of the actual task rows with node names and
  SHUTDOWN badges. Added an in-memory task cache to `swarmAdapter` that
  preserves the last-seen running tasks per service. When a service has 0
  desired replicas and Docker returns no tasks, the cache serves them with
  state overridden to "shutdown". The JS-side `_svcTaskCache` still handles
  the instant UI update during scale-down; the server cache handles persistence
  across page refreshes.
- **Swarm service table shrinks when services stopped (#64).** The server-side
  "Service scaled to 0" placeholder row had `colspan="6"` after an empty
  checkbox cell, creating a 7-column total in a 6-column `table-layout:fixed`
  table. This forced the browser to redistribute column widths, visibly
  shrinking the table whenever any swarm service was at 0 replicas. Changed to
  `colspan="5"` (1 + 5 = 6). Also fixed JS-created task rows that had the wrong
  cell count and missing `col-*` classes for column alignment.
- **Stopped containers hidden by default on dashboard (#63).** Fresh databases
  had no `show_stopped` setting in BoltDB, but `LoadSetting` returns an empty
  string with no error for missing keys. The handler treated this as `false`,
  hiding all non-running containers. Now only an explicit user preference
  overrides the default (show all).
- **Portainer runtime scanner creation.** Adding a Portainer instance via the
  UI now creates a live scanner immediately. Previously, instances added after
  boot had no scanner until the next restart.
- **Portainer TLS verification.** Portainer connections now skip TLS certificate
  verification, fixing failures with self-signed certs (standard in homelab and
  private network setups).
- **Connectors page CSRF token.** Fixed `csrfToken` function reference being
  passed as a header value instead of being called, which broke all fetch
  requests on the connectors page.
- **Local socket endpoints not blocked.** `IsLocalSocket()` was defined but
  never called. Now auto-blocks local Docker socket endpoints during Test
  Connection and excludes them from scanning.
- **Smart local socket blocking.** `unix://` endpoints are only auto-blocked
  when the Portainer instance runs on the same host as Sentinel. Previously
  all `unix://` endpoints were blocked, which incorrectly disabled remote
  Portainer instances whose Docker sockets are valid monitoring targets.
- **Runtime Portainer instances not scanned.** Portainer instances added via
  the UI after boot were saved to the store and had scanners for API calls,
  but were invisible to the scan engine. Now `ConnectInstance` registers
  with both the web layer and the engine, so runtime-added instances are
  scanned immediately. Endpoint config changes (enabled/blocked) are also
  synced to the engine without requiring a restart.
- **Queue approval for Portainer containers.** Approving a Portainer-managed
  container from the Pending Updates queue previously fell through to the
  local Docker daemon (which can't reach remote containers). Now routes
  through the Portainer API, with Portainer image detection for self-update.
- **NPM resolver cross-host port shadowing.** When multiple NPM proxy hosts
  forwarded the same port to different IPs (e.g. port 8080 on both .57 and
  .64), the resolver matched the lowest NPM ID regardless of which host the
  container actually ran on. Replaced the single `SENTINEL_HOST` string match
  with auto-detection of local network addresses via `net.InterfaceAddrs()`,
  hostname, and `host.docker.internal` DNS. `SENTINEL_HOST` is still honoured
  as an additive override for containerised deployments where bridge networking
  hides the host's LAN IP.
- **Failed approvals missing from history.** When an approved update failed
  (e.g. Portainer agent disconnected, network error), only a log line was
  written. The update vanished from the queue with no history record. Now
  records a "failed" entry with the error message and elapsed duration.
- **History page scan summary display.** Scan summary rows were rendered as
  regular container rows, causing truncated text in the Container column and
  broken `/container/history-0` URLs when clicked. Now render with colspan
  spanning Container + Version columns, non-clickable, with proper text
  wrapping and column alignment at all viewport widths.
- **Dashboard table squashed when Swarm Services present (#62).** Swarm
  service rows had a phantom 7th `col-actions` cell that regular container
  rows and the thead did not. With `table-layout: fixed`, this created an
  invisible extra column that stole ~300px of width, pushing the entire table
  left and preventing dividers from spanning the full UI width. Removed the
  unused cells so all rows consistently have 6 columns.
- **Host-group dividers not spanning full width (#62).** The `tbody
  tr:last-child { border-bottom: none }` rule removed the bottom border from
  each host-group's last row, so consecutive host groups had no visible
  separator. Added a more-specific `.host-group tr:last-child` override.
  Also moved the Swarm Services section-divider border from the inner div
  to the `<td>` so it spans the full table width in `border-collapse` mode.
- **Dashboard stuck on "Updating" after queue approval.** Approving updates
  from the Pending Updates page and navigating to the Dashboard could leave
  containers showing "Updating" indefinitely. The update completed and
  cleared the maintenance flag, but the SSE event was published before the
  Dashboard's EventSource connection was established -- a classic missed-event
  race. Added a catch-up fetch on SSE connect: the Dashboard now scans for
  any rows still showing "Updating" and re-fetches their current state from
  the server, picking up the cleared maintenance flag.
- **Images page column alignment.** Size, Status, and Actions columns were
  left-aligned while their content (numbers, badges, buttons) sat off-centre.
  Size is now right-aligned, Status and Actions are centred. Unused badge
  changed from grey to red for better visibility.
- **Portainer connector: hot-reload without restart.** Saving Portainer URL
  and API token in the UI now takes effect immediately. Previously the test
  button always returned "not configured" because the provider was only
  created at startup. Uses the same factory pattern as the NPM connector.
- **Portainer connector: stale credentials after token change.** The connection
  test now always recreates the provider from current DB settings, so changing
  the URL or token and re-testing uses the new values instead of the ones
  from startup.
- **Portainer duplicate queue entries.** When a Portainer endpoint pointed at
  the same Docker socket Sentinel runs on, every container was scanned twice
  (once locally, once via Portainer), creating duplicate queue entries. The
  Portainer scan now collects local container IDs and skips any Portainer
  container whose Docker ID matches a locally-monitored container.
- **Portainer API token help text.** Corrected the path from "Settings > Users >
  Access tokens" to "My account > Access tokens".
- **Portainer container detail page 404.** Clicking a Portainer container in
  the pending updates queue returned "Container not found" because the detail
  handler only knew about local and cluster containers. Now resolves Portainer
  containers via the Portainer API.
- **Dashboard stat card count mismatch.** The "Updates Pending" stat card only
  counted local containers, while the nav badge counted all queue items
  including Portainer. Both now use the full queue length. Removed the
  redundant checkmark icon from the zero-state.
- **Filter bar missing bottom border.** The filter bar on multiple pages lacked
  a visual divider separating it from the table content below.
- **Remote container history/snapshot key mismatch.** History and snapshot
  lookups for cluster and Portainer containers used the plain container name
  instead of the scoped `hostID::name` key, returning empty results.
- **NPM port URLs for all containers.** NPM URL resolution matched every
  container against Sentinel's own domain when no `SENTINEL_HOST` was set,
  causing all containers to show the same proxy URL.
- **NPM wildcard domain resolution.** Proxy hosts with wildcard domains
  (e.g. `*.s3.garage.example.com`) produced broken URLs. Now skips wildcard
  entries and picks the first non-wildcard domain.
- **Data race in `Scan()` on `portainerInstances`.** The prune loop and length
  guard in `Scan()` read `portainerInstances` without holding `portainerMu`,
  while HTTP handlers mutate the slice concurrently via `Add`/`Remove`. The
  prune loop took pointers into the slice, risking dangling pointers if a
  concurrent append reallocated the backing array. Now snapshots under `RLock`,
  matching the pattern already used in `scanPortainerInstances`.
- **Portainer endpoint `ForceAllow` and `EngineID` lost on save.** The store
  adapter conversion between `web.EndpointCfg` and `store.EndpointConfig`
  only copied `Enabled`, `Blocked`, and `Reason`, silently dropping `EngineID`
  and `ForceAllow`. This caused manually force-allowed endpoints to revert to
  blocked on the next test/reconnect, and Engine ID deduplication to fail after
  a restart.
- **Endpoint existence check fragile.** Used `cfg == (EndpointCfg{})` to detect
  new endpoints, which would fail at compile time if a non-comparable field
  (slice, map) were added. Replaced with the map `ok` idiom.

### Changed
- **Queue/history key format.** Portainer HostIDs changed from `portainer:N`
  to `portainer:instanceID:N`. Existing queue and history entries are
  automatically migrated on first boot.
- **Portainer settings storage.** Old flat settings (`portainer_url`,
  `portainer_token`, `portainer_enabled`) are migrated to a structured
  `portainer_instances` BoltDB bucket on first boot. The migration is
  idempotent and safe to re-run.
- **Portainer integration descriptions.** Updated the vague "view endpoints"
  description to accurately explain that Sentinel scans Portainer endpoints
  for updates, applies policies, and can redeploy stacks or update standalone
  containers via Portainer's API.

## [2.11.1] - 2026-03-10

### Fixed
- **Cluster agent: multi-network containers failed to recreate.** Docker API
  1.44+ (Engine 26.0) rejects container creation with multiple network
  endpoints. The agent was passing all networks in a single `CreateContainer`
  call, causing `"conflicting options: cannot attach network to more than 1
  network endpoint"` for any container on 2+ non-bridge networks. Now uses
  the same single-primary + `NetworkConnect` pattern as the standalone engine.
- **Cluster agent: BoltDB lock not released on gRPC stream failure.** After a
  successful self-update, the old container stop (which releases the BoltDB
  file lock) was conditional on the gRPC result send succeeding. If the stream
  broke during send, the new container could never open the database. The old
  container stop now uses `defer` and `context.Background()` to guarantee
  execution regardless of stream health.
- **Cluster agent: non-deterministic network skip in self-update.** The extra
  network connect loop re-iterated Go maps non-deterministically, potentially
  skipping the wrong network. Now uses a deterministic `networkPlan` that
  selects the primary once before `CreateContainer`.

## [2.11.0] - 2026-03-10

### Changed
- **Self-update rewritten: rename-before-replace pattern.** The old self-update
  spawned a helper container that ran a shell script to pull/stop/rm/run. This
  was fragile: the helper had to reconstruct all `docker run` flags, and the
  stop/rm/run sequence created a window where Sentinel was completely down.
  The new approach renames the running container out of the way, creates the
  replacement with the original name, and starts it. If anything fails, the
  rename is rolled back. The old container is stopped only after the new one
  is confirmed running and has reported success. This eliminates the helper
  container entirely, preserves all container configuration from the Docker
  inspect output, and reduces downtime from seconds to milliseconds. (#72)

### Fixed
- **Self-update left old container running (BoltDB lock):** After a successful
  rename-before-replace self-update, the old container kept running and held
  the BoltDB file lock on the shared data volume. The new container could not
  open the database. The old container is now stopped after the update result
  is sent, releasing the lock for the replacement. (#72)
- **Self-update failed for local/dev images:** Pull failures are now tolerated
  when the target image already exists locally, so self-updates work in
  environments without registry access.

## [2.10.3] - 2026-03-10

### Fixed
- **Dashboard action icons clipped off screen:** Removed the redundant row action
  icons (view details, check for updates, view logs) that overflowed the table on
  narrow viewports. Clicking the row already navigates to the container detail page
  where all actions are available. (#60)
- **"View Logs" link did not show logs:** Added URL hash fragment handling so
  navigating to `/container/{name}#logs` auto-opens the Container Logs accordion
  and scrolls it into view. Works for any accordion section (e.g. `#policy`,
  `#history`). (#60)

### Changed
- **Inline release notes in queue:** Pending updates now show the upstream release
  notes body inline below the changelog link, rendered in a scrollable pre-formatted
  block. Previously only a link was shown.

## [2.10.2] - 2026-03-10

### Fixed
- **Repeated "Image Identical" results for multi-arch images:** The digest
  equivalence cache in the image ID guard was storing a useless self-referential
  pair for mutable tags like `:latest`. Images like `eclipse-mosquitto` that
  have differing repo vs manifest list digests would trigger a pull on every
  scan cycle even though the image content never changed. The cache now stores
  the correct digest pair (`ImageDigest` vs `DistributionDigest`) so subsequent
  scans skip the false positive after one confirmation pull. (#63)

## [2.10.1] - 2026-03-09

### Changed
- **Clearer history outcome labels:** Renamed all outcome badges for clarity:
  `Success` → `Updated`, `Rollback` → `Rolled Back`, `No Change` → `Image Identical`,
  `Warning` → `Updated (partial)`, `Dry Run` → `Simulated`, `Pull Only` → `Pulled`.
  Split `Skipped` into `Rate Limited` and `Check Failed`.
- **Outcome tooltips:** Every history badge now has a hover tooltip explaining what
  the outcome means in plain English.
- **Scan summary row:** Each scan writes a summary line to history showing the full
  breakdown (checked, up to date, updated, queued, skipped, failed).
- **"Identical" filter pill:** New filter button on the history page to show/hide
  image-identical entries.

## [2.10.0] - 2026-03-09

### Added
- **Real-time container log streaming:** Live log tailing via SSE with Follow
  mode that survives container restarts and stream disconnects. Includes 30s
  heartbeat to survive nginx proxy timeouts.
- **Container control actions:** Stop, start, and restart buttons in the
  container detail logs toolbar.
- **Auto self-update mode:** New setting that triggers Sentinel self-update
  automatically after each scan when no other updates are in progress.
  Configurable as a global persistent setting in the UI.
- **OIDC group/role mapping:** Map IdP group claims to Sentinel roles so admins
  don't manually assign roles to OIDC users. Configurable group claim name,
  priority-based role resolution (admin > operator > viewer), and settings UI.
- **Notification batching:** Buffer rapid-fire notifications during bulk updates
  and send a single summary instead of N individual alerts. Configurable batch
  window duration (default: disabled for backwards compatibility).
- **Scheduled backups:** Automatic BoltDB backups on a cron schedule with
  configurable retention, optional S3-compatible export (stdlib HTTP + SigV4),
  and a manual trigger button in the UI.
- **Cloud registry auth (ECR/ACR/GCR):** Native authentication for AWS ECR,
  Azure ACR, and Google GCR/Artifact Registry with automatic token refresh and
  cached credentials. Pure HTTP implementations with no cloud SDK dependencies.
- **Compose file awareness:** Parse Docker Compose files to discover service
  relationships instead of requiring `sentinel.depends-on` labels. Auto-discovery
  from container labels, with label-based deps taking priority on conflict.
- **Image signature verification:** Verify image signatures via Cosign/Sigstore
  before deployment. Enforce, warn, or disable modes with per-container overrides
  via the `sentinel.verify` label.
- **Security scanning (Trivy):** Scan images for vulnerabilities before or after
  updates with configurable severity thresholds. Pre-update mode blocks deploys
  that exceed the threshold.
- **Styled confirmation dialogs:** All 8 bare `confirm()` calls replaced with
  `showConfirm()` modal, including a red danger variant for destructive actions.
- **Enhanced empty states:** Contextual SVG icons and call-to-action buttons on
  all empty-data pages (queue, history, cluster, images, portainer, logs).
- **`apiFetch()` utility:** Generalised fetch wrapper with automatic button
  loading spinners, JSON handling, and toast notifications. Replaces raw `fetch()`
  in images and queue modules.
- **Responsive hamburger navigation:** Collapsible mobile nav at 768px with
  hamburger toggle, Escape/outside-click dismiss, and aria-expanded support.
  Added to all 13 page templates.
- **Scan progress bar:** Real-time 3px accent bar below stat cards shows scan
  progress via new `scan_start` and `scan_progress` SSE events from the engine.
- **Container log viewer redesign:** Line-by-line rendering with level-based
  colouring (error/warn/info/debug), timestamp extraction, filter input, and
  auto-scroll toggle. Replaces raw `<pre>` block.
- **Cluster journal replay (Phase 6):** Offline journal entries from cluster
  agents are now persisted to the history store with full field mapping and
  enriched SSE events. Previously entries were logged but not recorded.
- **Scanner/verifier wiring:** Trivy vulnerability scanning and Cosign signature
  verification are now called during the update flow. Pre-update scan blocks
  deploys exceeding the severity threshold; enforce mode rejects unsigned images.
  Settings API endpoints and UI accordion sections added for configuration.
- **Queue CSV/JSON export:** Export pending updates from the queue page as CSV
  or JSON, matching the existing history export pattern.
- **Health check endpoints:** `/healthz` (liveness) and `/readyz` (readiness)
  probes for Kubernetes and load balancer integration. Readiness checks DB and
  Docker socket connectivity. Both endpoints skip authentication.
- **Queue keyboard shortcuts:** `j`/`k` navigation, `a`/`r`/`i` for
  approve/reject/ignore, Enter to toggle details, `?` for help overlay.
- **Atom feed for update history:** Subscribe to updates via Atom 1.0 feed at
  `/api/history/feed?token=xxx`. Authenticated via API token in query parameter.
  Auto-discovery link on the history page.
- **Bulk container actions:** Restart, stop, and start buttons in manage mode
  bulk bar. Sequential execution with 200ms stagger, progress counter, and
  summary toast on completion.
- **Notification retry with backoff:** Configurable retry (0-3 attempts) with
  exponential backoff for all notification providers. Initial backoff and max
  retries configurable in the Notifications settings tab.

### Changed
- **BoltDB nil-bucket safety:** All `tx.Bucket()` calls across the persistence
  layer (bolt.go, bolt_auth.go, bolt_hooks.go, bolt_registry.go, bolt_webauthn.go,
  custom_urls.go, notify.go) now check for nil before use, returning descriptive
  errors instead of panicking on missing or corrupted buckets.
- **Docker daemon startup validation:** `NewClient()` now pings the Docker daemon
  on init and returns a clear error if unreachable, instead of deferring failures
  to the first API call.
- **Notification dispatch timeouts:** `Stop()`, `flush()`, and `Reconfigure()`
  on `notify.Multi` now use a 30-second context timeout instead of
  `context.Background()`, preventing goroutines from hanging indefinitely during
  shutdown.

### Security
- **HTTP security headers:** Added middleware setting `X-Content-Type-Options`,
  `X-Frame-Options`, `Referrer-Policy`, `Permissions-Policy`, and
  `Content-Security-Policy` on all responses.
- **Auth endpoint rate limiting:** Per-IP sliding window rate limiter (10 req/min)
  on login, setup, passkey, and TOTP verification endpoints.
- **Container name input validation:** All 28 API handlers that accept a container
  name from URL path parameters now validate the input against a strict allowlist
  (alphanumeric, underscore, dot, hyphen) before processing.

### Fixed
- **IsIdle() race condition:** Replaced racy TryLock-based idle check with an
  atomic counter that tracks in-progress updates
- **SSE log stream injection:** Escape newlines and carriage returns in log data
  before writing to SSE frames to prevent event injection
- **Self-update concurrent guard:** Prevent multiple simultaneous self-update
  goroutines with an atomic bool gate; re-queue on failure
- **Notification dispatch ignores context:** `dispatch()` now passes the caller's
  context to notifier `Send()` calls instead of always using `context.Background()`
- **EventSource leak on navigation:** Clean up SSE log stream connection when
  user navigates away from the page
- **Scroll listener accumulation:** Deduplicate scroll handler on log stream
  reconnect to prevent listener pile-up
- **Reconfigure() timer race:** Flush pending batch events and stop timer before
  swapping notifier chain to prevent dispatching to stale notifiers
- **Inconsistent ContainerNames in single events:** Single-event batch
  pass-through now populates `ContainerNames` for consumer consistency
- Queue page self-update button not working (#58)
- Throttle `UpdateLastSeen` BoltDB writes to every 5 minutes per agent instead
  of every 30-second heartbeat, reducing disk I/O ~10x at scale (#50)
- **Version severity badges:** Coloured MAJOR/MINOR/PATCH/BUILD chips next to
  version arrows on dashboard, queue, and history pages. Uses `registry.ParseSemVer`
  with OCI label fallback for non-semver tags like "latest".
- **Row quick actions:** Detail, check, and logs icon buttons per container row,
  visible on hover (always visible on mobile touch devices).
- **History page version column:** Shows old-to-new version transition with
  severity badge. JS-side semver parser mirrors the Go implementation.
- **Settings "Show Advanced" toggle:** Hides expert settings (cluster, Portainer,
  scan concurrency, maintenance windows, OIDC, lifecycle hooks) by default.
  Persisted in localStorage.
- **System health indicators:** Docker and database health dots in the About tab.
  Nav bar health dot fetches `/readyz` on page load.
- **Dashboard keyboard shortcuts:** `m` (toggle manage mode), `s` (trigger scan),
  `?` (show help overlay).
- **"All up to date" celebration state:** Checkmark icon replaces "0" on the
  Updates Pending stat card when no updates are pending.
- **Row update flash animation:** Green highlight fades out over 500ms when SSE
  patches a container row.

### Fixed
- Severity badge empty for containers with non-semver tags (e.g. "latest") that
  have pending semver updates. Now falls back to the OCI
  `org.opencontainers.image.version` label.
- Port chips vanishing on SSE row refresh. `handleContainerRow` local path was
  missing `Ports` and `HostAddress` fields.
- Container detail page missing `Severity`, `HasUpdate`, and `DigestOnly` fields
  for both local and remote containers.
- Remote container detail `HasUpdate` not detecting digest-only updates. Now
  checks queue membership instead of `newestVersion != ""`.

## [2.8.0] - 2026-03-03

### Added
- NPM (Nginx Proxy Manager) integration with automatic proxy host resolution
- Per-port custom URL overrides for containers
- Clickable port links on dashboard with vertical stacking and +N collapse
- Configurable dashboard columns (show/hide ports, status, policy, etc.)
- Port mappings for remote cluster containers

### Changed
- Dashboard table redesign: removed Actions column, made status badges clickable
- Centre-aligned policy, status, and ports columns

### Fixed
- Queue CSS dead selectors and stale settings.html text
- Port chip alignment with column header
- NPM hot-init, flexBool deserialisation, host matching
- Private IP validation for NPM/Portainer URLs
- SSE reload prevention on form pages
- 40 code review issues resolved (#10-#57)

## [2.6.0] - 2026-03-02

### Added
- Version scope setting (relaxed/strict) for update filtering

### Fixed
- Default version scope to strict (issue #48)
- Per-row removal in bulk queue actions without page reload (#47)

## [2.5.1] - 2026-03-01

### Fixed
- Multi-arch digest mismatch causing no-op updates (#45)

## [2.5.0] - 2026-02-28

### Added
- Auto-update setting for remote Sentinel agents
- Remote containers in dependency graph
- GHCR switch for remote containers
- Host-scoped hook storage for remote containers
- Host-scoped notification preferences for remote containers
- Rollback for remote containers via update history lookup
- Remote container log streaming via cluster gRPC

### Fixed
- Local container updates now pull the target version, not the current tag
- Auto-filter to Unused when entering Images manage mode (#9)
- Images page column widths, manage mode checkboxes, vertical alignment (#9)
- UI bugs across Images, Logs, History, and Settings pages (#8)
- SSE auto-refresh for remote container updates
- Agent pushes fresh container list before update result
- ImageDigest population in agent container listings
- Rate limit check skips registry instead of stopping entire scan
- Stale gRPC goroutine after stream replacement
- 14 bugs from code review audit

## [2.4.0] - 2026-02-23

### Added
- Tag precision scope for constrained version comparison
- Parts field on SemVer for tag precision tracking

## [2.3.5] - 2026-02-21

### Fixed
- Fall back to registry digest for Swarm services without pinned digest

## [2.3.4] - 2026-02-21

### Fixed
- Minor stability fixes

## [2.3.3] - 2026-02-21

### Fixed
- Minor stability fixes

## [2.3.2] - 2026-02-21

### Fixed
- Minor stability fixes

## [2.3.1] - 2026-02-21

### Fixed
- Minor stability fixes

## [2.3.0] - 2026-02-20

### Added
- System Settings accordion in General tab
- Portainer-style enrollment snippets and auto-enrollment
- First-run setup wizard with time-limited setup window

### Fixed
- BoltDB deadlock, double-escape, lint issues
- Deduplicate local host tab when cluster mode is enabled
- Skip wizard when upgrading from older version with existing users
- Template parsing restricted to required files only
- Security and correctness fixes from code review

## [2.2.0] - 2026-02-19

### Added
- Multi-host agent architecture (cluster mode)
- Cluster management UI with settings tab, host cards, and dashboard host groups
- Embedded git commit hash in version string

### Fixed
- Cluster-aware policy resolution, registry dropdown, bulk policy
- 6 multi-host agent bugs found during acceptance testing
- Registry UX, Swarm sort, stat sync (issues #30, #31)

## [2.1.0] - 2026-02-16

### Added
- Docker Swarm service support with monitoring and updates
- Rollback policies (automatic, manual, disabled)
- Resolved version display for non-semver tags on dashboard

### Fixed
- SSE service row updates now match full page render
- Paginate GHCR tag listing for large repos
- Filter calver tags from semver version comparison
- Caching, validation, and safety issues from code review

## [2.0.2] - 2026-02-16

### Changed
- Replace setup token with 5-minute time window

### Fixed
- Show setup token field when URL param is missing

## [2.0.1] - 2026-02-15

### Added
- User column in activity log

### Fixed
- Settings toggles not persisting across page reloads

### Changed
- Split large files for navigability

## [2.0.0] - 2026-02-15

### Added
- Web dashboard with htmx and embedded HTML
- Authentication system (username/password, WebAuthn passkeys, OIDC SSO, TOTP 2FA)
- Role-based access control (admin, operator, viewer)
- Notification system with 10 providers (Gotify, Slack, Discord, Telegram, ntfy,
  Pushover, webhook, SMTP, Apprise, MQTT)
- Home Assistant MQTT discovery
- Notification templates with per-event customisation
- Per-container notification preferences and digest scheduling
- Registry credential management with rate limit tracking
- GHCR alternative detection for Docker Hub images
- Image cleanup after updates
- Cron scheduling (in addition to interval-based polling)
- Prometheus metrics endpoint
- Lifecycle hooks (pre/post-update, rollback)
- Dependency-aware update ordering
- BoltDB persistence for all state
- Container snapshot and rollback
- Manual approval queue with bulk actions
- Activity logging
- Config export/import
- Self-update mechanism
- Image management page (list, prune, delete)
- Grafana dashboard template

### Changed
- Complete rewrite from Docker-Guardian (shell script) to Go
- Modular architecture with clean package boundaries

[Unreleased]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.16.1...HEAD
[2.16.1]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.16.0...v2.16.1
[2.16.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.15.3...v2.16.0
[2.15.3]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.15.2...v2.15.3
[2.15.2]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.15.1...v2.15.2
[2.15.1]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.15.0...v2.15.1
[2.15.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.14.1...v2.15.0
[2.14.1]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.14.0...v2.14.1
[2.14.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.13.2...v2.14.0
[2.10.1]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.10.0...v2.10.1
[2.10.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.9.1...v2.10.0
[2.9.1]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.9.0...v2.9.1
[2.9.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.8.0...v2.9.0
[2.8.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.6.0...v2.8.0
[2.6.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.5.1...v2.6.0
[2.5.1]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.5.0...v2.5.1
[2.5.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.4.0...v2.5.0
[2.4.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.3.5...v2.4.0
[2.3.5]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.3.4...v2.3.5
[2.13.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.12.0...v2.13.0
[2.3.4]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.3.3...v2.3.4
[2.3.3]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.3.2...v2.3.3
[2.3.2]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.3.1...v2.3.2
[2.3.1]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.3.0...v2.3.1
[2.3.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.2.0...v2.3.0
[2.2.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.0.2...v2.1.0
[2.0.2]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.0.1...v2.0.2
[2.0.1]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/Will-Luck/Docker-Sentinel/releases/tag/v2.0.0
