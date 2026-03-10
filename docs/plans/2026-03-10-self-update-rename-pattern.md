# Self-Update: Rename-Before-Replace Pattern

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace Sentinel's ephemeral sidecar self-update with Watchtower's rename-before-replace pattern, eliminating the helper container and reducing the Traefik discovery gap from seconds to milliseconds.

**Architecture:** Rename the running Sentinel container to a temporary name, create a new container with the original name using the inspect config directly (no shell script), connect extra networks, start the new container, then let the old process exit naturally. The Docker API handles all operations natively with no intermediate shell or helper image.

**Tech Stack:** Go, moby client v0.2.2 (`ContainerRename`, `NetworkConnect`), existing `docker.API` interface

---

## Background

**Current approach (sidecar):** Pulls `docker:cli` helper image, creates an ephemeral container that runs a shell script to `docker stop/rm/run` the Sentinel container. Problems:
- Requires `docker:cli` image to be available
- Shell script reconstructs `docker run` args via string interpolation (fragile)
- Container is fully removed before recreation, causing a Traefik route gap of several seconds
- Helper container runs unsupervised after Sentinel exits

**New approach (rename-before-replace):** Uses Docker's `ContainerRename` API to move the running container out of the way, then creates the new container with the original name before stopping anything. Traefik sees the new container appear and only briefly loses the old one. No helper image, no shell scripts, no string interpolation.

**Flow:**
```
pull new image
  → rename "sentinel" → "sentinel-old-{timestamp}"
  → create "sentinel" (new image, same config from inspect)
  → connect extra networks to "sentinel"
  → start "sentinel"
  → old process exits naturally (this process IS the old container)
```

---

### Task 1: Add `RenameContainer` and `NetworkConnect` to `docker.API`

**Files:**
- Modify: `internal/docker/interface.go`
- Modify: `internal/docker/containers.go`

**Step 1: Add methods to the API interface**

In `internal/docker/interface.go`, add two methods to the `API` interface after `RestartContainer`:

```go
RenameContainer(ctx context.Context, id string, newName string) error
NetworkConnect(ctx context.Context, networkID string, containerID string) error
```

**Step 2: Implement `RenameContainer` in `containers.go`**

```go
// RenameContainer changes the name of a container.
func (c *Client) RenameContainer(ctx context.Context, id string, newName string) error {
	_, err := c.api.ContainerRename(ctx, id, client.ContainerRenameOptions{
		NewName: newName,
	})
	return err
}
```

**Step 3: Implement `NetworkConnect` in `containers.go`**

```go
// NetworkConnect connects a container to a network.
func (c *Client) NetworkConnect(ctx context.Context, networkID string, containerID string) error {
	_, err := c.api.NetworkConnect(ctx, networkID, client.NetworkConnectOptions{
		Container: containerID,
	})
	return err
}
```

**Step 4: Verify compilation**

Run: `cd /home/lns/Docker-Sentinel && go build ./...`
Expected: compiles cleanly (interface satisfied by `var _ API = (*Client)(nil)`)

**Step 5: Commit**

```bash
git add internal/docker/interface.go internal/docker/containers.go
git commit -m "feat: add RenameContainer and NetworkConnect to docker.API"
```

---

### Task 2: Add mock support for rename and network connect

**Files:**
- Modify: `internal/engine/mock_test.go`

**Step 1: Add mock fields**

Add to the `mockDocker` struct:

```go
renameCalls []struct{ id, newName string }
renameErr   map[string]error

networkConnectCalls []struct{ networkID, containerID string }
networkConnectErr   map[string]error
```

**Step 2: Initialise maps in `newMockDocker()`**

```go
renameErr:        make(map[string]error),
networkConnectErr: make(map[string]error),
```

**Step 3: Implement mock methods**

```go
func (m *mockDocker) RenameContainer(_ context.Context, id string, newName string) error {
	m.mu.Lock()
	m.renameCalls = append(m.renameCalls, struct{ id, newName string }{id, newName})
	m.mu.Unlock()
	if err, ok := m.renameErr[id]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) NetworkConnect(_ context.Context, networkID string, containerID string) error {
	m.mu.Lock()
	m.networkConnectCalls = append(m.networkConnectCalls, struct{ networkID, containerID string }{networkID, containerID})
	m.mu.Unlock()
	if err, ok := m.networkConnectErr[networkID]; ok {
		return err
	}
	return nil
}
```

