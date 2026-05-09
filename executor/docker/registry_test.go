//nolint:testpackage // tests unexported helpers
package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"iter"
	"strings"
	"testing"
	"time"

	dockerimagespec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/jsonstream"
	mobyclient "github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/mock"

	"github.com/openotters/agentfile/executor"
	mockdocker "github.com/openotters/agentfile/mocks/docker"
)

// fakePullPushResponse satisfies the SDK's ImagePullResponse /
// ImagePushResponse interfaces with a plain in-memory body. We
// don't exercise JSONMessages or Wait — the registry only does
// io.Copy(io.Discard, rc) — but the methods exist so the type
// assignable to the interface.
type fakePullPushResponse struct {
	io.ReadCloser
}

func (fakePullPushResponse) JSONMessages(_ context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(_ func(jsonstream.Message, error) bool) {}
}

func (fakePullPushResponse) Wait(_ context.Context) error { return nil }

func newFakeStreamResponse() fakePullPushResponse {
	return fakePullPushResponse{ReadCloser: io.NopCloser(strings.NewReader(`{"status":"ok"}`))}
}

func TestPickLabel(t *testing.T) {
	t.Parallel()

	m := map[string]string{
		"description":                          "fallback desc",
		"org.opencontainers.image.description": "primary desc",
	}

	got := pickLabel(m, ocispec.AnnotationDescription, "description")
	if got != "primary desc" {
		t.Errorf("pickLabel preferred-key = %q, want primary", got)
	}

	got = pickLabel(m, "missing", "description")
	if got != "fallback desc" {
		t.Errorf("pickLabel fallback = %q, want fallback", got)
	}

	if got2 := pickLabel(m, "missing"); got2 != "" {
		t.Errorf("pickLabel none = %q, want empty", got2)
	}
}

func TestParseRFC3339Unix(t *testing.T) {
	t.Parallel()

	// Empty / unparseable surfaces as 0 so callers can detect
	// "no creation time known" with a single == 0 check.
	if got := parseRFC3339Unix(""); got != 0 {
		t.Errorf("empty = %d, want 0", got)
	}
	if got := parseRFC3339Unix("not-a-time"); got != 0 {
		t.Errorf("garbage = %d, want 0", got)
	}

	// Real RFC3339 round-trip.
	ts := "2026-05-08T10:00:00Z"
	want, _ := time.Parse(time.RFC3339, ts)
	if got := parseRFC3339Unix(ts); got != want.Unix() {
		t.Errorf("parseRFC3339Unix(%q) = %d, want %d", ts, got, want.Unix())
	}

	// Nano-precision.
	tsn := "2026-05-08T10:00:00.123456789Z"
	wantN, _ := time.Parse(time.RFC3339Nano, tsn)
	if got := parseRFC3339Unix(tsn); got != wantN.Unix() {
		t.Errorf("nano = %d, want %d", got, wantN.Unix())
	}
}

