//nolint:testpackage // tests unexported helpers
package system

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/executor"
)

// embeddedRegistryFake spins up an httptest server that mimics
// the OCI distribution v2 API the embedded registry serves —
// just the catalog, tags/list, manifests endpoints we touch in
// unit tests. Lets us exercise List / Inspect / Remove without
// standing up the real registry code path.
type embeddedRegistryFake struct {
	server     *httptest.Server
	repos      []string
	tagsByRepo map[string][]string
	manifests  map[string]map[string][]byte // repo -> tag -> bytes
	deletes    []string                     // repo:digest
}

func newEmbeddedRegistryFake(t *testing.T) *embeddedRegistryFake {
	t.Helper()
	f := &embeddedRegistryFake{
		repos:      []string{"foo", "bar"},
		tagsByRepo: map[string][]string{"foo": {"latest", "v1"}, "bar": {"latest"}},
		manifests:  map[string]map[string][]byte{},
	}

	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/_catalog":
			data := struct {
				Repositories []string `json:"repositories"`
			}{Repositories: f.repos}
			_ = json.NewEncoder(w).Encode(data)

		case r.Method == http.MethodGet && hasSuffix(r.URL.Path, "/tags/list"):
			repo := stripV2(r.URL.Path, "/tags/list")
			data := struct {
				Name string   `json:"name"`
				Tags []string `json:"tags"`
			}{Name: repo, Tags: f.tagsByRepo[repo]}
			_ = json.NewEncoder(w).Encode(data)

		case r.Method == http.MethodGet && contains(r.URL.Path, "/manifests/"):
			repo, tag := splitManifestPath(r.URL.Path)
			body, ok := f.manifests[repo][tag]
			if !ok {
				http.NotFound(w, r)
				return
			}
			d := digest.FromBytes(body)
			w.Header().Set("Docker-Content-Digest", d.String())
			w.Header().Set("Content-Type", v1.MediaTypeImageManifest)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)

		case r.Method == http.MethodDelete && contains(r.URL.Path, "/manifests/"):
			repo, ref := splitManifestPath(r.URL.Path)
			f.deletes = append(f.deletes, repo+":"+ref)
			w.WriteHeader(http.StatusAccepted)

		default:
			http.NotFound(w, r)
		}
	}))

	t.Cleanup(f.server.Close)
	return f
}

//nolint:unparam // repo varies in future tests; method accepts it as a contract
func (f *embeddedRegistryFake) addManifest(repo, tag string, body []byte) {
	if f.manifests[repo] == nil {
		f.manifests[repo] = map[string][]byte{}
	}
	f.manifests[repo][tag] = body
}

