package docker

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	mobyclient "github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"

	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/spec"
)

// registry is the docker executor's Registry: agents / runtime / BIN
// images live in the Docker daemon's image store, accessed via the
// moby/moby SDK. Requires the containerd snapshotter for non-Docker
// OCI mediatypes (verified at NewProvider time).
//
// Operations not yet implemented (PullRemote / PushRemote in this
// commit, BuildTarget always nil) return ErrNotImplemented; daemon
// callers detect that and emit "not supported on docker executor"
// where appropriate.
// ociIndexFilename is the entry name for the OCI image layout's
// index.json inside the tar produced by cli.ImageSave.
const ociIndexFilename = "index.json"

// maxLayerDiscardBytes caps how many bytes we'll io.Copy(Discard)
// per entry when skipping a non-manifest blob in the save tar.
// OCI artifact images we care about are at most a few hundred MB
// per platform — anything larger is almost certainly malformed
// and not worth pulling off the wire.
const maxLayerDiscardBytes = 1 << 30 // 1 GiB

type registry struct {
	client Client
}

func newRegistry(client Client) *registry {
	return &registry{client: client}
}

// List returns every image ref Docker knows about, expanding
// multi-tagged images into one entry per tag. No filtering: an
// "openotters image label" filter would be appropriate once the
// build path stamps a consistent label, but until then we surface
// the daemon's full RepoTags so the operator can see what's
// actually there.
func (r *registry) List(ctx context.Context) ([]string, error) {
	res, err := r.client.ImageList(ctx, mobyclient.ImageListOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker: ImageList: %w", err)
	}

	seen := map[string]struct{}{}
	var refs []string
	for _, img := range res.Items {
		for _, tag := range img.RepoTags {
			if tag == "<none>:<none>" {
				continue
			}
			if _, dup := seen[tag]; dup {
				continue
			}
			seen[tag] = struct{}{}
			refs = append(refs, tag)
		}
	}

	return refs, nil
}

// Resolve looks up ref → descriptor. Returns ErrRefNotFound when
// Docker has no image at that ref. Routes through resolveImageID
// so OCI artifacts (which `cli.ImageInspect` 404s on by tag) are
// reachable.
func (r *registry) Resolve(ctx context.Context, ref string) (ocispec.Descriptor, error) {
	id, err := resolveImageID(ctx, r.client, ref)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("docker: resolve %s: %w", ref, err)
	}
	if id == "" {
		return ocispec.Descriptor{}, fmt.Errorf("%w: %s", executor.ErrRefNotFound, ref)
	}

	res, err := r.client.ImageInspect(ctx, id)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("docker: ImageInspect %s: %w", id, err)
	}

	return ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.Digest(res.ID),
		Size:      res.Size,
	}, nil
}

