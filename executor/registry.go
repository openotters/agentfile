package executor

import (
	"context"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
)

// ImageInfo is the metadata an Executor's Registry exposes for a
// stored ref. Mirrors the cross-cutting fields the openotters daemon
// surfaces via its `inspect` / `describe` RPCs.
//
// Created is unix seconds (0 = unknown) so both backends can return
// a single comparable value: oras-served registries pull it from
// the on-disk manifest's mtime; the Docker SDK parses the RFC3339
// `Created` field on ImageInspect.
//
// Description / Source are surfaced as their own fields (rather
// than via Labels/Annotations alone) because every consumer the
// daemon talks to wants those two specifically — extracting them
// once at the Registry layer means each call site doesn't have to
// re-implement the OCI standard-key fallback logic.
type ImageInfo struct {
	Ref         string
	Digest      string
	MediaType   string // OCI artifactType from the manifest, or the Docker config media type
	Size        int64
	CreatedUnix int64
	Description string
	Source      string

	// Labels are OCI image-config labels (Dockerfile LABEL
	// directives or oras-side equivalents). Populated when the
	// backend can read them cheaply; may be empty.
	Labels map[string]string

	// Annotations are manifest-level annotations. Same population
	// caveat.
	Annotations map[string]string
}

// Registry is the storage backend an Executor exposes for agent /
// runtime / BIN OCI artifacts. Each Executor implements it against
// its native storage:
//
//   - system: an embedded oras-go store + an OCI distribution HTTP
//     server bound to a loopback port.
//   - docker: the Docker daemon's image store, accessed via the moby
//     SDK (requires the containerd snapshotter for custom OCI
//     mediatypes — agent artifacts use
//     application/vnd.openotters.agent.v1).
//
// The interface is intentionally high-level. For the system
// executor's build pipeline (which streams blobs) BuildTarget
// returns the underlying oras.Target so existing build code keeps
// working without a wrapper. Docker returns nil from BuildTarget
// while build support is unimplemented; daemon image-build RPCs
// detect that and return a clear error.
type Registry interface {
	// List enumerates all refs known to this registry. Order is
	// implementation-defined.
	List(ctx context.Context) ([]string, error)

	// Resolve returns the descriptor for ref, or an error if ref
	// is not known. Errors are typed where possible
	// (errors.Is(err, ErrRefNotFound) == true on miss).
	Resolve(ctx context.Context, ref string) (ocispec.Descriptor, error)

	// Inspect returns metadata for ref: digest, size, labels,
	// annotations. Implementations populate as much as their
	// backend exposes; missing fields are zero-valued.
	Inspect(ctx context.Context, ref string) (ImageInfo, error)

	// Fetch returns a stream of the content addressed by desc.
	// Caller closes.
	Fetch(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error)

	// Tag points dst at the same descriptor as src. dst must be a
	// valid registry ref; src must already exist.
	Tag(ctx context.Context, src, dst string) error

	// Remove deletes ref. Unreferenced content blobs are garbage
	// collected by the backend on its own schedule.
	Remove(ctx context.Context, ref string) error

	// PullRemote fetches remoteRef from a remote registry into
	// this Registry. The local ref name matches remoteRef
	// verbatim (callers retag via Tag if they want a different
	// local name).
	PullRemote(ctx context.Context, remoteRef string) error

	// PushRemote sends localRef to a remote registry as
	// remoteRef. Auth comes from environment / credentials store
	// per backend.
	PushRemote(ctx context.Context, localRef, remoteRef string) error

	// BuildTarget exposes the underlying oras.Target for the
	// agentfile/build pipeline (which writes blobs + tags
	// directly via oras). Returns nil when the backend doesn't
	// support direct OCI writes — Docker today, until we wire
	// build-via-ImageLoad. Daemon callers must check for nil and
	// emit a clear "build unsupported on this executor" error.
	BuildTarget() oras.Target

	// ManifestKind returns the manifest's `artifactType` for ref —
	// the openotters kind ("application/vnd.openotters.{agent,bin}.v1")
	// when the producer stamped it, otherwise the empty string. Used
	// by the daemon to populate its image_kinds index at ingestion
	// time so subsequent listings don't have to re-derive the kind
	// from each backend's idiosyncratic surface (docker config Labels
	// for bins, manifest annotations for agents). Cheap on both
	// backends: system reads the manifest blob it already has;
	// docker reads the same Config.Labels[LabelArtifactType] today's
	// Inspect path used to read.
	ManifestKind(ctx context.Context, ref string) (string, error)
}