func TestIsNotFoundErr(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"no such image", errors.New("no such image: foo"), true},
		{"not found", errors.New("Error response from daemon: not found"), true},
		{"dial refused", errors.New("dial unix: connection refused"), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := isNotFoundErr(c.err); got != c.want {
				t.Errorf("isNotFoundErr(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestRegistry_List(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageList(mock.Anything, mock.Anything).
		Return(mobyclient.ImageListResult{
			Items: []image.Summary{
				{ID: "sha256:111", RepoTags: []string{"foo:latest", "foo:v1"}},
				{ID: "sha256:222", RepoTags: []string{"bar:latest"}},
				{ID: "sha256:333", RepoTags: []string{"<none>:<none>"}}, // skipped
				{ID: "sha256:111", RepoTags: []string{"foo:latest"}},    // dup
			},
		}, nil)

	reg := newRegistry(cli)
	refs, err := reg.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{"foo:latest": true, "foo:v1": true, "bar:latest": true}
	if len(refs) != len(want) {
		t.Errorf("got %d refs, want %d: %v", len(refs), len(want), refs)
	}
	for _, r := range refs {
		if !want[r] {
			t.Errorf("unexpected ref %q", r)
		}
	}
}

func TestRegistry_List_Error(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageList(mock.Anything, mock.Anything).
		Return(mobyclient.ImageListResult{}, errors.New("daemon down"))

	reg := newRegistry(cli)
	if _, err := reg.List(context.Background()); err == nil {
		t.Error("expected error")
	}
}

func TestRegistry_Resolve_NotFound(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageList(mock.Anything, mock.Anything).
		Return(mobyclient.ImageListResult{Items: []image.Summary{}}, nil)

	reg := newRegistry(cli)
	if _, err := reg.Resolve(context.Background(), "missing:latest"); !errors.Is(err, executor.ErrRefNotFound) {
		t.Errorf("got %v, want ErrRefNotFound", err)
	}
}

func TestRegistry_Resolve_OK(t *testing.T) {
	t.Parallel()

	const id = "sha256:abc123"
	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageList(mock.Anything, mock.Anything).
		Return(mobyclient.ImageListResult{
			Items: []image.Summary{{ID: id, RepoTags: []string{"foo:latest"}}},
		}, nil)
	cli.EXPECT().
		ImageInspect(mock.Anything, id).
		Return(mobyclient.ImageInspectResult{
			InspectResponse: image.InspectResponse{ID: id, Size: 1234},
		}, nil)

	reg := newRegistry(cli)
	desc, err := reg.Resolve(context.Background(), "foo:latest")
	if err != nil {
		t.Fatal(err)
	}
	if string(desc.Digest) != id {
		t.Errorf("digest = %s, want %s", desc.Digest, id)
	}
	if desc.Size != 1234 {
		t.Errorf("size = %d, want 1234", desc.Size)
	}
}

func TestRegistry_Tag(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageTag(mock.Anything, mobyclient.ImageTagOptions{Source: "src:latest", Target: "dst:latest"}).
		Return(mobyclient.ImageTagResult{}, nil)

	reg := newRegistry(cli)
	if err := reg.Tag(context.Background(), "src:latest", "dst:latest"); err != nil {
		t.Errorf("Tag: %v", err)
	}
}

func TestRegistry_Tag_Error(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageTag(mock.Anything, mock.Anything).
		Return(mobyclient.ImageTagResult{}, errors.New("boom"))

	reg := newRegistry(cli)
	if err := reg.Tag(context.Background(), "src:latest", "dst:latest"); err == nil {
		t.Error("expected error")
	}
}

func TestRegistry_Remove(t *testing.T) {
	t.Parallel()

	const id = "sha256:zzz"
	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageList(mock.Anything, mock.Anything).
		Return(mobyclient.ImageListResult{
			Items: []image.Summary{{ID: id, RepoTags: []string{"victim:latest"}}},
		}, nil)
	cli.EXPECT().
		ImageRemove(mock.Anything, id, mock.Anything).
		Return(mobyclient.ImageRemoveResult{}, nil)

	reg := newRegistry(cli)
	if err := reg.Remove(context.Background(), "victim:latest"); err != nil {
		t.Errorf("Remove: %v", err)
	}
}

func TestRegistry_Remove_NotFound(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageList(mock.Anything, mock.Anything).
		Return(mobyclient.ImageListResult{Items: []image.Summary{}}, nil)

	reg := newRegistry(cli)
	if err := reg.Remove(context.Background(), "ghost:latest"); !errors.Is(err, executor.ErrRefNotFound) {
		t.Errorf("got %v, want ErrRefNotFound", err)
	}
}

func TestRegistry_BuildTarget(t *testing.T) {
	t.Parallel()

	// docker.Store now accepts our Client interface, so
	// BuildTarget always returns a real Store — the mock client
	// drives it just fine.
	cli := mockdocker.NewMockClient(t)
	reg := newRegistry(cli)
	got := reg.BuildTarget()
	if got == nil {
		t.Error("BuildTarget should return a non-nil Store")
	}
	if _, ok := got.(*Store); !ok {
		t.Errorf("BuildTarget = %T, want *Store", got)
	}
}

func TestRegistry_PullRemote(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImagePull(mock.Anything, "foo:latest", mock.Anything).
		Return(newFakeStreamResponse(), nil)

	reg := newRegistry(cli)
	if err := reg.PullRemote(context.Background(), "foo:latest"); err != nil {
		t.Errorf("PullRemote: %v", err)
	}
}

func TestRegistry_PullRemote_Error(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImagePull(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("auth failed"))

	reg := newRegistry(cli)
	if err := reg.PullRemote(context.Background(), "foo:latest"); err == nil {
		t.Error("expected error")
	}
}

func TestRegistry_PushRemote_SameRef(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImagePush(mock.Anything, "foo:latest", mock.Anything).
		Return(newFakeStreamResponse(), nil)

	reg := newRegistry(cli)
	if err := reg.PushRemote(context.Background(), "foo:latest", "foo:latest"); err != nil {
		t.Errorf("PushRemote: %v", err)
	}
}

func TestRegistry_PushRemote_Retags(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageTag(mock.Anything, mobyclient.ImageTagOptions{Source: "local:tag", Target: "remote:tag"}).
		Return(mobyclient.ImageTagResult{}, nil)
	cli.EXPECT().
		ImagePush(mock.Anything, "remote:tag", mock.Anything).
		Return(newFakeStreamResponse(), nil)

	reg := newRegistry(cli)
	if err := reg.PushRemote(context.Background(), "local:tag", "remote:tag"); err != nil {
		t.Errorf("PushRemote retag: %v", err)
	}
}

// Fast path: when the image config carries the openotters
// artifactType label (modern bintool / agent builds stamp it at
// build time), Inspect reads it directly from Config.Labels and
// skips the ImageSave roundtrip entirely.
func TestRegistry_Inspect_FastPathLabel(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageInspect(mock.Anything, "foo:latest").
		Return(mobyclient.ImageInspectResult{
			InspectResponse: image.InspectResponse{
				ID:   "sha256:abc",
				Size: 99,
				Config: &dockerimagespec.DockerOCIImageConfig{
					ImageConfig: ocispec.ImageConfig{
						Labels: map[string]string{
							"io.openotters.artifact-type":          "application/vnd.openotters.bin.v1",
							"org.opencontainers.image.description": "fast-path",
							"org.opencontainers.image.source":      "https://example.test",
						},
					},
				},
			},
		}, nil)
	// Crucially: NO ImageSave expectation. The mock fails the
	// test if Inspect tries to call it.

	reg := newRegistry(cli)
	info, err := reg.Inspect(context.Background(), "foo:latest")
	if err != nil {
		t.Fatal(err)
	}
	if info.MediaType != "application/vnd.openotters.bin.v1" {
		t.Errorf("MediaType = %q, want bin artifactType from label", info.MediaType)
	}
	if info.Description != "fast-path" {
		t.Errorf("Description = %q", info.Description)
	}
	if info.Source != "https://example.test" {
		t.Errorf("Source = %q", info.Source)
	}
}

func TestRegistry_Inspect_FetchesArtifactType(t *testing.T) {
	t.Parallel()

	manifestBlob := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"artifactType": "application/vnd.openotters.bin.v1",
		"annotations": {"org.opencontainers.image.description": "from-manifest"}
	}`)
	manifestDigest := digest.FromBytes(manifestBlob)
	saveTar, err := buildOCILayoutTar("foo:latest", ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    manifestDigest,
		Size:      int64(len(manifestBlob)),
	}, map[digest.Digest][]byte{
		manifestDigest: manifestBlob,
	})
	if err != nil {
		t.Fatalf("buildOCILayoutTar: %v", err)
	}

	cli := mockdocker.NewMockClient(t)
	// Inspect passes the ref directly to ImageInspect — Docker 28+
	// with the containerd snapshotter resolves OCI artifact tags
	// directly. artifactType comes from the manifest blob via
	// ImageSave (the daemon's only API exposing the manifest body).
	cli.EXPECT().
		ImageInspect(mock.Anything, "foo:latest").
		Return(mobyclient.ImageInspectResult{
			InspectResponse: image.InspectResponse{
				ID:      "sha256:abc",
				Size:    42,
				Created: "2026-05-08T10:00:00Z",
			},
		}, nil)
	cli.EXPECT().
		ImageSave(mock.Anything, []string{"foo:latest"}).
		Return(io.NopCloser(bytes.NewReader(saveTar.Bytes())), nil)

	reg := newRegistry(cli)
	info, err := reg.Inspect(context.Background(), "foo:latest")
	if err != nil {
		t.Fatal(err)
	}

	if info.MediaType != "application/vnd.openotters.bin.v1" {
		t.Errorf("MediaType = %q, want bin artifactType", info.MediaType)
	}
	if info.Description != "from-manifest" {
		t.Errorf("Description = %q, want from-manifest", info.Description)
	}
	if info.Size != 42 {
		t.Errorf("Size = %d, want 42", info.Size)
	}
}

func TestRegistry_Inspect_NotFound(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageInspect(mock.Anything, "missing:latest").
		Return(mobyclient.ImageInspectResult{}, errors.New("Error response from daemon: No such image: missing:latest"))

	reg := newRegistry(cli)
	if _, err := reg.Inspect(context.Background(), "missing:latest"); !errors.Is(err, executor.ErrRefNotFound) {
		t.Errorf("got %v, want ErrRefNotFound", err)
	}
}

func TestRegistry_Fetch_NotImplemented(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	reg := newRegistry(cli)
	if _, err := reg.Fetch(context.Background(), ocispec.Descriptor{}); !errors.Is(err, executor.ErrNotImplemented) {
		t.Errorf("got %v, want ErrNotImplemented", err)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"", "", "a"}, "a"},
		{[]string{"a", "b"}, "a"},
		{[]string{"", ""}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.in, "|"), func(t *testing.T) {
			t.Parallel()
			if got := firstNonEmpty(c.in...); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// When the daemon returns no Descriptor (older API or non-manifest
// store), Inspect still returns useful basic info from
// ImageInspect even though it can't surface the artifactType.
func TestRegistry_Inspect_NoDescriptor(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageInspect(mock.Anything, "foo:latest").
		Return(mobyclient.ImageInspectResult{
			InspectResponse: image.InspectResponse{ID: "sha256:abc", Size: 7},
		}, nil)
	// No artifactType available — ImageSave errors out, Inspect
	// degrades to the manifest mediaType fallback.
	cli.EXPECT().
		ImageSave(mock.Anything, []string{"foo:latest"}).
		Return(nil, errors.New("save failed"))

	reg := newRegistry(cli)
	info, err := reg.Inspect(context.Background(), "foo:latest")
	if err != nil {
		t.Fatal(err)
	}
	// MediaType falls back to the manifest mediatype since the
	// daemon didn't surface a Descriptor with artifactType.
	if info.MediaType != ocispec.MediaTypeImageManifest {
		t.Errorf("MediaType = %q, want manifest mediatype", info.MediaType)
	}
}

// Drains a JSON stream like docker's ImagePull progress without
// touching the daemon — exercises the io.Copy(io.Discard, rc) path.
func TestRegistry_PullRemote_DrainsBody(t *testing.T) {
	t.Parallel()

	body := strings.NewReader(strings.Repeat(`{"status":"Downloading"}`+"\n", 100))
	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImagePull(mock.Anything, mock.Anything, mock.Anything).
		Return(fakePullPushResponse{ReadCloser: io.NopCloser(body)}, nil)

	reg := newRegistry(cli)
	if err := reg.PullRemote(context.Background(), "foo:latest"); err != nil {
		t.Errorf("PullRemote drain: %v", err)
	}
}

// Sanity check the Inspect → annotation propagation for the
// description-from-annotations branch (no LABELs, but the manifest
// carries the OCI description annotation).
func TestRegistry_Inspect_DescriptionFromAnnotations(t *testing.T) {
	t.Parallel()

	manifestBlob := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"artifactType": "application/vnd.openotters.agent.v1",
		"annotations": {
			"org.opencontainers.image.description": "agent description",
			"org.opencontainers.image.source": "https://github.com/openotters/openotters"
		}
	}`)
	manifestDigest := digest.FromBytes(manifestBlob)
	saveTar, err := buildOCILayoutTar("foo:latest", ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    manifestDigest,
		Size:      int64(len(manifestBlob)),
	}, map[digest.Digest][]byte{
		manifestDigest: manifestBlob,
	})
	if err != nil {
		t.Fatalf("buildOCILayoutTar: %v", err)
	}

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageInspect(mock.Anything, "foo:latest").
		Return(mobyclient.ImageInspectResult{
			InspectResponse: image.InspectResponse{ID: "sha256:abc"},
		}, nil)
	cli.EXPECT().
		ImageSave(mock.Anything, []string{"foo:latest"}).
		Return(io.NopCloser(bytes.NewReader(saveTar.Bytes())), nil)

	reg := newRegistry(cli)
	info, err := reg.Inspect(context.Background(), "foo:latest")
	if err != nil {
		t.Fatal(err)
	}
	if info.Description != "agent description" {
		t.Errorf("Description = %q, want agent description", info.Description)
	}
	if info.Source != "https://github.com/openotters/openotters" {
		t.Errorf("Source = %q", info.Source)
	}
}
