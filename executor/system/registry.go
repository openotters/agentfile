package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"

	"github.com/openotters/agentfile/executor"
	agentoci "github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// maxRegistryReadBytes caps every registry HTTP read so a malicious
// or confused registry can't OOM the daemon.
const maxRegistryReadBytes int64 = 100 * 1024 * 1024

// CreatedAtFunc returns the unix-seconds when the manifest at
// (repo, tag) was first written to the embedded registry. The
// daemon implements this against its on-disk blob mtime; tests
// may pass nil and accept CreatedUnix=0 in returned ImageInfo.
type CreatedAtFunc func(repo, tag string) int64

// registry implements executor.Registry for the system executor.
// Storage lives in the daemon's embedded registry (an OCI
// distribution HTTP server backed by an oras.Target on disk).
//
// Three injection points:
//
//   - target: an oras.Target for content-addressed reads (Fetch,
//     Tag, BuildTarget). Comes from WithRegistryTarget.
//   - addr:   the embedded registry's HTTP address ("host:port"),
//     for List / Inspect / Remove which use the OCI distribution
//     spec endpoints (catalog, tags/list, manifests). Comes from
//     WithRegistryAddr.
//   - createdAt: callback returning when a manifest was first
//     stored. Comes from WithRegistryCreatedAt. Optional —
//     CreatedUnix is 0 in returned ImageInfo when absent.
//
// target / addr may be empty in tests; methods that need the
// missing dependency return ErrNotImplemented.
type registry struct {
	target    oras.Target
	addr      string
	createdAt CreatedAtFunc
}

func newRegistry(target oras.Target, addr string, createdAt CreatedAtFunc) *registry {
	return &registry{target: target, addr: addr, createdAt: createdAt}
}

// NewRegistry returns an executor.Registry backed by the embedded
// HTTP registry. Exported so other executors (notably the docker
// executor) can compose a fallback for agent OCI artifacts —
// custom mediatypes that Docker's image store can't represent.
//
// target is the oras.Target backing Fetch / Tag / BuildTarget;
// addr is the embedded registry's HTTP "host:port" used for
// catalog / manifest endpoints; createdAt is optional and may
// be nil.
func NewRegistry(target oras.Target, addr string, createdAt CreatedAtFunc) executor.Registry {
	return newRegistry(target, addr, createdAt)
}

// List returns every "<repo>:<tag>" pair served by the embedded
// registry.
func (r *registry) List(ctx context.Context) ([]string, error) {
	if r.addr == "" {
		return nil, executor.ErrNotImplemented
	}

	cat, err := fetchCatalog(ctx, r.addr)
	if err != nil {
		return nil, fmt.Errorf("system registry: list repos: %w", err)
	}

	var refs []string
	for _, repo := range cat.Repositories {
		tags, tagErr := fetchTags(ctx, r.addr, repo)
		if tagErr != nil {
			continue
		}
		for _, tag := range tags.Tags {
			refs = append(refs, repo+":"+tag)
		}
	}

	return refs, nil
}

// Resolve looks up ref → descriptor via the embedded registry.
func (r *registry) Resolve(ctx context.Context, ref string) (v1.Descriptor, error) {
	if r.target == nil {
		return v1.Descriptor{}, executor.ErrNotImplemented
	}

	desc, err := r.target.Resolve(ctx, ref)
	if err != nil {
		return v1.Descriptor{}, fmt.Errorf("%w: %s: %w", executor.ErrRefNotFound, ref, err)
	}

	return desc, nil
}

// Inspect returns ImageInfo by GETting the manifest from the
// embedded registry and walking it (multi-arch index → sum subs;
// plain manifest → sum config + layers).
func (r *registry) Inspect(ctx context.Context, ref string) (executor.ImageInfo, error) {
	if r.addr == "" {
		return executor.ImageInfo{}, executor.ErrNotImplemented
	}

	repo, tag := splitRef(ref)
	if repo == "" {
		return executor.ImageInfo{}, fmt.Errorf("system registry: invalid ref %q", ref)
	}

	mi, err := fetchManifestInfo(ctx, r.addr, repo, tag)
	if err != nil {
		return executor.ImageInfo{}, fmt.Errorf("system registry: %s: %w", ref, err)
	}

	var created int64
	if r.createdAt != nil {
		created = r.createdAt(repo, tag)
	}

	return executor.ImageInfo{
		Ref:         ref,
		Digest:      mi.digest,
		Size:        mi.size,
		CreatedUnix: created,
		Description: pickAnnotation(mi.annotations, v1.AnnotationDescription, "description"),
		Source:      pickAnnotation(mi.annotations, v1.AnnotationSource, "source"),
		Annotations: mi.annotations,
	}, nil
}