**Step 4: Verify compilation**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/engine/ -count=1 -run TestSelfUpdate`
Expected: existing tests still pass

**Step 5: Commit**

```bash
git add internal/engine/mock_test.go
git commit -m "test: add RenameContainer and NetworkConnect to mock"
```

---

### Task 3: Rewrite `SelfUpdater.Update()` to rename-before-replace

**Files:**
- Modify: `internal/engine/selfupdate.go`

**Step 1: Write the new `Update` method**

Replace the entire `Update` method body. The new flow:

```go
func (su *SelfUpdater) Update(ctx context.Context, targetImage string) error {
	// 1. Find our own container.
	containers, err := su.docker.ListContainers(ctx)
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}

	var selfID, selfName string
	for _, c := range containers {
		if c.Labels["sentinel.self"] == "true" {
			selfID = c.ID
			if len(c.Names) > 0 {
				selfName = c.Names[0]
				if len(selfName) > 0 && selfName[0] == '/' {
					selfName = selfName[1:]
				}
			}
			break
		}
	}
	if selfID == "" {
		return fmt.Errorf("could not find sentinel container (no sentinel.self=true label)")
	}

	// 2. Inspect to capture full config.
	inspect, err := su.docker.InspectContainer(ctx, selfID)
	if err != nil {
		return fmt.Errorf("inspect self: %w", err)
	}
	if inspect.Config == nil {
		return fmt.Errorf("inspect %s: container config is nil", selfName)
	}

	imageRef := inspect.Config.Image
	if targetImage != "" {
		imageRef = targetImage
	}
	su.log.Info("self-update initiated", "name", selfName, "image", imageRef)

	// 3. Pull the new image before making any changes.
	su.log.Info("pulling target image", "image", imageRef)
	if err := su.docker.PullImage(ctx, imageRef); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}

	// 4. Rename self out of the way.
	oldName := fmt.Sprintf("%s-old-%d", selfName, time.Now().Unix())
	su.log.Info("renaming self", "from", selfName, "to", oldName)
	if err := su.docker.RenameContainer(ctx, selfID, oldName); err != nil {
		return fmt.Errorf("rename self: %w", err)
	}

	// 5. Build config for the new container from inspect data.
	// Override the image to the target version.
	newConfig := *inspect.Config
	newConfig.Image = imageRef

	// Collect extra networks (all non-bridge, after the first which goes in NetworkingConfig).
	var primaryNetwork string
	var extraNetworks []string
	if inspect.NetworkSettings != nil {
		for netName := range inspect.NetworkSettings.Networks {
			if netName == "bridge" {
				continue
			}
			if primaryNetwork == "" {
				primaryNetwork = netName
			} else {
				extraNetworks = append(extraNetworks, netName)
			}
		}
	}

	var netCfg *network.NetworkingConfig
	if primaryNetwork != "" {
		netCfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				primaryNetwork: {},
			},
		}
	}

	// 6. Create new container with the original name.
	su.log.Info("creating replacement container", "name", selfName, "image", imageRef)
	newID, err := su.docker.CreateContainer(ctx, selfName, &newConfig, inspect.HostConfig, netCfg)
	if err != nil {
		// Rollback: rename back to original name.
		su.log.Error("create failed, rolling back rename", "error", err)
		_ = su.docker.RenameContainer(ctx, selfID, selfName)
		return fmt.Errorf("create replacement: %w", err)
	}

	// 7. Connect extra networks.
	for _, netName := range extraNetworks {
		if err := su.docker.NetworkConnect(ctx, netName, newID); err != nil {
			su.log.Error("failed to connect extra network", "network", netName, "error", err)
			// Non-fatal: container can still start, just missing a secondary network.
		}
	}

	// 8. Start the new container.
	su.log.Info("starting replacement container", "id", newID[:12])
	if err := su.docker.StartContainer(ctx, newID); err != nil {
		// Rollback: remove new container, rename old back.
		su.log.Error("start failed, rolling back", "error", err)
		_ = su.docker.RemoveContainer(ctx, newID)
		_ = su.docker.RenameContainer(ctx, selfID, selfName)
		return fmt.Errorf("start replacement: %w", err)
	}

	// 9. Success. The old container (this process) will exit naturally.
	// The web handler returns 200 before this goroutine completes,
	// and the SSE reconnect logic in the frontend handles the transition.
	su.log.Info("self-update complete — new container running, old process will exit")
	return nil
}
```

**Step 2: Remove dead code**

Delete the `buildDockerRunArgs` and `shellEscape` functions entirely. They were only used by the sidecar pattern.

**Step 3: Update imports**

Remove unused imports (`strings`). Add `network` import:

```go
import (
	"context"
	"fmt"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/moby/moby/api/types/network"
)
```

The `container` and `mount` imports are no longer needed (HostConfig comes from inspect, not constructed).

**Step 4: Verify compilation**

Run: `cd /home/lns/Docker-Sentinel && go build ./...`
Expected: compiles cleanly

**Step 5: Commit**

```bash
git add internal/engine/selfupdate.go
git commit -m "feat: replace sidecar self-update with rename-before-replace pattern"
```

---

### Task 4: Rewrite self-update tests

**Files:**
- Modify: `internal/engine/selfupdate_test.go`

**Step 1: Update `sentinelInspect` helper to include NetworkSettings**

```go
func sentinelInspect(image string) container.InspectResponse {
	return container.InspectResponse{
		Config: &container.Config{
			Image:  image,
			Env:    []string{"SENTINEL_WEB_PORT=8080"},
			Labels: map[string]string{"sentinel.self": "true"},
		},
		HostConfig: &container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"sentinel_default": {},
			},
		},
	}
}
```

**Step 2: Rewrite `TestSelfUpdateUsesOriginalImage`**

Verify that when no targetImage is given, the new container is created with the image from inspect:

```go
func TestSelfUpdateUsesOriginalImage(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelContainer("abc123", "sentinel", "ghcr.io/will-luck/docker-sentinel:2.2.0")}
	mock.inspectResults["abc123"] = sentinelInspect("ghcr.io/will-luck/docker-sentinel:2.2.0")

	su := newTestSelfUpdater(mock)
	if err := su.Update(context.Background(), ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should create a container named "sentinel" (the original name).
	if len(mock.createCalls) != 1 || mock.createCalls[0] != "sentinel" {
		t.Fatalf("expected create call for 'sentinel', got %v", mock.createCalls)
	}

	// New container config should use the original image.
	cfg := mock.createConfigs["sentinel"]
	if cfg == nil {
		t.Fatal("no config captured for replacement container")
	}
	if cfg.Image != "ghcr.io/will-luck/docker-sentinel:2.2.0" {
		t.Errorf("expected image ghcr.io/will-luck/docker-sentinel:2.2.0, got %s", cfg.Image)
	}
}
```

**Step 3: Rewrite `TestSelfUpdateUsesTargetImage`**

```go
func TestSelfUpdateUsesTargetImage(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelContainer("abc123", "sentinel", "ghcr.io/will-luck/docker-sentinel:2.2.0")}
	mock.inspectResults["abc123"] = sentinelInspect("ghcr.io/will-luck/docker-sentinel:2.2.0")

	su := newTestSelfUpdater(mock)
	if err := su.Update(context.Background(), "ghcr.io/will-luck/docker-sentinel:2.3.1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg := mock.createConfigs["sentinel"]
	if cfg == nil {
		t.Fatal("no config captured for replacement container")
	}
	if cfg.Image != "ghcr.io/will-luck/docker-sentinel:2.3.1" {
		t.Errorf("expected target image, got %s", cfg.Image)
	}
}
```

**Step 4: Keep `TestSelfUpdateNoSentinelContainer` and `TestSelfUpdateInspectFails` as-is**

These tests don't touch the sidecar logic. They test early-exit error paths that are unchanged.

**Step 5: Replace `TestSelfUpdateHelperLifecycle` with `TestSelfUpdateRenameAndReplace`**

```go
func TestSelfUpdateRenameAndReplace(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelContainer("abc123", "sentinel", "img:1.0")}
	mock.inspectResults["abc123"] = sentinelInspect("img:1.0")

	su := newTestSelfUpdater(mock)
	if err := su.Update(context.Background(), "img:2.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have pulled the target image.
	if len(mock.pullCalls) != 1 || mock.pullCalls[0] != "img:2.0" {
		t.Errorf("expected pull of img:2.0, got %v", mock.pullCalls)
	}

	// Should have renamed the old container.
	if len(mock.renameCalls) != 1 {
		t.Fatalf("expected 1 rename call, got %d", len(mock.renameCalls))
	}
	if mock.renameCalls[0].id != "abc123" {
		t.Errorf("expected rename of abc123, got %s", mock.renameCalls[0].id)
	}
	if !strings.HasPrefix(mock.renameCalls[0].newName, "sentinel-old-") {
		t.Errorf("expected rename to sentinel-old-*, got %s", mock.renameCalls[0].newName)
	}

	// Should have created a container with the original name.
	if len(mock.createCalls) != 1 || mock.createCalls[0] != "sentinel" {
		t.Errorf("expected create of 'sentinel', got %v", mock.createCalls)
	}

	// Should have started the new container.
	if len(mock.startCalls) != 1 {
		t.Fatalf("expected 1 start call, got %d", len(mock.startCalls))
	}

	// No helper container should exist (no docker:cli pull).
	for _, ref := range mock.pullCalls {
		if ref == "docker:cli" {
			t.Error("should not pull docker:cli helper image")
		}
	}
}
```

**Step 6: Add `TestSelfUpdateRollbackOnCreateFailure`**

```go
func TestSelfUpdateRollbackOnCreateFailure(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelContainer("abc123", "sentinel", "img:1.0")}
	mock.inspectResults["abc123"] = sentinelInspect("img:1.0")
	mock.createErr["sentinel"] = fmt.Errorf("name conflict")

	su := newTestSelfUpdater(mock)
	err := su.Update(context.Background(), "img:2.0")
	if err == nil {
		t.Fatal("expected error when create fails")
	}

	// Should have attempted rename, then rolled back.
	if len(mock.renameCalls) != 2 {
		t.Fatalf("expected 2 rename calls (rename + rollback), got %d", len(mock.renameCalls))
	}
	// Second rename should restore original name.
	if mock.renameCalls[1].newName != "sentinel" {
		t.Errorf("rollback should rename back to 'sentinel', got %s", mock.renameCalls[1].newName)
	}
}
```

**Step 7: Add `TestSelfUpdateMultiNetwork`**

```go
func TestSelfUpdateMultiNetwork(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelContainer("abc123", "sentinel", "img:1.0")}
	inspect := sentinelInspect("img:1.0")
	inspect.NetworkSettings = &container.NetworkSettings{
		Networks: map[string]*network.EndpointSettings{
			"app_net":     {},
			"monitor_net": {},
		},
	}
	mock.inspectResults["abc123"] = inspect

	su := newTestSelfUpdater(mock)
	if err := su.Update(context.Background(), ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// One network in create's NetworkingConfig, one via NetworkConnect.
	if len(mock.networkConnectCalls) != 1 {
		t.Fatalf("expected 1 NetworkConnect call for second network, got %d", len(mock.networkConnectCalls))
	}
}
```

**Step 8: Run tests**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/engine/ -count=1 -run TestSelfUpdate -v`
Expected: all 7 tests pass

