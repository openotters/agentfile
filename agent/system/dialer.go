package system

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// Dialer opens a gRPC connection to the agent runtime subprocess.
// Abstracted so tests can substitute a bufconn-backed dialer and
// exercise Prompt / PromptObject / ListSessionMessages without
// spawning a real subprocess. The default is defaultDialer, which
// matches the pre-interface behaviour (insecure.Credentials +
// WaitForStateChange within dialReadyTimeout).
type Dialer interface {
	Dial(ctx context.Context, addr string) (*grpc.ClientConn, error)
}

// DialerFunc lets a plain function satisfy Dialer — typical for
// bufconn-backed test dialers where the addr is ignored.
type DialerFunc func(ctx context.Context, addr string) (*grpc.ClientConn, error)

// Dial calls d(ctx, addr).
func (d DialerFunc) Dial(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	return d(ctx, addr)
}

// defaultDialer is the production Dialer: grpc.NewClient +
// WaitForStateChange until Ready or dialReadyTimeout expires.
type defaultDialer struct{}

func (defaultDialer) Dial(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", addr, err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, dialReadyTimeout)
	defer cancel()

	conn.Connect()

	for {
		s := conn.GetState()
		if s == connectivity.Ready {
			return conn, nil
		}

		if !conn.WaitForStateChange(waitCtx, s) {
			_ = conn.Close()

			return nil, fmt.Errorf("runtime at %s not ready within %s: %w",
				addr, dialReadyTimeout, waitCtx.Err())
		}
	}
}