func (f *embeddedRegistryFake) addr() string {
	// httptest server URL is http://host:port — strip the scheme
	// since CreatedAtFunc / addr arguments expect host:port only.
	u := f.server.URL
	if len(u) > 7 && u[:7] == "http://" {
		return u[7:]
	}
	return u
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func stripV2(path, suffix string) string {
	const v2 = "/v2/"
	if len(path) > len(v2) && path[:len(v2)] == v2 {
		return path[len(v2) : len(path)-len(suffix)]
	}
	return path
}

func splitManifestPath(path string) (string, string) {
	const m = "/manifests/"
	const v2 = "/v2/"
	idx := indexOf(path, m)
	if idx < 0 || len(path) <= idx+len(m) {
		return "", ""
	}
	repo := path[len(v2):idx]
	ref := path[idx+len(m):]
	return repo, ref
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------

func TestNewRegistry(t *testing.T) {
	t.Parallel()

	target := memory.New()
	r := NewRegistry(target, "127.0.0.1:5051", nil)
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}
}

func TestRegistry_List(t *testing.T) {
	t.Parallel()

	f := newEmbeddedRegistryFake(t)
	r := NewRegistry(memory.New(), f.addr(), nil)

	refs, err := r.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{"foo:latest": true, "foo:v1": true, "bar:latest": true}
	for _, ref := range refs {
		if !want[ref] {
			t.Errorf("unexpected ref %q in %v", ref, refs)
		}
	}
	if len(refs) != len(want) {
		t.Errorf("got %d refs, want %d", len(refs), len(want))
	}
}

func TestRegistry_List_NoAddr(t *testing.T) {
	t.Parallel()

	r := NewRegistry(memory.New(), "", nil)
	if _, err := r.List(context.Background()); !errors.Is(err, executor.ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented, got %v", err)
	}
}

func TestRegistry_Inspect(t *testing.T) {
	t.Parallel()

	f := newEmbeddedRegistryFake(t)

	manifest := v1.Manifest{
		MediaType:    v1.MediaTypeImageManifest,
		ArtifactType: "application/vnd.openotters.agent.v1",
		Config: v1.Descriptor{
			MediaType: "application/vnd.openotters.agent.config.v1+json",
			Digest:    digest.FromBytes([]byte("{}")),
			Size:      2,
		},
		Layers: []v1.Descriptor{
			{
				MediaType: "application/vnd.openotters.context.v1",
				Digest:    digest.FromBytes([]byte("layer")),
				Size:      5,
			},
		},
		Annotations: map[string]string{
			v1.AnnotationDescription: "test agent",
		},
	}
	body, _ := json.Marshal(manifest)
	f.addManifest("foo", "latest", body)

	r := NewRegistry(memory.New(), f.addr(), nil)
	info, err := r.Inspect(context.Background(), "foo:latest")
	if err != nil {
		t.Fatal(err)
	}

	if info.Description != "test agent" {
		t.Errorf("Description = %q, want test agent", info.Description)
	}
	if info.Size == 0 {
		t.Errorf("Size should be non-zero")
	}

	// MediaType is intentionally not surfaced by Inspect anymore;
	// kind classification flows through ManifestKind and the
	// daemon's image_kinds index.
	if info.MediaType != "" {
		t.Errorf("MediaType = %q, want empty (use ManifestKind)", info.MediaType)
	}

	kind, kindErr := r.ManifestKind(context.Background(), "foo:latest")
	if kindErr != nil {
		t.Fatalf("ManifestKind: %v", kindErr)
	}
	if kind != "application/vnd.openotters.agent.v1" {
		t.Errorf("ManifestKind = %q, want agent.v1", kind)
	}
}

func TestRegistry_Inspect_NoAddr(t *testing.T) {
	t.Parallel()

	r := NewRegistry(memory.New(), "", nil)
	if _, err := r.Inspect(context.Background(), "foo:latest"); !errors.Is(err, executor.ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented, got %v", err)
	}
}

func TestRegistry_Resolve_NoTarget(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil, "host:port", nil)
	if _, err := r.Resolve(context.Background(), "foo:latest"); !errors.Is(err, executor.ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented, got %v", err)
	}
}

func TestRegistry_Resolve_OK(t *testing.T) {
	t.Parallel()

	target := memory.New()
	body := []byte(`{"schemaVersion":2}`)
	desc := v1.Descriptor{
		MediaType: v1.MediaTypeImageManifest,
		Digest:    digest.FromBytes(body),
		Size:      int64(len(body)),
	}
	if err := target.Push(context.Background(), desc, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err := target.Tag(context.Background(), desc, "foo:latest"); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry(target, "host:port", nil)
	got, err := r.Resolve(context.Background(), "foo:latest")
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != desc.Digest {
		t.Errorf("digest = %s, want %s", got.Digest, desc.Digest)
	}
}

func TestRegistry_Fetch_NoTarget(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil, "host:port", nil)
	if _, err := r.Fetch(context.Background(), v1.Descriptor{}); !errors.Is(err, executor.ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented, got %v", err)
	}
}

func TestRegistry_Tag_NoTarget(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil, "host:port", nil)
	if err := r.Tag(context.Background(), "src", "dst"); !errors.Is(err, executor.ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented, got %v", err)
	}
}