**Step 9: Commit**

```bash
git add internal/engine/selfupdate_test.go
git commit -m "test: rewrite self-update tests for rename-before-replace pattern"
```

---

### Task 5: Update the `SelfUpdater` doc comment

**Files:**
- Modify: `internal/engine/selfupdate.go`

**Step 1: Update the struct and method comments**

Change the `SelfUpdater` struct comment from:
```go
// SelfUpdater manages self-update operations via an ephemeral helper container.
```
to:
```go
// SelfUpdater manages self-update operations using the rename-before-replace
// pattern. It renames the running container, creates a new one with the
// original name, and starts it. The old container (this process) exits
// naturally after the new one is running.
```

**Step 2: Commit**

```bash
git add internal/engine/selfupdate.go
git commit -m "docs: update SelfUpdater comment for rename pattern"
```

---

### Task 6: Run full test suite and lint

**Step 1: Run all tests**

Run: `cd /home/lns/Docker-Sentinel && go test ./... -count=1`
Expected: all tests pass

**Step 2: Run linter**

Run: `cd /home/lns/Docker-Sentinel && make lint`
Expected: no issues (especially gofmt)

**Step 3: Fix any issues found**

If gofmt or lint fails, fix and re-run.

**Step 4: Final commit if any fixes needed**

```bash
git add -A
git commit -m "fix: lint/fmt corrections"
```

---

## Rollback plan

If rename-before-replace causes issues in production:
1. The old container (`sentinel-old-{timestamp}`) is still present and stopped
2. `docker rename sentinel-old-{timestamp} sentinel && docker start sentinel` restores the previous version
3. The old sidecar code can be reverted from git history

## What this does NOT change

- **Frontend SSE reconnect logic** (`sse.js` lines 224-242): unchanged, still checks `sentinel-self-updating` localStorage flag
- **API handler** (`api_control.go:apiSelfUpdate`): unchanged, still calls `SelfUpdater.Update()` in a goroutine
- **`SelfUpdater` interface** (`web/interfaces.go`): unchanged, still `Update(ctx, targetImage) error`
- **Queue JS** (`queue.js:triggerSelfUpdate`): unchanged, still sets localStorage flag and calls `/api/self-update`
