package system_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/google/uuid"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/executor/system"
	mocksystem "github.com/openotters/agentfile/mocks/system"
	"github.com/openotters/agentfile/spec"
)

func TestDefaultLoopbackAllocator_ReservesFreePort(t *testing.T) {
	t.Parallel()

	// We can't directly instantiate defaultLoopbackAllocator from an
	// external test (it's unexported), but NewProvider installs it by
	// default — so exercising Provider.CreateWithOptions covers the
	// default path. Observable outcome: the returned addr parses as
	// a 127.0.0.1:<port> pair with a non-zero port.
	p := system.NewProvider(memfs.New(), func(spec.Reference) oras.ReadOnlyTarget { return memory.New() })

	a, err := p.CreateWithOptions(context.Background(), uuid.New(), spec.Reference{Name: "test"}, nil)
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}

	sysAgent, ok := a.(*system.Agent)
	if !ok {
		t.Fatalf("Provider.Create returned %T, want *system.Agent", a)
	}

	addr := sysAgent.Addr()
	if addr == "" {
		t.Fatal("default allocator returned empty addr")
	}

	host, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		t.Fatalf("addr %q is not host:port: %v", addr, splitErr)
	}

	if host != "127.0.0.1" {
		t.Fatalf("expected loopback host, got %q", host)
	}

	if port == "0" {
		t.Fatalf("expected concrete port, got %q", port)
	}
}

func TestWithLoopbackAllocator_MockInjection(t *testing.T) {
	t.Parallel()

	mockAlloc := mocksystem.NewMockLoopbackAllocator(t)
	mockAlloc.EXPECT().Reserve().Return("127.0.0.1:9999", nil).Once()

	p := system.NewProvider(
		memfs.New(),
		func(spec.Reference) oras.ReadOnlyTarget { return memory.New() },
		system.WithLoopbackAllocator(mockAlloc),
	)

	a, err := p.CreateWithOptions(context.Background(), uuid.New(), spec.Reference{Name: "test"}, nil)
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}

	sysAgent, ok := a.(*system.Agent)
	if !ok {
		t.Fatalf("Provider.Create returned %T, want *system.Agent", a)
	}

	if got := sysAgent.Addr(); got != "127.0.0.1:9999" {
		t.Fatalf("Addr() = %q, want injected 127.0.0.1:9999", got)
	}
}

func TestWithLoopbackAllocator_ErrorPropagates(t *testing.T) {
	t.Parallel()

	mockAlloc := mocksystem.NewMockLoopbackAllocator(t)
	mockAlloc.EXPECT().Reserve().Return("", errors.New("no ports left")).Once()

	p := system.NewProvider(
		memfs.New(),
		func(spec.Reference) oras.ReadOnlyTarget { return memory.New() },
		system.WithLoopbackAllocator(mockAlloc),
	)

	_, err := p.CreateWithOptions(context.Background(), uuid.New(), spec.Reference{Name: "test"}, nil)
	if err == nil {
		t.Fatal("CreateWithOptions = nil err, want allocator error")
	}

	// Error is wrapped with context — check the wrapper and the inner
	// message both surface.
	msg := err.Error()
	if !strings.Contains(msg, "reserving runtime address") || !strings.Contains(msg, "no ports left") {
		t.Fatalf("err = %q; expected wrapped allocator error", msg)
	}
}