func TestRegistry_Tag(t *testing.T) {
	t.Parallel()

	target := memory.New()
	body := []byte(`{"schemaVersion":2}`)
	desc := v1.Descriptor{
		MediaType: v1.MediaTypeImageManifest,
		Digest:    digest.FromBytes(body),
		Size:      int64(len(body)),
	}
	_ = target.Push(context.Background(), desc, bytes.NewReader(body))
	_ = target.Tag(context.Background(), desc, "src:latest")

	r := NewRegistry(target, "host:port", nil)
	if err := r.Tag(context.Background(), "src:latest", "dst:latest"); err != nil {
		t.Fatal(err)
	}

	// Both src and dst tags resolve to the same desc.
	got, err := target.Resolve(context.Background(), "dst:latest")
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != desc.Digest {
		t.Errorf("dst:latest digest = %s, want %s", got.Digest, desc.Digest)
	}
}

func TestRegistry_Remove(t *testing.T) {
	t.Parallel()

	f := newEmbeddedRegistryFake(t)
	body := []byte(`{"schemaVersion":2}`)
	f.addManifest("foo", "latest", body)

	r := NewRegistry(memory.New(), f.addr(), nil)
	if err := r.Remove(context.Background(), "foo:latest"); err != nil {
		t.Fatal(err)
	}

	if len(f.deletes) == 0 {
		t.Error("expected DELETE to fire on the embedded registry")
	}
}

func TestRegistry_Remove_NoAddr(t *testing.T) {
	t.Parallel()

	r := NewRegistry(memory.New(), "", nil)
	if err := r.Remove(context.Background(), "foo:latest"); !errors.Is(err, executor.ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented, got %v", err)
	}
}

func TestRegistry_BuildTarget(t *testing.T) {
	t.Parallel()

	target := memory.New()
	r := NewRegistry(target, "host:port", nil)
	if r.BuildTarget() != target {
		t.Error("BuildTarget should return the underlying target")
	}
}

func TestRegistry_PullRemote_NoAddr(t *testing.T) {
	t.Parallel()

	r := NewRegistry(memory.New(), "", nil)
	if err := r.PullRemote(context.Background(), "ghcr.io/foo:latest"); !errors.Is(err, executor.ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented, got %v", err)
	}
}

func TestRegistry_PushRemote_NoAddr(t *testing.T) {
	t.Parallel()

	r := NewRegistry(memory.New(), "", nil)
	if err := r.PushRemote(context.Background(), "src:latest", "dst:latest"); !errors.Is(err, executor.ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented, got %v", err)
	}
}

func TestPickAnnotation(t *testing.T) {
	t.Parallel()

	m := map[string]string{
		"description":                          "fallback",
		"org.opencontainers.image.description": "primary",
	}

	got := pickAnnotation(m, v1.AnnotationDescription, "description")
	if got != "primary" {
		t.Errorf("got %q, want primary", got)
	}

	got = pickAnnotation(m, "missing", "description")
	if got != "fallback" {
		t.Errorf("got %q, want fallback", got)
	}

	if got2 := pickAnnotation(map[string]string{}, "missing"); got2 != "" {
		t.Errorf("empty got %q", got2)
	}
}

func TestSplitRef(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		repo string
		tag  string
	}{
		{"foo:latest", "foo", "latest"},
		{"foo", "foo", "latest"},
		{"127.0.0.1:5000/foo:tag", "127.0.0.1:5000/foo", "tag"},
		{"foo@sha256:abc", "foo", "sha256:abc"},
		{"", "", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			repo, tag := splitRef(c.in)
			if repo != c.repo || tag != c.tag {
				t.Errorf("splitRef(%q) = (%q,%q), want (%q,%q)", c.in, repo, tag, c.repo, c.tag)
			}
		})
	}
}

