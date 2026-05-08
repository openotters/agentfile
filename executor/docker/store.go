package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"

	mobyclient "github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/errdef"

	"github.com/openotters/agentfile/executor"
)

// Store is an oras.Target backed by the Docker daemon's image
// store. It exists so the docker executor can use Docker as the
// One True Registry — `otters image build`, `bin build`, `image
// ls`, `image rm`, `image push/pull` all flow through `cli.Image*`
// instead of an embedded oras-go HTTP server.
//
// Wire shape: Push accumulates blobs/manifests in memory; Tag
// finalises the staged content into an OCI image layout tar and
// streams it through cli.ImageLoad. Resolve / Fetch / Exists serve
// from the cache when present, falling back to cli.ImageSave for
// content the daemon already has but we haven't staged.
//
// Why staging instead of a per-blob commit: cli.ImageLoad expects
// a complete OCI layout (oci-layout file + index.json + blobs/);
// blob-by-blob writes to the daemon don't exist in the SDK. Build
// pipelines (build.Build / bin.Build / bin.BuildIndex) push every
// blob, then tag the manifest once at the end — the same shape as
// the OCI distribution spec, just batched into a single ImageLoad
// at Tag time.
//
// One Store is created per (daemon, agent root) — the openotters
// daemon's storeFor closure spawns one per ref under the docker
// executor. The Store keeps blobs in memory; agent / bin OCI
// artifacts are kilobytes (a Linux-arm64 ping is ~2 MiB even
// gzipped), so memory pressure isn't a concern.
type Store struct {
	cli *mobyclient.Client

	mu sync.Mutex
	// staged blobs keyed by digest. Includes manifest + config +
	// all layers; everything that gets Push()ed goes here.
	blobs map[digest.Digest][]byte
	// staged manifest descriptors so Tag's ImageLoad can write
	// proper index.json entries.
	descs map[digest.Digest]ocispec.Descriptor
	// loaded tracks refs that have been hydrated from the daemon
	// (via ImageSave) — keyed by tag, value = manifest digest.
	// Avoids re-saving the whole image on every Resolve call.
	loaded map[string]ocispec.Descriptor
}

// NewStore returns an oras.Target backed by cli. The Store is
// stateful (accumulates blobs across Push calls) so callers should
// scope one to each build / read flow rather than sharing across
// concurrent operations.
func NewStore(cli *mobyclient.Client) *Store {
	return &Store{
		cli:    cli,
		blobs:  map[digest.Digest][]byte{},
		descs:  map[digest.Digest]ocispec.Descriptor{},
		loaded: map[string]ocispec.Descriptor{},
	}
}

// Push stages the blob at desc in memory. The bytes don't reach
// the docker daemon until Tag is called for some descriptor that
// references this blob.
func (s *Store) Push(_ context.Context, desc ocispec.Descriptor, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("docker store: read blob %s: %w", desc.Digest, err)
	}

	s.mu.Lock()
	s.blobs[desc.Digest] = data
	s.descs[desc.Digest] = desc
	s.mu.Unlock()

	return nil
}

// Exists reports whether desc is staged or already known to the
// daemon. We don't probe the daemon for arbitrary blobs — that
// would require a content-store API we don't have — so we only
// claim existence when the blob is in our staging map. The build
// pipeline tolerates Exists=false followed by a successful Push
// (oras Copy semantics), so the conservative answer is correct.
func (s *Store) Exists(_ context.Context, desc ocispec.Descriptor) (bool, error) {
	s.mu.Lock()
	_, ok := s.blobs[desc.Digest]
	s.mu.Unlock()

	return ok, nil
}

// Fetch returns the bytes for desc. Staged blobs win; otherwise we
// hydrate the entire image whose ref points at one of our loaded
// manifests, populating the cache so subsequent Fetches succeed
// from memory.
func (s *Store) Fetch(_ context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	s.mu.Lock()
	data, ok := s.blobs[desc.Digest]
	s.mu.Unlock()

	if ok {
		return io.NopCloser(bytes.NewReader(data)), nil
	}

	// We don't know which ref this descriptor belongs to — the
	// caller hydrated some image via Resolve, which already pulled
	// every blob into our cache. Missing here means the descriptor
	// is genuinely unknown.
	return nil, fmt.Errorf("%w: %s", errdef.ErrNotFound, desc.Digest)
}

