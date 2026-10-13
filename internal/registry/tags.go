package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
)

// TagList holds the response from the Docker registry v2 tags/list endpoint.
type TagList struct {
	Tags []string `json:"tags"`
}

// SemVer represents a parsed semantic version.
type SemVer struct {
	Major int
	Minor int
	Patch int
	Pre   string // pre-release suffix (e.g. "rc1", "beta2")
	Raw   string // original tag string
	Parts int    // 2 for "x.y", 3 for "x.y.z"
}

// TagsResult holds the response from ListTags, including rate limit headers.
type TagsResult struct {
	Tags    []string
	Headers http.Header // response headers for rate limit extraction
}

// maxTagPages is the maximum number of pagination requests when fetching tags.
// GHCR caps responses at 1000 tags per page regardless of the n= parameter,
// and some images (e.g. linuxserver/*) have 5000+ tags with arch-specific and
// build-variant suffixes. 10 pages × 1000 = 10000 tags max.
const maxTagPages = 10

// ListTags fetches all tags for the given image reference from a container registry.
// For Docker Hub, it uses registry-1.docker.io. For other registries, it uses
// the registry host extracted from the image reference.
// Automatically paginates using the ?last= parameter when the registry returns
// a full page (GHCR caps at 1000 per page).
func ListTags(ctx context.Context, imageRef string, token string, host string, cred *RegistryCredential) (TagsResult, error) {
	repo := RepoPath(imageRef)
	var baseURL string
	if host != "" && host != "docker.io" {
		baseURL = "https://" + host + "/v2/" + repo + "/tags/list?n=10000"
	} else {
		baseURL = "https://registry-1.docker.io/v2/" + repo + "/tags/list"
	}

	var allTags []string
	var lastHeaders http.Header
	pageURL := baseURL

	for page := 0; page < maxTagPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return TagsResult{}, fmt.Errorf("create tags request: %w", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		} else if cred != nil {
			req.SetBasicAuth(cred.Username, cred.Secret)
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			return TagsResult{}, fmt.Errorf("fetch tags: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return TagsResult{}, fmt.Errorf("tags endpoint returned %d", resp.StatusCode)
		}

		var tagList TagList
		if err := json.NewDecoder(resp.Body).Decode(&tagList); err != nil {
			resp.Body.Close()
			return TagsResult{}, fmt.Errorf("decode tags response: %w", err)
		}
		lastHeaders = resp.Header
		resp.Body.Close()

		allTags = append(allTags, tagList.Tags...)

		// If we got fewer than 1000 tags, there are no more pages.
		if len(tagList.Tags) < 1000 {
			break
		}

		// Build next page URL using last= parameter.
		last := tagList.Tags[len(tagList.Tags)-1]
		if strings.Contains(baseURL, "?") {
			pageURL = baseURL + "&last=" + last
		} else {
			pageURL = baseURL + "?last=" + last
		}
	}

	return TagsResult{Tags: allTags, Headers: lastHeaders}, nil
}

// ParseSemVer attempts to parse a tag string as a semantic version.
// It handles "x.y.z", "x.y", and optional "v" prefix. Pre-release
// suffixes like "-rc1" or "-beta2" are captured in the Pre field.
// Returns the parsed version and true if successful, or zero value and false
// if the tag is not a valid semver.
func ParseSemVer(tag string) (SemVer, bool) {
	raw := tag

	// Strip optional "v" or "V" prefix.
	tag = strings.TrimPrefix(tag, "v")
	tag = strings.TrimPrefix(tag, "V")

	if tag == "" {
		return SemVer{}, false
	}

	// Split off pre-release suffix at the first hyphen.
	var pre string
	if idx := strings.Index(tag, "-"); idx >= 0 {
		pre = tag[idx+1:]
		tag = tag[:idx]
	}

	parts := strings.Split(tag, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return SemVer{}, false
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return SemVer{}, false
	}

	var minor int
	if len(parts) >= 2 {
		minor, err = strconv.Atoi(parts[1])
		if err != nil {
			return SemVer{}, false
		}
	}

	var patch int
	if len(parts) == 3 {
		patch, err = strconv.Atoi(parts[2])
		if err != nil {
			return SemVer{}, false
		}
	}

	return SemVer{
		Major: major,
		Minor: minor,
		Patch: patch,
		Pre:   pre,
		Raw:   raw,
		Parts: len(parts),
	}, true
}