// Inspect returns metadata for ref. Description / Source come
// from the image-config Labels map (the OCI standard
// `org.opencontainers.image.*` keys stamped by the build pipeline
// or Dockerfile LABEL directives).
//
// The ref is passed to ImageInspect verbatim — Docker 28+ with the
// containerd snapshotter resolves OCI artifact tags directly. We
// deliberately don't call resolveImageID first: that helper does
// cli.ImageList which is slow when the daemon hosts many images,
// and forcing it onto every Inspect serialised the listing path
// on a daemon-side O(N) scan.
//
// artifactType is read fast-path from Config.Labels[LabelArtifactType]
// (stamped at build time by bintool's BIN build, and any future
// builders following the same convention). When the label is
// absent — old bin images built before the convention, openotters
// agent images whose custom Config mediatype docker doesn't
// surface — Inspect falls back to reading the manifest blob via
// ImageSave. The fallback is slow but only triggers for legacy or
// agent images.
func (r *registry) Inspect(ctx context.Context, ref string) (executor.ImageInfo, error) {
	res, err := r.client.ImageInspect(ctx, ref)
	if err != nil {
		if isNotFoundErr(err) {
			return executor.ImageInfo{}, fmt.Errorf("%w: %s", executor.ErrRefNotFound, ref)
		}

		return executor.ImageInfo{}, fmt.Errorf("docker: ImageInspect %s: %w", ref, err)
	}

	labels := map[string]string{}
	if res.Config != nil {
		for k, v := range res.Config.Labels {
			labels[k] = v
		}
	}

	mediaType := ocispec.MediaTypeImageManifest
	if res.Descriptor != nil && res.Descriptor.MediaType != "" {
		mediaType = res.Descriptor.MediaType
	}

	var (
		artifactType string
		annotations  map[string]string
	)

	// Fast path: artifactType label present on the image config.
	// New bintool / agent builders stamp this at build time.
	if v, ok := labels[spec.LabelArtifactType]; ok && v != "" {
		artifactType = v
	} else {
		// Slow fallback for legacy images: read the manifest blob
		// via ImageSave. Agent images also land here because
		// docker doesn't surface Config.Labels for our custom
		// config mediatype.
		artifactType, annotations = r.readArtifactFromSave(ctx, ref)
	}

	description := pickLabel(labels, ocispec.AnnotationDescription, "description")
	source := pickLabel(labels, ocispec.AnnotationSource, "source")

	if description == "" {
		description = pickLabel(annotations, ocispec.AnnotationDescription, "description")
	}
	if source == "" {
		source = pickLabel(annotations, ocispec.AnnotationSource, "source")
	}

	return executor.ImageInfo{
		Ref:         ref,
		Digest:      res.ID,
		MediaType:   firstNonEmpty(artifactType, mediaType),
		Size:        res.Size,
		CreatedUnix: parseRFC3339Unix(res.Created),
		Description: description,
		Source:      source,
		Labels:      labels,
		Annotations: annotations,
	}, nil
}

// readArtifactFromSave streams the OCI image layout tar from
// ImageSave and parses out the manifest's artifactType +
// annotations. The tar reader aborts (via ReadCloser.Close()) the
// instant we have everything we need — for OCI artifacts the
// manifest comes before any large layer blobs, so the daemon stops
// streaming before transferring the bulk of the image. Empty
// strings + nil map on any failure; Inspect tolerates the absence
// (ImageInfo.MediaType falls back to the manifest mediaType).
//
// We can't read this from cli.ImageInspect — even with
// ImageInspectWithManifests(true), Descriptor.ArtifactType is set
// only when the descriptor itself (the parent index's pointer)
// carries it, never from the manifest *body* where openotters'
// build pipeline writes it. Descriptor.Annotations only contains
// containerd-injected runtime metadata (image.ref.name etc.), not
// the manifest blob's own annotations either.
func (r *registry) readArtifactFromSave(
	ctx context.Context, ref string,
) (string, map[string]string) {
	rc, err := r.client.ImageSave(ctx, []string{ref})
	if err != nil {
		return "", nil
	}
	defer func() { _ = rc.Close() }()

	return parseArtifactFromSaveTar(rc)
}

// parseArtifactFromSaveTar walks the tar entries and returns as
// soon as the manifest blob (small) has been read, leaving any
// remaining layer blobs unstreamed. Caller closes the underlying
// reader to abort the in-flight ImageSave.
func parseArtifactFromSaveTar(r io.Reader) (string, map[string]string) {
	tr := tar.NewReader(r)

	var (
		indexData     []byte
		manifestBlobs = map[string][]byte{}
		wantDigest    string
	)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil
		}

		switch {
		case hdr.Name == ociIndexFilename:
			data, readErr := io.ReadAll(tr)
			if readErr != nil {
				return "", nil
			}

			indexData = data

			var index struct {
				Manifests []ocispec.Descriptor `json:"manifests"`
			}
			if jsonErr := json.Unmarshal(indexData, &index); jsonErr == nil &&
				len(index.Manifests) > 0 {
				wantDigest = index.Manifests[0].Digest.Encoded()
			}

		case strings.HasPrefix(hdr.Name, "blobs/sha256/"):
			// Skip any blob clearly larger than a manifest. OCI
			// manifests are small JSON (typically <10 KiB); layer
			// blobs are MB+. This avoids buffering layer bytes
			// while we wait for the manifest entry.
			if hdr.Size > 256*1024 {
				if _, copyErr := io.CopyN(io.Discard, tr, maxLayerDiscardBytes); copyErr != nil && !errors.Is(copyErr, io.EOF) {
					return "", nil
				}

				continue
			}

			data, readErr := io.ReadAll(tr)
			if readErr != nil {
				return "", nil
			}

			manifestBlobs[path.Base(hdr.Name)] = data

			// If we already know which blob is the manifest and
			// just read it, stop reading the tar.
			if wantDigest != "" && path.Base(hdr.Name) == wantDigest {
				return extractArtifact(manifestBlobs[wantDigest])
			}
		}
	}

	if wantDigest == "" {
		return "", nil
	}

	if blob, ok := manifestBlobs[wantDigest]; ok {
		return extractArtifact(blob)
	}

	return "", nil
}