// Resolve looks up ref → descriptor. Staged tags win (so Tag +
// Resolve in the same Store sees just-built content). Otherwise we
// ImageSave the ref, parse the OCI image layout tar, and populate
// our cache with every blob we saw. Returns ErrNotFound when the
// daemon has no image at that ref.
func (s *Store) Resolve(ctx context.Context, ref string) (ocispec.Descriptor, error) {
	s.mu.Lock()
	if desc, ok := s.loaded[ref]; ok {
		s.mu.Unlock()
		return desc, nil
	}
	s.mu.Unlock()

	manifestDesc, err := s.hydrate(ctx, ref)
	if err != nil {
		return ocispec.Descriptor{}, err
	}

	s.mu.Lock()
	s.loaded[ref] = manifestDesc
	s.mu.Unlock()

	return manifestDesc, nil
}

// Tag commits the staged content as a Docker image-store entry
// pointed at by ref. Builds an OCI image layout tar containing
// every staged blob + an index.json with the ref → manifest
// pointer, then streams it to the daemon via cli.ImageLoad.
//
// Subsequent Resolve(ref) calls return without re-saving thanks to
// the staged blobs cache. Tagging the same ref twice in a row
// (e.g. `bin build` writes both `name:latest` and `name:tag`) is
// supported — each call rebuilds the layout from the same blob set.
func (s *Store) Tag(ctx context.Context, desc ocispec.Descriptor, ref string) error {
	s.mu.Lock()
	// Caller may supply just a digest (oras's TagByDigest pattern);
	// fill missing MediaType/Size from the staged descriptor.
	if staged, ok := s.descs[desc.Digest]; ok {
		if desc.MediaType == "" {
			desc.MediaType = staged.MediaType
		}
		if desc.Size == 0 {
			desc.Size = staged.Size
		}
	}

	// Always remember the tag locally so Resolve(ref) in the same
	// build flow returns this descriptor — the build pipeline
	// (e.g. `bin.BuildIndex`) tags per-platform manifests with
	// internal names like "latest-linux-arm64" then Resolves them
	// to assemble the index.
	s.loaded[ref] = desc
	s.mu.Unlock()

	// Skip Docker-side commits for refs that aren't user-meaningful:
	//
	//   * "sha256:..." — oras's TagByDigest pattern. Docker would
	//     parse this as `repo=sha256,tag=...` and create a junk
	//     repo entry; the content is already digest-addressable.
	//   * Bareword names without ":" — build-pipeline-internal
	//     navigation aids ("latest", "latest-linux-arm64", …).
	//     They land in s.loaded so the in-process Resolve picks
	//     them up, but never reach Docker's image store.
	if strings.HasPrefix(ref, "sha256:") || !strings.Contains(ref, ":") {
		return nil
	}

	s.mu.Lock()
	blobs := make(map[digest.Digest][]byte, len(s.blobs))
	for d, b := range s.blobs {
		blobs[d] = b
	}
	s.mu.Unlock()

	if _, ok := blobs[desc.Digest]; !ok {
		return fmt.Errorf("docker store: Tag %s: manifest %s not staged", ref, desc.Digest)
	}

	tarBuf, err := buildOCILayoutTar(ref, desc, blobs)
	if err != nil {
		return fmt.Errorf("docker store: build layout tar: %w", err)
	}

	resp, err := s.cli.ImageLoad(ctx, tarBuf)
	if err != nil {
		return fmt.Errorf("docker store: ImageLoad %s: %w", ref, err)
	}
	defer func() { _ = resp.Close() }()

	// Drain the JSON-stream load progress so the daemon flushes
	// its work; surfacing it nicely is a follow-up.
	if _, copyErr := io.Copy(io.Discard, resp); copyErr != nil {
		return fmt.Errorf("docker store: drain load progress: %w", copyErr)
	}

	return nil
}