// LessThan returns true if v is strictly less than other.
// Pre-release versions are considered less than their release counterpart
// (e.g. 1.2.3-rc1 < 1.2.3). When both have pre-release strings, they are
// compared identifier by identifier, with digit runs compared as numbers
// (rc9 < rc10).
func (v SemVer) LessThan(other SemVer) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor < other.Minor
	}
	if v.Patch != other.Patch {
		return v.Patch < other.Patch
	}

	// Same numeric version: pre-release sorts before release.
	if v.Pre != other.Pre {
		if v.Pre == "" {
			return false // v is release, other is pre-release
		}
		if other.Pre == "" {
			return true // v is pre-release, other is release
		}
		// Build-counter tags of one variant (ls190 vs ls191) order by counter.
		if vv, vc := splitBuildCounters(v.Pre); len(vc) > 0 {
			if ov, oc := splitBuildCounters(other.Pre); ov == vv && len(oc) == len(vc) {
				for i := range vc {
					if vc[i] != oc[i] {
						return vc[i] < oc[i]
					}
				}
				return false
			}
		}
		return comparePre(v.Pre, other.Pre) < 0
	}

	return false // equal
}

// preIdentifiers splits a pre-release string into comparable identifiers:
// separators (. - _) delimit identifiers and every change between letters and
// digits starts a new one, so "rc10" becomes ["rc", "10"] and "beta.2" becomes
// ["beta", "2"].
func preIdentifiers(pre string) []string {
	var ids []string
	start := 0
	isDigit := func(b byte) bool { return b >= '0' && b <= '9' }
	for i := 0; i <= len(pre); i++ {
		boundary := i == len(pre)
		if !boundary {
			switch pre[i] {
			case '.', '-', '_':
				boundary = true
			default:
				boundary = i > start && isDigit(pre[i]) != isDigit(pre[i-1])
			}
		}
		if !boundary {
			continue
		}
		if i > start {
			ids = append(ids, pre[start:i])
		}
		start = i
		if i < len(pre) && (pre[i] == '.' || pre[i] == '-' || pre[i] == '_') {
			start = i + 1
		}
	}
	return ids
}