// extractArtifact pulls artifactType + annotations out of a parsed
// manifest blob. Walks the multi-arch index path: if the top-level
// blob is itself an index without artifactType, the first child
// platform manifest's value wins (mirrors how openotters builds
// stamp the type on the whole image, not per-platform).
func extractArtifact(blob []byte) (string, map[string]string) {
	var manifest struct {
		ArtifactType string            `json:"artifactType"`
		Annotations  map[string]string `json:"annotations"`
		Manifests    []struct {
			ArtifactType string            `json:"artifactType"`
			Annotations  map[string]string `json:"annotations"`
		} `json:"manifests"`
	}

	if err := json.Unmarshal(blob, &manifest); err != nil {
		return "", nil
	}

	if manifest.ArtifactType != "" {
		return manifest.ArtifactType, manifest.Annotations
	}

	for _, m := range manifest.Manifests {
		if m.ArtifactType != "" {
			return m.ArtifactType, manifest.Annotations
		}
	}

	return "", manifest.Annotations
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Fetch is unused on the docker backend (the daemon's image RPCs
// only need List / Inspect / Tag / Remove / PullRemote /
// PushRemote). Returns ErrNotImplemented for now.
func (r *registry) Fetch(_ context.Context, _ ocispec.Descriptor) (io.ReadCloser, error) {
	return nil, executor.ErrNotImplemented
}

// Tag re-points dst at the same image as src.
func (r *registry) Tag(ctx context.Context, src, dst string) error {
	_, err := r.client.ImageTag(ctx, mobyclient.ImageTagOptions{Source: src, Target: dst})
	if err != nil {
		return fmt.Errorf("docker: ImageTag %s → %s: %w", src, dst, err)
	}
	return nil
}

// Remove deletes ref. OCI artifacts can't be removed by tag
// directly (the daemon's image API 404s on them), so we resolve
// to ID first and remove that — which untags every alias and
// deletes the underlying content.
func (r *registry) Remove(ctx context.Context, ref string) error {
	id, err := resolveImageID(ctx, r.client, ref)
	if err != nil {
		return fmt.Errorf("docker: resolve %s: %w", ref, err)
	}
	if id == "" {
		return fmt.Errorf("%w: %s", executor.ErrRefNotFound, ref)
	}

	_, err = r.client.ImageRemove(ctx, id, mobyclient.ImageRemoveOptions{
		Force:         true,
		PruneChildren: true,
	})
	if err != nil {
		if isNotFoundErr(err) {
			return fmt.Errorf("%w: %s", executor.ErrRefNotFound, ref)
		}
		return fmt.Errorf("docker: ImageRemove %s: %w", ref, err)
	}

	return nil
}

// PullRemote fetches remoteRef into Docker's local cache via the
// SDK's ImagePull. Auth resolution mirrors `docker pull` — the
// daemon honours ~/.docker/config.json on most setups, but private
// ghcr.io repos need an X-Registry-Auth header which we resolve
// ourselves to bypass the SDK's "anonymous-only" defaults.
func (r *registry) PullRemote(ctx context.Context, remoteRef string) error {
	rc, err := r.client.ImagePull(ctx, remoteRef, mobyclient.ImagePullOptions{
		RegistryAuth: resolveRegistryAuth(remoteRef),
	})
	if err != nil {
		return fmt.Errorf("docker: ImagePull %s: %w", remoteRef, err)
	}
	defer func() { _ = rc.Close() }()

	// Drain the JSON-stream pull progress; surfacing it nicely is
	// a follow-up.
	if _, copyErr := io.Copy(io.Discard, rc); copyErr != nil {
		return fmt.Errorf("docker: drain pull progress: %w", copyErr)
	}
	return nil
}

// PushRemote sends localRef to a remote registry as remoteRef. If
// localRef ≠ remoteRef this tags first then pushes.
func (r *registry) PushRemote(ctx context.Context, localRef, remoteRef string) error {
	if localRef != remoteRef {
		if err := r.Tag(ctx, localRef, remoteRef); err != nil {
			return err
		}
	}

	rc, err := r.client.ImagePush(ctx, remoteRef, mobyclient.ImagePushOptions{
		RegistryAuth: resolveRegistryAuth(remoteRef),
	})
	if err != nil {
		return fmt.Errorf("docker: ImagePush %s: %w", remoteRef, err)
	}
	defer func() { _ = rc.Close() }()

	if _, copyErr := io.Copy(io.Discard, rc); copyErr != nil {
		return fmt.Errorf("docker: drain push progress: %w", copyErr)
	}
	return nil
}

// BuildTarget exposes a docker-backed oras.Target so callers that
// can't use storeFor directly (e.g. an `image push` that streams
// from local disk) still have a write path into the daemon's
// image store. Returns a fresh Store per call — staging is per-
// build, never shared across operations.
func (r *registry) BuildTarget() oras.Target {
	return NewStore(r.client)
}

// parseRFC3339Unix turns an RFC3339 timestamp string into unix
// seconds. Returns 0 on parse failure so callers can detect "no
// known creation time" with a single == 0 check.
func parseRFC3339Unix(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return 0
		}
	}
	return t.Unix()
}

