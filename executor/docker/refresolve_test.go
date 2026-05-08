//nolint:testpackage // tests unexported helpers
package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/image"
	mobyclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/mock"

	mockdocker "github.com/openotters/agentfile/mocks/docker"
)

func TestIsLikelyImageID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ref  string
		want bool
	}{
		{"sha256:abcdef0123456789", true},
		{"sha256:", false}, // exactly the prefix, no payload
		{"abc123", false},
		{"ghcr.io/openotters/runtime:latest", false},
		{"", false},
	}

	for _, c := range cases {
		t.Run(c.ref, func(t *testing.T) {
			t.Parallel()
			if got := isLikelyImageID(c.ref); got != c.want {
				t.Errorf("isLikelyImageID(%q) = %v, want %v", c.ref, got, c.want)
			}
		})
	}
}

func TestResolveImageID_FastPath(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	// No ImageList expectation — sha256: refs return verbatim.

	id, err := resolveImageID(context.Background(), cli, "sha256:deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if id != "sha256:deadbeef" {
		t.Errorf("got %q, want sha256:deadbeef", id)
	}
}

func TestResolveImageID_ListMatch(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageList(mock.Anything, mock.Anything).
		Return(mobyclient.ImageListResult{
			Items: []image.Summary{
				{ID: "sha256:abc123", RepoTags: []string{"ghcr.io/openotters/foo:latest"}},
				{ID: "sha256:def456", RepoTags: []string{"alpine:latest"}},
			},
		}, nil)

	id, err := resolveImageID(context.Background(), cli, "alpine:latest")
	if err != nil {
		t.Fatal(err)
	}
	if id != "sha256:def456" {
		t.Errorf("got %q, want sha256:def456", id)
	}
}

func TestResolveImageID_NotFound(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageList(mock.Anything, mock.Anything).
		Return(mobyclient.ImageListResult{Items: []image.Summary{}}, nil)

	id, err := resolveImageID(context.Background(), cli, "missing:latest")
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Errorf("got %q, want empty", id)
	}
}

func TestResolveImageID_ListError(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageList(mock.Anything, mock.Anything).
		Return(mobyclient.ImageListResult{}, errors.New("daemon down"))

	if _, err := resolveImageID(context.Background(), cli, "anything:latest"); err == nil {
		t.Error("expected error, got nil")
	}
}
