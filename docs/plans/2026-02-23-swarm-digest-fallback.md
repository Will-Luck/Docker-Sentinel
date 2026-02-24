# Swarm Service Digest Fallback Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix "No such image" errors when scanning Swarm services on multi-node clusters where images aren't pulled on the manager node (GitHub issue #42).

**Architecture:** When a Swarm service spec lacks a pinned digest (`@sha256:...`), the current code calls `CheckVersioned()` which does a local `ImageDigest` inspect. On multi-node swarm, images live on worker nodes, not the manager, causing "No such image" errors and skipping all update detection. Fix by falling back from local `ImageDigest` to remote `DistributionDigest` when the local lookup fails, then using the existing `CheckVersionedWithDigest` flow.

**Tech Stack:** Go, Docker API (moby/moby), internal registry/engine packages

**Limitation:** When both local inspect and registry lookup fail (auth issues, private registry), the service is silently skipped rather than reported as an error. When local inspect fails and we use the registry digest as baseline, digest-only changes for non-semver tags (e.g., `latest`) won't be detected since local and remote digest are the same. Semver version listing still works.

---

### Task 1: Write failing test for multi-node digest fallback

**Files:**
- Modify: `internal/engine/service_comprehensive_test.go`

**Step 1: Write the failing test**

Add after `TestScanServicesNoDigestSuffix` (line ~466):

```go
// Service image on multi-node swarm where local ImageDigest fails
// (image only exists on worker nodes). Should fall back to
// DistributionDigest and not report an error.
func TestScanServicesNoLocalDigestFallsBack(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-remote", Spec: svcSpec("remote-svc", "fake.local/web:1.0", nil)},
	}
	// Local inspect fails (image not on manager node).
	mock.imageDigestErr["fake.local/web:1.0"] = fmt.Errorf("No such image: fake.local/web:1.0")
	// Remote registry reachable - different digest means update available.
	mock.distDigests["fake.local/web:1.0"] = "sha256:remote999"

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if len(result.Errors) > 0 {
		t.Errorf("expected no errors, got %v", result.Errors)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /home/lns/Docker-Sentinel && go test -run TestScanServicesNoLocalDigestFallsBack -count=1 ./internal/engine/`
Expected: FAIL - the current code propagates the ImageDigest error via `CheckVersioned()`.

**Step 3: Commit failing test**

```bash
cd /home/lns/Docker-Sentinel
git add internal/engine/service_comprehensive_test.go
git commit -m "test: add failing test for swarm no-local-digest fallback"
```

---

### Task 2: Implement the digest fallback in scanServices

**Files:**
- Modify: `internal/engine/service_update.go` lines 77-82

**Step 1: Replace the else branch**

Current code at lines 77-82:
```go
	var check registry.CheckResult
	if specDigest != "" {
		check = u.checker.CheckVersionedWithDigest(ctx, imageRef, specDigest)
	} else {
		check = u.checker.CheckVersioned(ctx, imageRef)
	}
```

Replace with:
```go
	var check registry.CheckResult
	if specDigest != "" {
		check = u.checker.CheckVersionedWithDigest(ctx, imageRef, specDigest)
	} else {
		// Try local image inspect first; fall back to registry digest
		// for multi-node swarm where images only exist on worker nodes.
		localDigest, err := u.docker.ImageDigest(ctx, imageRef)
		if err != nil {
			remoteDigest, rdErr := u.docker.DistributionDigest(ctx, imageRef)
			if rdErr != nil {
				u.log.Debug("service image not resolvable locally or remotely",
					"name", name, "image", imageRef, "error", rdErr)
				continue
			}
			localDigest = remoteDigest
		}
		check = u.checker.CheckVersionedWithDigest(ctx, imageRef, localDigest)
	}
```

**Step 2: Run the failing test from Task 1**

Run: `cd /home/lns/Docker-Sentinel && go test -run TestScanServicesNoLocalDigestFallsBack -count=1 ./internal/engine/`
Expected: PASS

**Step 3: Run full test suite**

Run: `cd /home/lns/Docker-Sentinel && go test -count=1 ./...`
Expected: `TestScanServicesRegistryCheckFails` will FAIL (it expects errors, but the fix changes behavior). Other tests pass.

**Step 4: Commit the fix**

```bash
cd /home/lns/Docker-Sentinel
git add internal/engine/service_update.go
git commit -m "fix: fall back to registry digest for swarm services without pinned digest

Fixes #42. In multi-node Swarm clusters, images may only exist on
worker nodes. When a service spec lacks an @sha256: digest pin,
ImageDigest (local inspect) fails with 'No such image'. Fall back
to DistributionDigest (registry query) to obtain a baseline digest
for version checking."
```