// comparePre orders two pre-release strings using semver precedence rules:
// numeric identifiers compare as integers and sort before alphabetic ones,
// alphabetic identifiers compare as strings, and a shorter identifier list
// sorts before a longer one it prefixes. It returns -1, 0 or 1.
func comparePre(a, b string) int {
	ai, bi := preIdentifiers(a), preIdentifiers(b)
	for i := 0; i < len(ai) && i < len(bi); i++ {
		if c := compareIdentifier(ai[i], bi[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(ai) < len(bi):
		return -1
	case len(ai) > len(bi):
		return 1
	}
	return 0
}

func compareIdentifier(a, b string) int {
	aNum := a[0] >= '0' && a[0] <= '9'
	bNum := b[0] >= '0' && b[0] <= '9'
	switch {
	case aNum && bNum:
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case aNum:
		return -1
	case bNum:
		return 1
	}
	return strings.Compare(a, b)
}

// NewerVersions filters tags to find semver versions newer than current,
// returning them sorted from newest to oldest. Non-semver tags are ignored.
func NewerVersions(current string, tags []string) []SemVer {
	return NewerVersionsScoped(current, tags, docker.ScopeDefault, docker.ScopeDefault)
}

// NewerVersionsScopedWithBeyond returns the scoped newer-versions list (identical
// to NewerVersionsScoped) plus a count of higher registry versions that exist
// beyond the effective scope. Used by the UI (#83) to hint that the Version Scope
// is hiding higher releases instead of silently rendering nothing.
//
// beyondScope is computed as len(ScopeMajor result) - len(scoped result), clamped
// to >= 0. This is correct ONLY by cancellation: the floating-tag same-subtree
// skip, the version-scheme-mismatch guard, the variant filter (#84), and the
// semantic dedup all run identically in both the ScopeMajor pass and the scoped
// pass, so they cancel and the difference isolates exactly the scope-switch
// effect. Because the variant filter runs in both passes, beyondScope counts
// only same-variant higher versions (nginx:1.24.0 reports the bare 1.25/1.26/...
// lines, not every -alpine/-perl/-bookworm flavour of them). ScopeMajor does NOT mean
// "every newer tag" — do not refactor this into a raw tag count or the cancellation
// (and thus correctness) breaks. Non-semver / calver-exempt / already-newest cases
// all yield beyondScope == 0 because both passes return the same set.
func NewerVersionsScopedWithBeyond(current string, tags []string, scope, defaultScope docker.SemverScope) (newer []SemVer, beyondScope int) {
	newer = NewerVersionsScoped(current, tags, scope, defaultScope)
	major := NewerVersionsScoped(current, tags, docker.ScopeMajor, defaultScope)
	beyondScope = len(major) - len(newer)
	if beyondScope < 0 {
		beyondScope = 0
	}
	return newer, beyondScope
}

// NewerVersionsScoped is like NewerVersions but accepts an explicit per-container
// scope and a global defaultScope. Comparison is variant-aware (#84): candidates
// whose build-variant suffix differs from the current tag's are skipped, so a
// bare tag only sees bare tags and a -alpine tag only sees -alpine tags. When scope is ScopeDefault (no per-container override), defaultScope
// controls the precision-based filtering logic:
//
// A current tag that is not itself a pre-release is never offered a pre-release
// tag (rc, beta, dev, ...); a pre-release current still sees later pre-releases
// and the final release.
//
//   - ScopeDefault (relaxed): 3-part→patch, 2-part→minor+patch, 1-part→all
//   - ScopeStrict: 3-part→none, 2-part→patch, 1-part→minor+patch
func NewerVersionsScoped(current string, tags []string, scope, defaultScope docker.SemverScope) []SemVer {
	cur, ok := ParseSemVer(current)
	if !ok {
		return nil
	}

	var newer []SemVer
	curIsCalver := cur.Major >= 1900
	curVariant := cur.Variant()
	for _, tag := range tags {
		sv, ok := ParseSemVer(tag)
		if !ok {
			continue
		}
		if versionSchemeMismatch(cur, sv) {
			continue
		}

		// #84: variant-aware comparison. A bare current tag (nginx:1.24.0) only
		// competes with bare tags (pre-releases are filtered below); a variant
		// tag (1.24.0-alpine) only with the same variant. Without this, images that publish many
		// flavours (-alpine, -perl, -bookworm, -otel, ...) inflate the newer
		// list and the beyond-scope count by a variant multiplier.
		if sv.Variant() != curVariant {
			continue
		}

		// NEW-1: a less-specific current tag is the implicit "all children" pointer.
		// cur=2026.4 is equivalent to 2026.4.* ; cur=1 is equivalent to 1.*.*. The
		// floating tag tracks the latest descendant digest via the digest path, so
		// skip same-subtree candidates here to stop them flagging as "newer" forever.
		if cur.Parts < sv.Parts {
			switch cur.Parts {
			case 1:
				if sv.Major == cur.Major {
					continue
				}
			case 2:
				if sv.Major == cur.Major && sv.Minor == cur.Minor {
					continue
				}
			}
		}

		// Scope constraint: calver is exempt from scope filtering.
		if !curIsCalver {
			switch scope {
			case docker.ScopeDefault:
				if defaultScope == docker.ScopeStrict {
					// Strict: one level tighter than tag precision.
					if cur.Parts == 3 {
						continue // 3-part: no updates
					}
					if cur.Parts == 2 && (sv.Major != cur.Major || sv.Minor != cur.Minor) {
						continue // 2-part: patch only
					}
					if cur.Parts == 1 && sv.Major != cur.Major {
						continue // 1-part: minor+patch only
					}
				} else {
					// Default (relaxed): existing behaviour.
					if cur.Parts == 3 && (sv.Major != cur.Major || sv.Minor != cur.Minor) {
						continue
					}
					if cur.Parts == 2 && sv.Major != cur.Major {
						continue
					}
					// 1-part: no scope filter (same as ScopeMajor)
				}
			case docker.ScopePatch:
				if sv.Major != cur.Major || sv.Minor != cur.Minor {
					continue
				}
			case docker.ScopeMinor:
				if sv.Major != cur.Major {
					continue
				}
			case docker.ScopeMajor:
				// no scope filter
			}
		}
		if cur.LessThan(sv) {
			// A stable current tag is never offered a pre-release: moving a
			// production container onto 1.25.0-rc0 is not an update.
			if sv.IsPreRelease() && !cur.IsPreRelease() {
				continue
			}
			newer = append(newer, sv)
		}
	}

	// Sort newest first.
	sort.Slice(newer, func(i, j int) bool {
		return newer[j].LessThan(newer[i])
	})

	// NEW-2: dedup semantically equivalent versions, keeping the more-specific
	// representation. e.g. {2026.5.1, 2026.5, 2026.5.0} -> {2026.5.1, 2026.5.0}.
	// Pre-release suffix is part of the key so 2026.5.0-rc1 and 2026.5.0 stay
	// as separate entries.
	if len(newer) > 1 {
		type dedupKey struct {
			Major, Minor, Patch int
			Pre                 string
		}
		out := newer[:0]
		seen := make(map[dedupKey]int)
		for _, sv := range newer {
			key := dedupKey{sv.Major, sv.Minor, sv.Patch, sv.Pre}
			if idx, ok := seen[key]; ok {
				if sv.Parts > out[idx].Parts {
					out[idx] = sv
				}
				continue
			}
			seen[key] = len(out)
			out = append(out, sv)
		}
		newer = out
	}

	return newer
}

// FilterTags filters a tag list by include/exclude regex patterns.
// Invalid regex patterns are silently ignored (treated as absent).
func FilterTags(tags []string, includeRE, excludeRE string) []string {
	var inc, exc *regexp.Regexp
	if includeRE != "" {
		inc, _ = regexp.Compile(includeRE)
	}
	if excludeRE != "" {
		exc, _ = regexp.Compile(excludeRE)
	}
	if inc == nil && exc == nil {
		return tags
	}
	var result []string
	for _, t := range tags {
		if inc != nil && !inc.MatchString(t) {
			continue
		}
		if exc != nil && exc.MatchString(t) {
			continue
		}
		result = append(result, t)
	}
	return result
}

// versionSchemeMismatch returns true when two versions appear to use different
// versioning schemes (e.g. semver "3.21" vs calver "2021.12.14"). Comparing
// across schemes produces nonsensical results.
func versionSchemeMismatch(a, b SemVer) bool {
	aCalver := a.Major >= 1900
	bCalver := b.Major >= 1900
	return aCalver != bCalver
}

// preReleaseSuffixRE matches suffixes that denote a pre-release of the same
// version line (rc1, beta.2, alpha-3, dev, snapshot, ...) rather than a build
// variant (alpine, perl, bookworm, otel, slim, ...). Pre-releases participate
// in version ordering (1.2.3-rc1 < 1.2.3); variants partition the tag space.
var preReleaseSuffixRE = regexp.MustCompile(`(?i)^(alpha|beta|rc|pre|preview|dev|snapshot|nightly|canary|next|ea|milestone|m)([-.]?\d+)?$`)

// IsPreRelease reports whether the tag carries a pre-release suffix such as
// rc1, beta.2 or dev, as opposed to being a release or a build variant.
func (v SemVer) IsPreRelease() bool {
	return v.Pre != "" && preReleaseSuffixRE.MatchString(v.Pre)
}

// buildCounterRE matches one hyphen-separated suffix segment that is a build
// or revision counter: linuxserver's ls190, Bitnami's and Alpine's r9.
var buildCounterRE = regexp.MustCompile(`(?i)^(ls|r)(\d+)$`)

// splitBuildCounters separates a tag suffix into its variant key and its build
// counters. Each counter segment is replaced by its prefix and "#", so
// "debian-12-r9" gives ("debian-12-r#", [9]) and "r1-ls300" gives
// ("r#-ls#", [1 300]). Pre-release suffixes and suffixes without counters are
// returned unchanged with no counters.
func splitBuildCounters(pre string) (string, []int) {
	if pre == "" || preReleaseSuffixRE.MatchString(pre) {
		return pre, nil
	}
	segs := strings.Split(pre, "-")
	var counters []int
	for i, seg := range segs {
		m := buildCounterRE.FindStringSubmatch(seg)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		segs[i] = m[1] + "#"
		counters = append(counters, n)
	}
	if len(counters) == 0 {
		return pre, nil
	}
	return strings.Join(segs, "-"), counters
}

// Variant returns the build-variant identifier of a tag, or "" for bare tags
// and pre-release suffixes. "1.24.0" and "2.0.0-rc1" are both variant "";
// "1.24.0-alpine" is variant "alpine"; "1.24.0-alpine3.19" is "alpine3.19".
// Trailing build counters are not part of the variant, so "4.3.3-ls190" and
// "4.3.4-ls200" share the variant "ls#" and can be compared by version and
// counter; "7.2.4-debian-12-r9" is variant "debian-12-r#".
func (v SemVer) Variant() string {
	if v.Pre == "" || preReleaseSuffixRE.MatchString(v.Pre) {
		return ""
	}
	variant, _ := splitBuildCounters(v.Pre)
	return variant
}

// NormaliseRepo converts an image reference to a Docker Hub repository path.
// "nginx" becomes "library/nginx", "gitea/gitea:1.21" becomes "gitea/gitea".
func NormaliseRepo(imageRef string) string {
	// Strip tag or digest.
	ref := imageRef
	if idx := strings.Index(ref, "@"); idx >= 0 {
		ref = ref[:idx]
	}
	if idx := strings.Index(ref, ":"); idx >= 0 {
		ref = ref[:idx]
	}

	// Official images have no slash — prefix with "library/".
	if !strings.Contains(ref, "/") {
		ref = "library/" + ref
	}

	return ref
}
