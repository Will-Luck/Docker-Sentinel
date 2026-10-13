package selfupdate

import (
	"context"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
)

// oldInstanceRE matches the name a superseded instance is renamed to.
var oldInstanceRE = regexp.MustCompile(`^(.+)-old-\d+$`)

// CleanupLeftovers removes "<name>-old-<unix>" containers left behind by
// earlier self-updates, and helper containers that did not remove themselves.
//
// The old container is the helper's way back until the helper has judged the
// new instance, so none is touched while a helper is active (running, created,
// restarting or paused) or the outcome file in dataDir says the swap is in
// progress. The helper removes the old container itself when the swap
// succeeds; this only clears what a helper left behind, and only for an old
// instance that has the sentinel.self label, is exited or created, and whose
// replacement (the same name without the suffix) is running and, if it has a
// health check, healthy. A helper is only removed when it has exited, or was created and never
// started for over 15 minutes (it is then taken for dead and does not hold
// the cleanup back).
// Anonymous volumes are kept.
//
// Only this instance's own leftovers are touched: selfID is this container's
// ID (or an ID prefix, see SelfID), and its name, without any -old- suffix, is
// the base of the leftovers that belong to it. Another Sentinel on the same
// host (a hub next to an agent) keeps its own. With no selfID nothing is done.
func CleanupLeftovers(ctx context.Context, d Docker, selfID, dataDir string, log Logger) {
	containers, err := d.ListAllContainers(ctx)
	if err != nil {
		log.Debug("self-update cleanup: list containers failed", "error", err)
		return
	}

	ownBase := ""
	for _, c := range containers {
		if selfID != "" && strings.HasPrefix(c.ID, selfID) && len(c.Names) > 0 {
			ownBase = strings.TrimPrefix(c.Names[0], "/")
			if m := oldInstanceRE.FindStringSubmatch(ownBase); m != nil {
				ownBase = m[1]
			}
			break
		}
	}
	if ownBase == "" {
		log.Debug("self-update cleanup: cannot tell which container this is, leaving leftovers alone")
		return
	}

	helperActive := InProgress(dataDir)
	runningID := make(map[string]string)
	for _, c := range containers {
		if len(c.Names) == 0 {
			continue
		}
		if c.Labels[HelperLabel] == "true" && !helperDead(c) && strings.TrimPrefix(c.Names[0], "/") == ownBase+"-self-update-helper" {
			helperActive = true
		}
		if c.State == container.StateRunning {
			runningID[strings.TrimPrefix(c.Names[0], "/")] = c.ID
		}
	}

	healthy := make(map[string]bool) // replacement name -> verdict, looked up once
	replacementGood := func(name string) bool {
		if v, ok := healthy[name]; ok {
			return v
		}
		good := false
		if id := runningID[name]; id != "" {
			if info, err := d.InspectContainer(ctx, id); err == nil && info.State != nil &&
				info.State.Running && !info.State.Restarting &&
				(info.State.Health == nil || info.State.Health.Status == container.Healthy) {
				good = true
			}
		}
		healthy[name] = good
		return good
	}

	for _, c := range containers {
		if len(c.Names) == 0 || (c.State != container.StateExited && c.State != container.StateCreated) {
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		if c.Labels[HelperLabel] == "true" && name != ownBase+"-self-update-helper" {
			continue // another instance's helper
		}
		isHelper := c.Labels[HelperLabel] == "true" && helperDead(c)
		if c.Labels[HelperLabel] == "true" && !isHelper {
			continue
		}
		isOld := false
		if !isHelper && c.Labels["sentinel.self"] == "true" && !helperActive {
			if m := oldInstanceRE.FindStringSubmatch(name); m != nil && m[1] == ownBase && replacementGood(m[1]) {
				isOld = true
			}
		}
		if !isHelper && !isOld {
			continue
		}
		if err := d.RemoveContainer(ctx, c.ID); err != nil {
			log.Warn("self-update cleanup: remove failed", "name", name, "error", err)
			continue
		}
		log.Info("removed a leftover container from an earlier self-update", "name", name)
	}
}

func timeNow() time.Time { return time.Now().UTC() }

// helperStaleAfter is how long a helper may sit in "created" (never started)
// before it is taken for dead.
const helperStaleAfter = 15 * time.Minute

// helperDead reports whether a helper container can no longer be at work: it
// has exited, or was created and never started for longer than helperStaleAfter.
func helperDead(c container.Summary) bool {
	switch c.State {
	case container.StateExited:
		return true
	case container.StateCreated:
		return c.Created > 0 && time.Since(time.Unix(c.Created, 0)) > helperStaleAfter
	}
	return false
}

// SelfContainerEnv names the environment variable that tells a Sentinel which
// container it is (name or ID), for when its ID cannot be read from the
// system: it is used only if neither the mount info nor the cgroup names the
// container, and the container must exist and carry sentinel.self=true.
const SelfContainerEnv = "SENTINEL_SELF_CONTAINER"

// SelfID returns this container's ID: from the container path in
// /proc/self/mountinfo (which holds whatever hostname the container was
// given), else from /proc/self/cgroup (cgroup v1 docker/<id>, v2
// docker-<id>.scope), else the value of SENTINEL_SELF_CONTAINER, else the
// hostname (a container's default hostname is its short ID). Use ResolveSelf
// to find the container in the Docker API; it checks an override.
func SelfID() string {
	var detected string
	if b, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		detected = containerIDFromMountInfo(string(b))
	}
	if detected == "" {
		if b, err := os.ReadFile("/proc/self/cgroup"); err == nil {
			detected = containerIDFromCgroup(string(b))
		}
	}
	host, _ := os.Hostname()
	return selfIDFrom(detected, os.Getenv(SelfContainerEnv), host)
}

// selfIDFrom picks the ID from what was detected, the override and the hostname.
func selfIDFrom(detected, override, hostname string) string {
	switch {
	case detected != "":
		return detected
	case override != "":
		return override
	}
	return hostname
}

var cgroupIDRE = regexp.MustCompile(`(?:/docker/|docker-)([0-9a-f]{64})`)

// containerIDFromCgroup finds the container ID in /proc/self/cgroup content:
// cgroup v1 lists ".../docker/<id>", v2 under systemd "docker-<id>.scope".
func containerIDFromCgroup(cgroup string) string {
	if m := cgroupIDRE.FindStringSubmatch(cgroup); m != nil {
		return m[1]
	}
	return ""
}

var containerPathRE = regexp.MustCompile(`/containers/([0-9a-f]{64})/`)

// containerIDFromMountInfo finds the container ID in /proc/self/mountinfo
// content (the /etc/hostname or resolv.conf mount sources name it).
func containerIDFromMountInfo(mountinfo string) string {
	if m := containerPathRE.FindStringSubmatch(mountinfo); m != nil {
		return m[1]
	}
	return ""
}