// ManifestKind returns the manifest's artifactType for ref. The
// embedded registry's manifest blob is the source of truth; the
// daemon calls this at ingestion time and persists the result into
// its image_kinds index so subsequent listings don't need to round-
// trip through this path.
func (r *registry) ManifestKind(ctx context.Context, ref string) (string, error) {
	if r.addr == "" {
		return "", executor.ErrNotImplemented
	}

	repo, tag := splitRef(ref)
	if repo == "" {
		return "", fmt.Errorf("system registry: invalid ref %q", ref)
	}

	mi, err := fetchManifestInfo(ctx, r.addr, repo, tag)
	if err != nil {
		return "", fmt.Errorf("system registry: %s: %w", ref, err)
	}

	return mi.artifactType, nil
}

// pickAnnotation returns the first non-empty value among the keys.
func pickAnnotation(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

// Fetch streams the content for desc from the underlying
// oras.Target.
func (r *registry) Fetch(ctx context.Context, desc v1.Descriptor) (io.ReadCloser, error) {
	if r.target == nil {
		return nil, executor.ErrNotImplemented
	}
	return r.target.Fetch(ctx, desc)
}

// Tag points dst at the same descriptor as src in the embedded
// registry.
func (r *registry) Tag(ctx context.Context, src, dst string) error {
	if r.target == nil {
		return executor.ErrNotImplemented
	}
	desc, err := r.target.Resolve(ctx, src)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", executor.ErrRefNotFound, src, err)
	}
	return r.target.Tag(ctx, desc, dst)
}

// Remove deletes ref from the embedded registry via the OCI
// distribution spec's `DELETE /v2/<repo>/manifests/<digest>`.
func (r *registry) Remove(ctx context.Context, ref string) error {
	if r.addr == "" {
		return executor.ErrNotImplemented
	}

	repo, tag := splitRef(ref)
	if repo == "" {
		return fmt.Errorf("system registry: invalid ref %q", ref)
	}

	// The HTTP API requires a digest for deletes (tags can't be
	// deleted directly). Resolve tag → digest first.
	mi, err := fetchManifestInfo(ctx, r.addr, repo, tag)
	if err != nil {
		return fmt.Errorf("system registry: resolve %s: %w", ref, err)
	}

	url := fmt.Sprintf("http://%s/v2/%s/manifests/%s", r.addr, repo, mi.digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("system registry: delete %s: %w", ref, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxRegistryReadBytes))
		return fmt.Errorf("system registry: delete %s: HTTP %d: %s", ref, resp.StatusCode, string(body))
	}
	return nil
}

// PullRemote fetches a remote ref into the embedded registry. The
// embedded copy lives at "<embeddedAddr>/<remote.Name>:<tag>", so
// daemon code that resolves unqualified names against the embedded
// registry finds it without further setup.
func (r *registry) PullRemote(ctx context.Context, remoteRef string) error {
	if r.addr == "" {
		return executor.ErrNotImplemented
	}

	parsed := spec.ParseReference(remoteRef)

	srcRepo, err := agentoci.NewRemoteRepository(parsed)
	if err != nil {
		return fmt.Errorf("system registry: open remote %s: %w", remoteRef, err)
	}

	srcTag := parsed.Tag
	if srcTag == "" {
		srcTag = defaultRefTag
	}

	localRef := spec.Reference{
		Name: r.addr + "/" + parsed.Name,
		Tag:  srcTag,
	}

	dstRepo, err := agentoci.NewRemoteRepository(localRef, agentoci.WithPlainHTTP)
	if err != nil {
		return fmt.Errorf("system registry: open local %s: %w", localRef, err)
	}

	if _, copyErr := oras.Copy(ctx, srcRepo, srcTag, dstRepo, srcTag, oras.DefaultCopyOptions); copyErr != nil {
		return fmt.Errorf("system registry: pull %s: %w", remoteRef, copyErr)
	}
	return nil
}

// PushRemote sends localRef from the embedded registry to a remote
// registry as remoteRef. localRef may be qualified
// ("<embeddedAddr>/<name>:<tag>") or unqualified
// ("<name>:<tag>"); the latter is auto-qualified against the
// embedded address.
func (r *registry) PushRemote(ctx context.Context, localRef, remoteRef string) error {
	if r.addr == "" {
		return executor.ErrNotImplemented
	}

	parsedLocal := spec.ParseReference(localRef)
	if !strings.HasPrefix(parsedLocal.Name, r.addr+"/") {
		parsedLocal = spec.Reference{
			Name: r.addr + "/" + parsedLocal.Name,
			Tag:  parsedLocal.Tag,
		}
	}

	srcTag := parsedLocal.Tag
	if srcTag == "" {
		srcTag = defaultRefTag
	}

	srcRepo, err := agentoci.NewRemoteRepository(parsedLocal, agentoci.WithPlainHTTP)
	if err != nil {
		return fmt.Errorf("system registry: open local %s: %w", localRef, err)
	}

	parsedRemote := spec.ParseReference(remoteRef)

	dstRepo, err := agentoci.NewRemoteRepository(parsedRemote)
	if err != nil {
		return fmt.Errorf("system registry: open remote %s: %w", remoteRef, err)
	}

	dstTag := parsedRemote.Tag
	if dstTag == "" {
		dstTag = defaultRefTag
	}

	if _, copyErr := oras.Copy(ctx, srcRepo, srcTag, dstRepo, dstTag, oras.DefaultCopyOptions); copyErr != nil {
		return fmt.Errorf("system registry: push %s → %s: %w", localRef, remoteRef, copyErr)
	}
	return nil
}