---

### Task 3: Update TestScanServicesRegistryCheckFails

The existing test (line 31-48) sets `imageDigestErr` for a service without spec digest and expects `result.Errors` to be non-empty. With the fix, the service now falls through to `DistributionDigest`. Since `distDigests` isn't set for that image, `DistributionDigest` returns `""` (no error), and `CheckVersionedWithDigest` proceeds with empty local digest vs empty remote digest.

**Files:**
- Modify: `internal/engine/service_comprehensive_test.go`

**Step 1: Read the test**

Current test at lines 31-48:
```go
func TestScanServicesRegistryCheckFails(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-1", Spec: svcSpec("broken-svc", "ghcr.io/owner/app:latest", nil)},
	}
	mock.imageDigestErr["ghcr.io/owner/app:latest"] = fmt.Errorf("image not found locally")

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Queued != 0 {
		t.Errorf("Queued = %d, want 0 (registry check failed)", result.Queued)
	}
	if len(result.Errors) == 0 {
		t.Error("expected registry error in result.Errors")
	}
}
```

**Step 2: Update to test new behavior**

The test should now verify that when local inspect fails AND remote also fails, the service is silently skipped (no error, no queue). Update to:

```go
func TestScanServicesRegistryCheckFails(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-1", Spec: svcSpec("broken-svc", "ghcr.io/owner/app:latest", nil)},
	}
	// Both local and remote lookups fail.
	mock.imageDigestErr["ghcr.io/owner/app:latest"] = fmt.Errorf("image not found locally")
	mock.distErr["ghcr.io/owner/app:latest"] = fmt.Errorf("401 unauthorized")

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Queued != 0 {
		t.Errorf("Queued = %d, want 0 (both lookups failed)", result.Queued)
	}
	// Both lookups failed: service silently skipped, not reported as error.
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors (silent skip), got %v", result.Errors)
	}
}
```

**Step 3: Run tests**

Run: `cd /home/lns/Docker-Sentinel && go test -count=1 ./internal/engine/`
Expected: All pass.

**Step 4: Commit**

```bash
cd /home/lns/Docker-Sentinel
git add internal/engine/service_comprehensive_test.go
git commit -m "test: update registry check test for digest fallback behavior"
```

---

### Task 4: Add test for multi-node update detection via fallback

Verify that when local inspect fails, the fallback to registry digest still allows update detection when remote digest differs from the fallback baseline (semver case where the tag itself has a newer version).

**Files:**
- Modify: `internal/engine/service_comprehensive_test.go`

**Step 1: Write the test**

```go
// Multi-node swarm: local inspect fails, registry returns a digest,
// and a different remote digest is available. Since we used the
// registry digest as baseline, digest comparison won't find a change
// (same registry queried both times). But this test verifies the
// error-free path completes without panics or spurious errors.
func TestScanServicesNoLocalDigestSameRegistryDigest(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-fb", Spec: svcSpec("fb-svc", "fake.local/fb:2.0", nil)},
	}
	mock.imageDigestErr["fake.local/fb:2.0"] = fmt.Errorf("No such image: fake.local/fb:2.0")
	// Registry returns same digest for both local-fallback and remote check.
	mock.distDigests["fake.local/fb:2.0"] = "sha256:same999"

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	// Same digest on both sides: no update, no error.
	if len(result.Errors) > 0 {
		t.Errorf("expected no errors, got %v", result.Errors)
	}
	if result.Queued != 0 {
		t.Errorf("Queued = %d, want 0 (no digest change)", result.Queued)
	}
}
```

**Step 2: Run tests**

Run: `cd /home/lns/Docker-Sentinel && go test -run TestScanServicesNoLocalDigest -count=1 ./internal/engine/`
Expected: Both `TestScanServicesNoLocalDigestFallsBack` and `TestScanServicesNoLocalDigestSameRegistryDigest` pass.

**Step 3: Commit**

```bash
cd /home/lns/Docker-Sentinel
git add internal/engine/service_comprehensive_test.go
git commit -m "test: verify no-digest service scan with same registry digest"
```

---

### Task 5: Run full test suite and build

**Step 1: Run all tests**

Run: `cd /home/lns/Docker-Sentinel && go test -count=1 ./...`
Expected: All pass.

**Step 2: Build**

Run: `cd /home/lns/Docker-Sentinel && go build ./...`
Expected: Clean build.

---

## Verification

After deploying a test build to a multi-node Swarm cluster:
1. Services without `@sha256:` in their spec should no longer show "No such image" errors
2. Services with pinned digests should work as before
3. Semver version detection should work for services regardless of spec format
4. Both local and remote lookup failures should silently skip the service (debug log only)
