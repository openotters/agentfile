package oci

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v5"
)

// DefaultRetryMaxElapsedTime caps the total wall-clock time the retry
// transport will spend on a single HTTP request, including all backoff
// sleeps. 30 s comfortably covers the GHCR "first-push" 500 race
// (typically 1–5 s) without making bad-credential errors feel sluggish
// — the auth path 401s fast and 4xx isn't retried.
const DefaultRetryMaxElapsedTime = 30 * time.Second

// retryTransport wraps an http.RoundTripper with cenkalti/backoff retry
// on transport errors and 5xx responses. 2xx, 3xx, and 4xx responses
// pass through unchanged.
//
// Why retry pushes at all: GHCR (and a few other registry backends)
// provision a brand-new package's storage lazily on the first manifest
// PUT. The very first push of a never-before-seen `<owner>/<name>:tag`
// can race the provisioning and 500. Subsequent attempts succeed once
// the backend is ready. Without retry, our automation has to do that
// by hand. With a 1 s / 3 s / 9 s exponential backoff the recovery is
// invisible to the caller.
//
// Body handling: HTTP retry of body-bearing requests is only safe when
// the body can be re-read. http.NewRequestWithContext sets GetBody
// automatically for *bytes.Reader, *bytes.Buffer, and *strings.Reader,
// which is what oras-go uses for blobs and manifests. When GetBody is
// missing on a body-bearing request, we degrade to a single attempt
// rather than silently corrupting the wire.
type retryTransport struct {
	inner          http.RoundTripper
	maxElapsedTime time.Duration
}

// withRetry wraps the given RoundTripper. Inner can be nil; in that
// case http.DefaultTransport is used.
func withRetry(inner http.RoundTripper) http.RoundTripper {
	if inner == nil {
		inner = http.DefaultTransport
	}

	return &retryTransport{inner: inner, maxElapsedTime: DefaultRetryMaxElapsedTime}
}

func (rt *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	canRetry := req.Body == nil || req.GetBody != nil

	op := func() (*http.Response, error) {
		// Reset body for every attempt so retries don't send an empty
		// payload after the first read.
		if req.Body != nil && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, backoff.Permanent(fmt.Errorf("rewinding body: %w", err))
			}

			req.Body = body
		}

		resp, err := rt.inner.RoundTrip(req)
		if err != nil {
			// Transport error: retry unless the body can't be replayed.
			if !canRetry {
				return nil, backoff.Permanent(err)
			}

			return nil, err
		}

		if resp.StatusCode >= http.StatusInternalServerError {
			// 5xx: drain + close so the connection can be reused, then
			// retry. If the body can't be replayed, surface the response
			// as-is — the caller still sees a real status.
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			if !canRetry {
				return resp, backoff.Permanent(
					fmt.Errorf("registry returned %d (body not retryable)", resp.StatusCode))
			}

			return nil, fmt.Errorf("registry returned %d", resp.StatusCode)
		}

		// 2xx / 3xx / 4xx: pass straight through.
		return resp, nil
	}

	return backoff.Retry(req.Context(), op,
		backoff.WithBackOff(backoff.NewExponentialBackOff()),
		backoff.WithMaxElapsedTime(rt.maxElapsedTime),
	)
}
