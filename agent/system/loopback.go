package system

import "net"

// LoopbackAllocator reserves a host:port the runtime subprocess will
// bind for its gRPC server. Abstracted behind an interface so tests
// don't have to bind real TCP ports (which causes port conflicts and
// CI flake).
//
// Implementations return absolute addresses like "127.0.0.1:51234".
// The returned port is expected to be free at the moment Reserve
// returns, but the window between Reserve and the runtime's bind is
// inherently racy — good enough for dev; production deployments
// should inherit a listener fd instead.
type LoopbackAllocator interface {
	Reserve() (string, error)
}

// defaultLoopbackAllocator binds a random free port on 127.0.0.1,
// closes the listener, and returns the address. Matches the
// pre-interface behaviour of reserveLoopbackAddr().
type defaultLoopbackAllocator struct{}

func (defaultLoopbackAllocator) Reserve() (string, error) {
	// 0-port reservation is bind+immediate-close; no ctx benefit.
	ln, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // synchronous bind+close
	if err != nil {
		return "", err
	}

	addr := ln.Addr().String()
	_ = ln.Close()

	return addr, nil
}