// BuildTarget exposes the underlying oras.Target so the daemon's
// build pipeline can stream blobs + tag the result without going
// through the higher-level Registry methods.
func (r *registry) BuildTarget() oras.Target {
	return r.target
}

// Compile-time guarantee that *registry satisfies executor.Registry.
var _ executor.Registry = (*registry)(nil)

// catalogResponse / tagsResponse / manifestInfo mirror the OCI
// distribution spec response bodies the embedded registry serves.
type catalogResponse struct {
	Repositories []string `json:"repositories"`
}

type tagsResponse struct {
	Tags []string `json:"tags"`
}

type manifestInfo struct {
	digest       string
	artifactType string
	size         int64
	annotations  map[string]string
}

func fetchCatalog(ctx context.Context, addr string) (*catalogResponse, error) {
	return fetchJSON[catalogResponse](ctx, fmt.Sprintf("http://%s/v2/_catalog", addr))
}

func fetchTags(ctx context.Context, addr, repo string) (*tagsResponse, error) {
	return fetchJSON[tagsResponse](ctx, fmt.Sprintf("http://%s/v2/%s/tags/list", addr, repo))
}

func fetchManifestInfo(ctx context.Context, addr, repo, tag string) (*manifestInfo, error) {
	data, dgst, err := fetchManifestRaw(ctx, addr, repo, tag)
	if err != nil {
		return nil, err
	}

	// Multi-arch index path.
	var index v1.Index
	if json.Unmarshal(data, &index) == nil && len(index.Manifests) > 0 {
		total := int64(len(data))
		for _, m := range index.Manifests {
			sub, subErr := fetchManifestInfo(ctx, addr, repo, m.Digest.String())
			if subErr != nil {
				total += m.Size
				continue
			}
			total += sub.size
		}
		return &manifestInfo{
			digest:       dgst,
			artifactType: index.ArtifactType,
			size:         total,
			annotations:  index.Annotations,
		}, nil
	}

	var manifest v1.Manifest
	if parseErr := json.Unmarshal(data, &manifest); parseErr != nil {
		return nil, fmt.Errorf("parse manifest: %w", parseErr)
	}

	total := int64(len(data)) + manifest.Config.Size
	for _, l := range manifest.Layers {
		total += l.Size
	}

	return &manifestInfo{
		digest:       dgst,
		artifactType: manifest.ArtifactType,
		size:         total,
		annotations:  manifest.Annotations,
	}, nil
}

func fetchManifestRaw(ctx context.Context, addr, repo, tag string) ([]byte, string, error) {
	url := fmt.Sprintf("http://%s/v2/%s/manifests/%s", addr, repo, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, "", err
	}
	// Accept both manifest and index media types so multi-arch
	// images don't 415 when the registry advertises an Accept-aware
	// negotiation.
	req.Header.Set("Accept", strings.Join([]string{
		v1.MediaTypeImageManifest,
		v1.MediaTypeImageIndex,
	}, ", "))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistryReadBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) > maxRegistryReadBytes {
		return nil, "", errors.New("manifest exceeds size cap")
	}

	dgst := resp.Header.Get("Docker-Content-Digest")
	if dgst == "" {
		dgst = digest.FromBytes(body).String()
	}

	return body, dgst, nil
}

func fetchJSON[T any](ctx context.Context, url string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistryReadBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxRegistryReadBytes {
		return nil, errors.New("response exceeds size cap")
	}
	var out T
	if parseErr := json.Unmarshal(body, &out); parseErr != nil {
		return nil, fmt.Errorf("parse response: %w", parseErr)
	}
	return &out, nil
}

// splitRef parses "<repo>:<tag>" / "<repo>@sha256:..." / "<repo>"
// (default tag = latest). Returns ("", "") if ref is empty.
func splitRef(ref string) (string, string) {
	if ref == "" {
		return "", ""
	}
	if idx := strings.LastIndex(ref, "@"); idx >= 0 {
		return ref[:idx], ref[idx+1:]
	}
	if idx := strings.LastIndex(ref, ":"); idx >= 0 {
		// Avoid splitting "host:port/repo".
		if !strings.Contains(ref[idx+1:], "/") {
			return ref[:idx], ref[idx+1:]
		}
	}
	return ref, defaultRefTag
}

// defaultRefTag is the tag used when a ref omits one. Constant
// because it appears in 4+ places and goconst would otherwise flag
// the bare "latest" string.
const defaultRefTag = "latest"
