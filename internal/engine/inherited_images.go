package engine

import (
	"context"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker/imageconfig"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/moby/moby/api/types/container"
)

// inheritedImageRows is how many of a container's history rows are read for
// the images it ran before the current one.
const inheritedImageRows = 10

// inheritedImageTimeout bounds the lookup of those images' configs.
const inheritedImageTimeout = 30 * time.Second

// inheritedImages returns the configs of the images the container ran
// before the one it runs now (imageID), from Sentinel's update history: the
// image each recorded update replaced, and the image each successful one
// moved to. A container an older Sentinel updated still carries the config
// of the first image it ran (see docker.RecreateOptions.InheritedImages).
// Only images of the container's current repository are used, so a
// different app that once had the same name is never taken for one of its
// images. An image whose config cannot be found on the host or in its
// registry is left out (logged at Debug), which keeps today's behaviour.
func (u *Updater) inheritedImages(ctx context.Context, name, imageRef, imageID string) []*container.Config {
	if u.imageConfigs == nil {
		return nil
	}
	recs, err := u.store.ListHistoryByContainer(name, inheritedImageRows)
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
	for _, rec := range recs {
		if rec.Type != "" && rec.Type != "container" {
			continue
		}
		add(rec.OldImage, rec.OldDigest)
		if rec.Outcome == "success" {
			add(rec.NewImage, rec.NewDigest)
		}
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