func TestRegistry_Inspect_Index(t *testing.T) {
	t.Parallel()

	f := newEmbeddedRegistryFake(t)

	// Multi-arch index → fetchManifestInfo recurses to sum
	// platform sizes. We add a single child manifest to the
	// fake registry so the recursion has something to fetch.
	childManifest := v1.Manifest{
		MediaType:    v1.MediaTypeImageManifest,
		ArtifactType: "application/vnd.openotters.bin.v1",
		Config: v1.Descriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    digest.FromBytes([]byte("config")),
			Size:      6,
		},
		Layers: []v1.Descriptor{
			{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				Digest:    digest.FromBytes([]byte("layer")),
				Size:      5,
			},
		},
	}
	childBody, _ := json.Marshal(childManifest)
	childDigest := digest.FromBytes(childBody)
	f.addManifest("foo", childDigest.String(), childBody)

	index := v1.Index{
		MediaType:    v1.MediaTypeImageIndex,
		ArtifactType: "application/vnd.openotters.bin.v1",
		Manifests: []v1.Descriptor{
			{
				MediaType: v1.MediaTypeImageManifest,
				Digest:    childDigest,
				Size:      int64(len(childBody)),
				Platform: &v1.Platform{
					OS:           "linux",
					Architecture: "arm64",
				},
			},
		},
		Annotations: map[string]string{
			v1.AnnotationDescription: "from-index",
		},
	}
	indexBody, _ := json.Marshal(index)
	f.addManifest("foo", "latest", indexBody)

	r := NewRegistry(memory.New(), f.addr(), nil)
	info, err := r.Inspect(context.Background(), "foo:latest")
	if err != nil {
		t.Fatal(err)
	}
	if info.Description != "from-index" {
		t.Errorf("description = %q", info.Description)
	}

	kind, kindErr := r.ManifestKind(context.Background(), "foo:latest")
	if kindErr != nil {
		t.Fatalf("ManifestKind: %v", kindErr)
	}
	if kind != "application/vnd.openotters.bin.v1" {
		t.Errorf("ManifestKind = %q", kind)
	}
}

func TestRegistry_Inspect_FetchError(t *testing.T) {
	t.Parallel()

	// Server returns 404 for any manifest path → Inspect surfaces
	// the error.
	f := &embeddedRegistryFake{manifests: map[string]map[string][]byte{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	t.Cleanup(f.server.Close)

	r := NewRegistry(memory.New(), f.addr(), nil)
	_, err := r.Inspect(context.Background(), "foo:latest")
	if err == nil {
		t.Error("expected error on 404")
	}
}

// Hits the createdAt callback path — exercises the
// CreatedAtFunc parameter that returns a timestamp the daemon
// resolves from on-disk manifest mtimes.
func TestRegistry_Inspect_WithCreatedAt(t *testing.T) {
	t.Parallel()

	f := newEmbeddedRegistryFake(t)

	manifest := v1.Manifest{
		MediaType: v1.MediaTypeImageManifest,
		Layers:    []v1.Descriptor{{Size: 10}},
	}
	body, _ := json.Marshal(manifest)
	f.addManifest("foo", "latest", body)

	called := false
	createdAt := func(repo, tag string) int64 {
		called = true
		if repo != "foo" || tag != "latest" {
			t.Errorf("createdAt got (%q, %q)", repo, tag)
		}
		return 1234567890
	}

	r := NewRegistry(memory.New(), f.addr(), createdAt)
	info, err := r.Inspect(context.Background(), "foo:latest")
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("createdAt not called")
	}
	if info.CreatedUnix != 1234567890 {
		t.Errorf("CreatedUnix = %d", info.CreatedUnix)
	}
}

// Sanity-check the option setters that thread Registry deps onto
// the Provider — they're simple field assignments but still
// counted toward coverage.
func TestProvider_RegistryOptions(t *testing.T) {
	t.Parallel()

	p := &Provider{}
	target := memory.New()

	WithRegistryTarget(target)(p)
	if p.registryTarget != target {
		t.Error("WithRegistryTarget didn't set field")
	}

	WithRegistryAddr("host:port")(p)
	if p.registryAddr != "host:port" {
		t.Errorf("WithRegistryAddr = %q", p.registryAddr)
	}

	WithRegistryCreatedAt(func(_, _ string) int64 { return 7 })(p)
	if p.registryCreatedAt == nil {
		t.Error("WithRegistryCreatedAt didn't set field")
	}
}

func TestProvider_Registry(t *testing.T) {
	t.Parallel()

	p := &Provider{}
	r1 := p.Registry()
	r2 := p.Registry()
	if r1 != r2 {
		t.Error("Registry should be cached")
	}
}
