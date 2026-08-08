package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
)

// HostPlatform is the platform of the process itself — what the system
// executor runs binaries as.
func HostPlatform() v1.Platform {
	return v1.Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH}
}

// LinuxPlatform is linux on the host's architecture — what the docker
// executor needs, since binaries run inside Linux containers regardless
// of the host OS.
func LinuxPlatform() v1.Platform {
	return v1.Platform{OS: "linux", Architecture: runtime.GOARCH}
}

// ResolveManifest fetches the blob at desc and parses it as a v1.Manifest.
// If the blob is a v1.Index, the child manifest matching platform is
// selected and resolved recursively. Resolution fails closed: an index
// with no manifest for the requested platform is an error, never a
// silent arbitrary pick.
func ResolveManifest(
	ctx context.Context, fetcher content.Fetcher, desc v1.Descriptor, platform v1.Platform,
) (*v1.Manifest, error) {
	data, err := FetchBlobBytes(ctx, fetcher, desc)
	if err != nil {
		return nil, fmt.Errorf("fetching manifest: %w", err)
	}

	platformDesc, isIndex, err := resolveIndex(data, platform)
	if err != nil {
		return nil, err
	}

	if isIndex {
		return ResolveManifest(ctx, fetcher, platformDesc, platform)
	}

	var manifest v1.Manifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	return &manifest, nil
}

// resolveIndex picks the index entry matching platform exactly
// (OS + architecture). The second return is false when data is not an
// index at all; a genuine index with no matching entry is an error
// naming what was requested and what is available.
func resolveIndex(data []byte, platform v1.Platform) (v1.Descriptor, bool, error) {
	var index v1.Index
	if err := json.Unmarshal(data, &index); err != nil {
		// Not parseable as an index — the caller falls through to
		// parsing data as a plain manifest, which produces the real
		// error if the blob is malformed.
		return v1.Descriptor{}, false, nil //nolint:nilerr // non-index input is the normal path, not a failure
	}

	if len(index.Manifests) == 0 {
		return v1.Descriptor{}, false, nil
	}

	available := make([]string, 0, len(index.Manifests))

	for _, m := range index.Manifests {
		if m.Platform == nil {
			continue
		}

		if m.Platform.OS == platform.OS && m.Platform.Architecture == platform.Architecture {
			return m, true, nil
		}

		available = append(available, m.Platform.OS+"/"+m.Platform.Architecture)
	}

	return v1.Descriptor{}, true, fmt.Errorf(
		"image index has no manifest for %s/%s (available: %s)",
		platform.OS, platform.Architecture, strings.Join(available, ", "),
	)
}
