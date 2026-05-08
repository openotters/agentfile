//nolint:testpackage // tests unexported helpers
package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/system"
	mobyclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/mock"

	mockdocker "github.com/openotters/agentfile/mocks/docker"
)

func TestParseEngineMajor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"28.0.0", 28, false},
		{"v28.0.0", 28, false},
		{"  29.2.1 ", 29, false},
		{"30.5.10-rc1", 30, false},
		{"bogus", 0, true},
		{"", 0, true},
		{"28", 0, true}, // no dot
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			got, err := parseEngineMajor(c.in)
			if (err != nil) != c.wantErr {
				t.Errorf("parseEngineMajor(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("parseEngineMajor(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestHasContainerdSnapshotter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   [][2]string
		want bool
	}{
		{
			name: "containerd snapshotter present",
			in: [][2]string{
				{"driver-type", "io.containerd.snapshotter.v1"},
			},
			want: true,
		},
		{
			name: "classic graphdriver",
			in: [][2]string{
				{"Backing Filesystem", "extfs"},
				{"Supports d_type", "true"},
			},
			want: false,
		},
		{
			name: "empty",
			in:   nil,
			want: false,
		},
		{
			name: "containerd value but wrong key",
			in: [][2]string{
				{"otherkey", "io.containerd.snapshotter.v1"},
			},
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := hasContainerdSnapshotter(c.in); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestVerify_DaemonUnreachable(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ServerVersion(mock.Anything, mock.Anything).
		Return(mobyclient.ServerVersionResult{}, errors.New("dial unix: no such file"))

	err := Verify(context.Background(), cli)
	if !errors.Is(err, ErrDaemonUnreachable) {
		t.Errorf("expected ErrDaemonUnreachable, got %v", err)
	}
}

func TestVerify_EngineTooOld(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ServerVersion(mock.Anything, mock.Anything).
		Return(mobyclient.ServerVersionResult{
			Version: "27.4.0",
		}, nil)

	err := Verify(context.Background(), cli)
	if !errors.Is(err, ErrEngineTooOld) {
		t.Errorf("expected ErrEngineTooOld, got %v", err)
	}
}

func TestVerify_NoSnapshotter(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ServerVersion(mock.Anything, mock.Anything).
		Return(mobyclient.ServerVersionResult{
			Version: "29.0.0",
		}, nil)
	cli.EXPECT().
		Info(mock.Anything, mock.Anything).
		Return(mobyclient.SystemInfoResult{
			Info: system.Info{
				DriverStatus: [][2]string{
					{"Backing Filesystem", "extfs"},
				},
			},
		}, nil)

	err := Verify(context.Background(), cli)
	if !errors.Is(err, ErrNoSnapshotter) {
		t.Errorf("expected ErrNoSnapshotter, got %v", err)
	}
}

func TestVerify_OK(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ServerVersion(mock.Anything, mock.Anything).
		Return(mobyclient.ServerVersionResult{
			Version: "29.2.1",
		}, nil)
	cli.EXPECT().
		Info(mock.Anything, mock.Anything).
		Return(mobyclient.SystemInfoResult{
			Info: system.Info{
				DriverStatus: [][2]string{
					{"driver-type", "io.containerd.snapshotter.v1"},
				},
			},
		}, nil)

	if err := Verify(context.Background(), cli); err != nil {
		t.Errorf("Verify ok path returned %v", err)
	}
}
