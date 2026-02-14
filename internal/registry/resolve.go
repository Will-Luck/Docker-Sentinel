package registry

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// maxManifestHEADs is the maximum number of manifest HEAD requests for the
// initial pass (finding the target version, which is usually near the top).
const maxManifestHEADs = 10

// maxManifestHEADsExtended is the budget for the second pass when the target
// was found but the current version wasn't (the local image may be several
// versions behind). For images like Alpine, ~40 semver tags can separate
// adjacent minor versions (3.23→3.18), so 50 gives comfortable headroom.
const maxManifestHEADsExtended = 50

// RepoPath extracts the repository path from an image reference, stripping
// the registry host prefix. Unlike NormaliseRepo, this correctly handles
// third-party registries:
//
//	"nginx:latest"                  -> "library/nginx"
//	"ghcr.io/user/repo:tag"        -> "user/repo"
//	"gitea/gitea:1.21"             -> "gitea/gitea"
//	"lscr.io/linuxserver/radarr"   -> "linuxserver/radarr"
//	"docker.io/library/nginx"      -> "library/nginx"
func RepoPath(imageRef string) string {
	// Strip digest.
	ref := imageRef
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	// Strip tag. Use LastIndex to skip hostname:port colons and find
	// the tag separator (the last colon in the reference).
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		// Only strip if the colon is after the last slash (i.e. it's a tag
		// separator, not part of a hostname:port).
		if slash := strings.LastIndex(ref, "/"); i > slash {
			ref = ref[:i]
		}
	}

	// Detect and strip registry host prefix.
	if slash := strings.Index(ref, "/"); slash >= 0 {
		firstSegment := ref[:slash]
		if strings.ContainsAny(firstSegment, ".:") {
			// First segment is a hostname — strip it.
			ref = ref[slash+1:]
		}
	}

	// Official images have no slash — prefix with "library/".
	if !strings.Contains(ref, "/") {
		ref = "library/" + ref
	}

	return ref
}

// ManifestDigest performs a HEAD request against the registry v2 manifests
// endpoint and returns the Docker-Content-Digest header value. The repo
// parameter should be a registry-relative path (from RepoPath), not a full
// image reference.
func ManifestDigest(ctx context.Context, repo, tag, token, host string, cred *RegistryCredential) (string, http.Header, error) {
	var url string
	if host != "" && host != "docker.io" {
		url = "https://" + host + "/v2/" + repo + "/manifests/" + tag
	} else {
		url = "https://registry-1.docker.io/v2/" + repo + "/manifests/" + tag
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", nil, fmt.Errorf("create manifest HEAD request: %w", err)
	}

	// Accept manifest list / OCI index types to get the manifest list digest,
	// matching what DistributionInspect returns for multi-arch images.
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
	}, ", "))

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if cred != nil {
		req.SetBasicAuth(cred.Username, cred.Secret)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("manifest HEAD: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", resp.Header, fmt.Errorf("manifest HEAD returned %d", resp.StatusCode)
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", resp.Header, fmt.Errorf("no Docker-Content-Digest header")
	}

	return digest, resp.Header, nil
}

// ResolveVersions attempts to find which semver tags correspond to the given
// local and remote digests by performing manifest HEAD requests. It returns
// the matched version strings, or empty strings if no match is found.
//
// Uses a two-pass strategy: the first pass checks the newest tags (up to
// maxManifestHEADs) to find the target version quickly. If the target is
// found but the current version isn't (the local image may be several
// releases behind), a second pass continues deeper into the tag list.
func ResolveVersions(ctx context.Context, imageRef, localDigest, remoteDigest string,
	tags []string, token, host string, cred *RegistryCredential,
	tracker *RateLimitTracker) (currentVersion, targetVersion string) {

	// Filter and sort semver tags newest-first.
	var semvers []SemVer
	for _, tag := range tags {
		if sv, ok := ParseSemVer(tag); ok {
			semvers = append(semvers, sv)
		}
	}
	if len(semvers) == 0 {
		return "", ""
	}
	sort.Slice(semvers, func(i, j int) bool {
		return semvers[j].LessThan(semvers[i])
	})

	repo := RepoPath(imageRef)
	localHash := extractHash(localDigest)
	remoteHash := extractHash(remoteDigest)

	// First pass: check newest tags to find the target (and current if close).
	firstLimit := maxManifestHEADs
	if len(semvers) < firstLimit {
		firstLimit = len(semvers)
	}

	checked := 0
	for i := 0; i < firstLimit; i++ {
		// Check rate limits before each manifest HEAD request.
		if tracker != nil {
			if ok, _ := tracker.CanProceed(host, 2); !ok {
				break
			}
		}
		sv := semvers[i]
		digest, headers, err := ManifestDigest(ctx, repo, sv.Raw, token, host, cred)
		if tracker != nil && headers != nil {
			tracker.Record(host, headers)
		}
		checked++
		if err != nil {
			continue
		}

		hash := extractHash(digest)
		if hash == remoteHash && targetVersion == "" {
			targetVersion = sv.Raw
		}
		if hash == localHash && currentVersion == "" {
			currentVersion = sv.Raw
		}
		if currentVersion != "" && targetVersion != "" {
			return currentVersion, targetVersion
		}
	}

	// Second pass: if we found the target but not the current, keep searching
	// deeper into older tags to resolve the current version.
	if targetVersion != "" && currentVersion == "" {
		extLimit := checked + maxManifestHEADsExtended
		if len(semvers) < extLimit {
			extLimit = len(semvers)
		}
		for i := checked; i < extLimit; i++ {
			// Check rate limits before each manifest HEAD request.
			if tracker != nil {
				if ok, _ := tracker.CanProceed(host, 2); !ok {
					break
				}
			}
			sv := semvers[i]
			digest, headers, err := ManifestDigest(ctx, repo, sv.Raw, token, host, cred)
			if tracker != nil && headers != nil {
				tracker.Record(host, headers)
			}
			if err != nil {
				continue
			}

			hash := extractHash(digest)
			if hash == localHash {
				currentVersion = sv.Raw
				break
			}
		}
	}

	return currentVersion, targetVersion
}
