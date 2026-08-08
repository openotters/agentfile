//nolint:testpackage // tests unexported helpers
package oci

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// AgentFetcher / RemotePuller are constructor closures — covering
// them is just "construction returns a non-nil callable". The
// behaviour they wrap is exercised in the integration paths.

func TestAgentFetcher_Constructable(t *testing.T) {
	t.Parallel()

	if AgentFetcher() == nil {
		t.Error("AgentFetcher returned nil")
	}
}

func TestRemotePuller_Constructable(t *testing.T) {
	t.Parallel()

	if RemotePuller(HostPlatform()) == nil {
		t.Error("RemotePuller returned nil")
	}
}

// retryTransport wraps an http.RoundTripper with backoff. Cover
// the happy path (200) — the body-rewinding / 429-retry paths are
// exercised by oras-go integration tests upstream.
func TestRetryTransport_Smoke(t *testing.T) {
	t.Parallel()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rt := withRetry(http.DefaultTransport)

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if calls == 0 {
		t.Error("retryTransport never reached the upstream")
	}
}

func TestWithRetry_NilInner(t *testing.T) {
	t.Parallel()

	// withRetry(nil) substitutes http.DefaultTransport so the
	// returned RoundTripper is always non-nil and usable.
	rt := withRetry(nil)
	if rt == nil {
		t.Error("withRetry(nil) returned nil")
	}
}
