package engine

import (
	"context"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker/imageconfig"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/moby/moby/api/types/container"
)

// inheritedImageRows is how many of a container's updates that changed its
// image (success, partial, rollback) are read for the images it ran before
// the current one; inheritedHistoryScan bounds the rows read to find them.
const (
	inheritedImageRows   = 10
	inheritedHistoryScan = 1000
)

// inheritedImageTimeout bounds the lookup of those images' configs.
const inheritedImageTimeout = 30 * time.Second

// inheritedImages returns the configs of the images the container ran
// before the one it runs now (imageID), from its last inheritedImageRows
// updates that changed the image in Sentinel's history: the image each one
// replaced, and the image each successful (or partial) one moved to. A container an older Sentinel updated still carries the config
// of the first image it ran (see docker.RecreateOptions.InheritedImages).
// Only images of the container's current repository are used, so a
// different app that once had the same name is never taken for one of its
// images. An image whose config cannot be found on the host or in its
// registry is left out (logged at Debug), which keeps today's behaviour.
func (u *Updater) inheritedImages(ctx context.Context, name, imageRef, imageID string) []*container.Config {
	if u.imageConfigs == nil {
		return nil
	}
	recs, err := u.store.ListHistoryByContainer(name, inheritedHistoryScan)
	if err != nil {
		u.log.Debug("could not read the history for earlier images", "name", name, "error", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, inheritedImageTimeout)
	defer cancel()

	repo := repoKey(imageRef)
	seen := map[string]bool{"": true, imageID: true}
	var configs []*container.Config
	add := func(ref, digest string) {
		if seen[digest] || repoKey(ref) != repo {
			return
		}
		seen[digest] = true
		cfg, err := u.imageConfigs.Config(ctx, imageconfig.Image{Ref: ref, Digest: digest})
		if err != nil {
			u.log.Debug("could not read the config of an earlier image", "name", name, "image", digest, "error", err)
			return
		}
		configs = append(configs, cfg)
	}
	used := 0
	for _, rec := range recs {
		if used == inheritedImageRows {
			break
		}
		if rec.Type != "" && rec.Type != "container" {
			continue
		}
		switch rec.Outcome {
		case "success", "partial":
			// The container moved to the new image.
			add(rec.OldImage, rec.OldDigest)
			add(rec.NewImage, rec.NewDigest)
		case "rollback":
			add(rec.OldImage, rec.OldDigest)
		default:
			continue // a failed attempt, a refusal or a note changed nothing
		}
		used++
	}
	return configs
}

// repoKey names the repository of an image reference, registry included.
func repoKey(ref string) string {
	if ref == "" {
		return ""
	}
	return registry.RegistryHost(ref) + "/" + registry.RepoPath(ref)
}

// registryCredentials returns the stored registry credentials, if any.
func (u *Updater) registryCredentials() []registry.RegistryCredential {
	if u.checker == nil || u.checker.CredentialStore() == nil {
		return nil
	}
	creds, err := u.checker.CredentialStore().GetRegistryCredentials()
	if err != nil {
		return nil
	}
	return creds
}
