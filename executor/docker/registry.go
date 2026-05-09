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

// ListEntries returns one ImageInfo per RepoTag in the daemon's
// image store, populated from a single cli.ImageList response. No
// per-ref Inspect roundtrips — Docker's ImageList already surfaces
// Id, Created, Size, and Labels for every image, which is
// everything the listing path consumes.
//
// Built so the daemon's ListImages can avoid a fan-out of N
// ImageInspect calls (which on a 99-image registry adds ~3-5s of
// serialised socket traffic on top of the single ImageList).
func (r *registry) ListEntries(ctx context.Context) ([]executor.ImageInfo, error) {
	res, err := r.client.ImageList(ctx, mobyclient.ImageListOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker: ImageList: %w", err)
	}

	seen := map[string]struct{}{}
	entries := make([]executor.ImageInfo, 0, len(res.Items))

	for _, img := range res.Items {
		for _, tag := range img.RepoTags {
			if tag == "<none>:<none>" {
				continue
			}
			if _, dup := seen[tag]; dup {
				continue
			}

			seen[tag] = struct{}{}

			labels := map[string]string{}
			for k, v := range img.Labels {
				labels[k] = v
			}

			entries = append(entries, executor.ImageInfo{
				Ref:         tag,
				Digest:      img.ID,
				Size:        img.Size,
				CreatedUnix: img.Created,
				Description: pickLabel(labels, ocispec.AnnotationDescription, "description"),
				Source:      pickLabel(labels, ocispec.AnnotationSource, "source"),
				Labels:      labels,
			})
		}
	}

	return entries, nil
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

// Inspect returns metadata for ref: digest, size, created, plus
// description / source extracted from the image config's standard
// OCI Labels. Cheap — single cli.ImageInspect roundtrip per ref,
// no manifest-blob reads.
//
// MediaType is intentionally left empty here. The daemon owns
// manifest-kind classification through its own image_kinds index
// (populated at ingestion time via ManifestKind), so Inspect no
// longer needs to surface artifactType.
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

	return executor.ImageInfo{
		Ref:         ref,
		Digest:      res.ID,
		Size:        res.Size,
		CreatedUnix: parseRFC3339Unix(res.Created),
		Description: pickLabel(labels, ocispec.AnnotationDescription, "description"),
		Source:      pickLabel(labels, ocispec.AnnotationSource, "source"),
		Labels:      labels,
	}, nil
}

// ManifestKind always returns empty on the docker backend: the
// moby SDK only exposes the manifest body via ImageSave (multi-MB
// tar stream over the unix socket — the slow path we removed in
// alpha.17), and Config.Labels is unreliable for openotters' agent
// images whose custom config mediatype causes docker to return a
// stub Config.
//
// The daemon doesn't need this method to work on docker because
// every ingestion path (commitBuilt / Pull / Save) plumbs the
// artifactType into the image_kinds index directly from the
// build pipeline / remote fetch where it's known. ManifestKind is
// kept on the interface for the system backend, where reading it
// from the embedded HTTP registry is cheap.
func (r *registry) ManifestKind(_ context.Context, _ string) (string, error) {
	return "", nil
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
		case hdr.Name == "index.json":
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