// hydrate ImageSaves ref out of the daemon and unpacks the OCI
// image layout into our staging maps. Returns the manifest
// descriptor for ref, taken from the saved index.json's ref-name
// annotation.
//
// We resolve ref to its image ID first because OCI artifacts (the
// agent OCI artifacts the docker.Store stores) can't be addressed
// by tag through `cli.ImageSave` — the SDK 404s as if the image
// doesn't exist. ImageList carries them in the catalog, so the ID
// lookup works for both regular images and artifacts.
func (s *Store) hydrate(ctx context.Context, ref string) (ocispec.Descriptor, error) {
	id, err := resolveImageID(ctx, s.cli, ref)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("%w: %s: %w", errdef.ErrNotFound, ref, err)
	}
	if id == "" {
		return ocispec.Descriptor{}, fmt.Errorf("%w: %s", errdef.ErrNotFound, ref)
	}

	rc, err := s.cli.ImageSave(ctx, []string{id})
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("%w: %s: %w", errdef.ErrNotFound, ref, err)
	}
	defer func() { _ = rc.Close() }()

	var indexData []byte
	saved := map[digest.Digest][]byte{}

	tr := tar.NewReader(rc)
	for {
		hdr, hdrErr := tr.Next()
		if hdrErr == io.EOF {
			break
		}
		if hdrErr != nil {
			return ocispec.Descriptor{}, fmt.Errorf("docker store: read save tar: %w", hdrErr)
		}

		switch {
		case hdr.Name == "index.json":
			data, readErr := io.ReadAll(tr)
			if readErr != nil {
				return ocispec.Descriptor{}, fmt.Errorf("docker store: read index.json: %w", readErr)
			}
			indexData = data

		case strings.HasPrefix(hdr.Name, "blobs/sha256/"):
			data, readErr := io.ReadAll(tr)
			if readErr != nil {
				return ocispec.Descriptor{}, fmt.Errorf("docker store: read blob: %w", readErr)
			}
			hex := path.Base(hdr.Name)
			d := digest.NewDigestFromEncoded(digest.SHA256, hex)
			saved[d] = data
		}
	}

	if indexData == nil {
		return ocispec.Descriptor{}, fmt.Errorf("docker store: %s save tar has no index.json", ref)
	}

	var index ocispec.Index
	if err = json.Unmarshal(indexData, &index); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("docker store: parse index.json: %w", err)
	}

	manifestDesc, ok := pickManifest(index, ref)
	if !ok {
		return ocispec.Descriptor{}, fmt.Errorf("%w: %s", errdef.ErrNotFound, ref)
	}

	s.mu.Lock()
	for d, data := range saved {
		s.blobs[d] = data
		// We don't have descriptor metadata for non-manifest
		// blobs from the index alone, so record a minimal one;
		// downstream Fetch only uses Digest/Size.
		s.descs[d] = ocispec.Descriptor{
			Digest: d,
			Size:   int64(len(data)),
		}
	}
	s.descs[manifestDesc.Digest] = manifestDesc
	s.mu.Unlock()

	return manifestDesc, nil
}

// buildOCILayoutTar streams blobs + the ref → desc index.json
// into an OCI image layout tarball suitable for cli.ImageLoad.
// Layout (per the OCI image-layout spec):
//
//	./oci-layout                           imageLayoutVersion 1.0.0
//	./index.json                           top-level index with ref annotation
//	./blobs/sha256/<hex>                   one file per blob (manifest + config + layers)
func buildOCILayoutTar(
	ref string, manifestDesc ocispec.Descriptor, blobs map[digest.Digest][]byte,
) (*bytes.Buffer, error) {
	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)

	if err := writeTarFile(tw, "oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)); err != nil {
		return nil, err
	}

	index := ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{
			{
				MediaType: manifestDesc.MediaType,
				Digest:    manifestDesc.Digest,
				Size:      manifestDesc.Size,
				Annotations: map[string]string{
					ocispec.AnnotationRefName:  ref,
					"io.containerd.image.name": ref,
				},
			},
		},
	}

	indexData, err := json.Marshal(index)
	if err != nil {
		return nil, fmt.Errorf("marshal index: %w", err)
	}

	if err = writeTarFile(tw, "index.json", indexData); err != nil {
		return nil, err
	}

	for d, data := range blobs {
		blobPath := "blobs/" + d.Algorithm().String() + "/" + d.Encoded()
		if err = writeTarFile(tw, blobPath, data); err != nil {
			return nil, err
		}
	}

	if err = tw.Close(); err != nil {
		return nil, err
	}

	return &buf, nil
}

func writeTarFile(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:   name,
		Mode:   0o644,
		Size:   int64(len(data)),
		Format: tar.FormatPAX,
	}

	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("tar header %s: %w", name, err)
	}

	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("tar write %s: %w", name, err)
	}

	return nil
}

// pickManifest finds the index entry whose ref-name annotation
// matches ref. Falls back to the first manifest entry when no
// annotation matches — single-image saves often omit the
// annotation but only have one entry, so this is safe.
func pickManifest(index ocispec.Index, ref string) (ocispec.Descriptor, bool) {
	for _, m := range index.Manifests {
		if m.Annotations[ocispec.AnnotationRefName] == ref ||
			m.Annotations["io.containerd.image.name"] == ref {
			return m, true
		}
	}

	if len(index.Manifests) == 1 {
		return index.Manifests[0], true
	}

	return ocispec.Descriptor{}, false
}

// Compile-time guarantees: *Store satisfies oras.Target.
var (
	_ oras.Target = (*Store)(nil)
	_             = executor.Mount{} // keep `executor` import live for cross-pkg edits
)