// pickLabel returns the first non-empty value among the supplied
// keys. Lets the OCI standard `org.opencontainers.image.*`
// annotations be preferred while still surfacing values stamped
// with the bare `description` / `source` keys.
func pickLabel(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

// readManifestFromSaveTar parses an OCI image layout tar (the
// shape ImageSave / Store.buildOCILayoutTar produces) and returns
// the raw bytes of the manifest pointed at by index.json. Used by
// the store tests to round-trip the tar layout we hand to
// cli.ImageLoad. The Inspect path used to call this on every ref
// to extract artifactType — that's been replaced with
// ImageInspectWithManifests, which surfaces artifactType directly
// without streaming the image tar.
func readManifestFromSaveTar(r io.Reader) ([]byte, error) {
	tr := tar.NewReader(r)

	blobs := map[string][]byte{}

	var indexData []byte

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		switch {
		case hdr.Name == ociIndexFilename:
			data, readErr := io.ReadAll(tr)
			if readErr != nil {
				return nil, readErr
			}
			indexData = data

		case strings.HasPrefix(hdr.Name, "blobs/sha256/"):
			data, readErr := io.ReadAll(tr)
			if readErr != nil {
				return nil, readErr
			}
			blobs[path.Base(hdr.Name)] = data
		}
	}

	if len(indexData) == 0 {
		return nil, errors.New("save tar: no index.json")
	}

	var index struct {
		Manifests []ocispec.Descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(indexData, &index); err != nil {
		return nil, err
	}

	if len(index.Manifests) == 0 {
		return nil, errors.New("save tar: empty manifests")
	}

	hex := index.Manifests[0].Digest.Encoded()

	manifestBytes, ok := blobs[hex]
	if !ok {
		return nil, errors.New("save tar: manifest blob missing")
	}

	return manifestBytes, nil
}

// isNotFoundErr matches the moby SDK's "image not found" error
// shape. The SDK exposes errdefs.IsNotFound but checking the
// stringly-typed message is a fallback that works across SDK
// versions.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	// Best-effort string match. The SDK's typed
	// errdefs.IsNotFound would be cleaner; revisit when we wire
	// errdefs across the codebase.
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "no such image"),
		strings.Contains(msg, "not found"):
		return true
	}
	// Also wrap other errdefs-style not-found via errors.Is if the
	// SDK gains them.
	var notFound interface{ NotFound() bool }
	if errors.As(err, &notFound) && notFound.NotFound() {
		return true
	}
	return false
}

// Compile-time guarantee.
var _ executor.Registry = (*registry)(nil)
