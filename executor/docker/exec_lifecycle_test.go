//nolint:testpackage // exercises unexported async-exec internals
package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/mock"

	"github.com/openotters/agentfile/executor"
	mockdocker "github.com/openotters/agentfile/mocks/docker"
)

// muxFrame wraps payload in docker's multiplexed log framing
// (stream id + big-endian length header) so dockerStdCopy can
// demultiplex it like a real daemon stream.
func muxFrame(stream byte, payload string) []byte {
	var hdr [8]byte
	hdr[0] = stream
	binary.BigEndian.PutUint32(hdr[4:8], uint32(len(payload)))
	return append(hdr[:], payload...)
}

func TestDockerStdCopy(t *testing.T) {
	t.Parallel()

	var src bytes.Buffer
	src.Write(muxFrame(1, "to stdout"))
	src.Write(muxFrame(2, "to stderr"))
	src.Write(muxFrame(1, " more"))

	var stdout, stderr bytes.Buffer
	if err := dockerStdCopy(&stdout, &stderr, &src); err != nil {
		t.Fatalf("dockerStdCopy: %v", err)
	}

	if stdout.String() != "to stdout more" || stderr.String() != "to stderr" {
		t.Errorf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// execReadyAgent returns an agent whose runtime descriptor declares
// one BIN, ready for Exec calls.
func execReadyAgent(t *testing.T, cli *mockdocker.MockClient) *Agent {
	t.Helper()

	a := newAgent(lifecycleDeps(t, cli))
	a.mu.Lock()
	a.rt = &executor.Runtime{ResolvedConfig: executor.ResolvedConfig{
		Model: "anthropic/m",
		Tools: []executor.ResolvedTool{{Name: "jq", Ref: "ghcr.io/openotters/tools/jq:latest"}},
	}}
	a.initialized = true
	a.mu.Unlock()

	return a
}

func TestAgent_Exec_HappyPath(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	a := execReadyAgent(t, cli)

	cli.EXPECT().ContainerCreate(mock.Anything, mock.Anything).
		Return(mobyclient.ContainerCreateResult{ID: "job-1"}, nil).Once()
	cli.EXPECT().ContainerStart(mock.Anything, "job-1", mock.Anything).
		Return(mobyclient.ContainerStartResult{}, nil).Once()

	var logs bytes.Buffer
	logs.Write(muxFrame(1, `{"ok":true}`))
	logs.Write(muxFrame(2, "warning: x"))
	cli.EXPECT().ContainerLogs(mock.Anything, "job-1", mock.Anything).
		Return(io.NopCloser(&logs), nil).Once()

	cli.EXPECT().ContainerInspect(mock.Anything, "job-1", mock.Anything).
		Return(mobyclient.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				State: &containertypes.State{ExitCode: 0},
			},
		}, nil).Once()
	cli.EXPECT().ContainerRemove(mock.Anything, "job-1", mock.Anything).
		Return(mobyclient.ContainerRemoveResult{}, nil).Once()

	res := a.Exec(context.Background(), "jq", []string{".ok"}, "")
	if res.Err != nil {
		t.Fatalf("Exec: %v", res.Err)
	}

	if res.Stdout != `{"ok":true}` || res.Stderr != "warning: x" || res.ExitCode != 0 {
		t.Errorf("res = %+v", res)
	}
}

func TestAgent_Exec_NonZeroExit(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	a := execReadyAgent(t, cli)

	cli.EXPECT().ContainerCreate(mock.Anything, mock.Anything).
		Return(mobyclient.ContainerCreateResult{ID: "job-2"}, nil).Once()
	cli.EXPECT().ContainerStart(mock.Anything, "job-2", mock.Anything).
		Return(mobyclient.ContainerStartResult{}, nil).Once()
	cli.EXPECT().ContainerLogs(mock.Anything, "job-2", mock.Anything).
		Return(io.NopCloser(bytes.NewReader(muxFrame(2, "boom"))), nil).Once()
	cli.EXPECT().ContainerInspect(mock.Anything, "job-2", mock.Anything).
		Return(mobyclient.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				State: &containertypes.State{ExitCode: 3},
			},
		}, nil).Once()
	cli.EXPECT().ContainerRemove(mock.Anything, "job-2", mock.Anything).
		Return(mobyclient.ContainerRemoveResult{}, nil).Once()

	res := a.Exec(context.Background(), "jq", nil, "")
	if res.Err != nil || res.ExitCode != 3 || res.Stderr != "boom" {
		t.Errorf("res = %+v, want exit 3 with stderr", res)
	}
}

func TestAgent_Exec_FailFast(t *testing.T) {
	t.Parallel()

	t.Run("uninitialized", func(t *testing.T) {
		t.Parallel()

		cli := mockdocker.NewMockClient(t)
		a := newAgent(lifecycleDeps(t, cli))

		res := a.Exec(context.Background(), "jq", nil, "")
		if res.Err == nil || !strings.Contains(res.Err.Error(), "not initialized") {
			t.Fatalf("err = %v", res.Err)
		}
	})

	t.Run("undeclared bin", func(t *testing.T) {
		t.Parallel()

		cli := mockdocker.NewMockClient(t)
		a := execReadyAgent(t, cli)

		res := a.Exec(context.Background(), "curl", nil, "")
		if res.Err == nil || !strings.Contains(res.Err.Error(), "not declared") {
			t.Fatalf("err = %v", res.Err)
		}
	})

	t.Run("create failure", func(t *testing.T) {
		t.Parallel()

		cli := mockdocker.NewMockClient(t)
		a := execReadyAgent(t, cli)

		cli.EXPECT().ContainerCreate(mock.Anything, mock.Anything).
			Return(mobyclient.ContainerCreateResult{}, errors.New("no space")).Once()

		res := a.Exec(context.Background(), "jq", nil, "")
		if res.Err == nil || !strings.Contains(res.Err.Error(), "create") {
			t.Fatalf("err = %v", res.Err)
		}
	})
}

func TestBinDeclaredHelpers(t *testing.T) {
	t.Parallel()

	rt := &executor.Runtime{ResolvedConfig: executor.ResolvedConfig{
		Tools: []executor.ResolvedTool{{Name: "wget"}, {Name: "jq"}},
	}}

	if !binDeclared(rt, "wget") || binDeclared(rt, "nope") {
		t.Error("binDeclared verdicts wrong")
	}

	if got := declaredBinNames(rt); !strings.Contains(got, "jq") || !strings.Contains(got, "wget") {
		t.Errorf("declaredBinNames = %q", got)
	}
}
