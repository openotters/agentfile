package executor

import "errors"

// ErrRefNotFound is returned by Registry.Resolve / Registry.Inspect
// when the requested ref is not known to the backend. Use
// errors.Is(err, executor.ErrRefNotFound) to test for it; concrete
// implementations may wrap it with backend-specific context.
var ErrRefNotFound = errors.New("ref not found")

// ErrNotImplemented is returned by Registry methods that aren't yet
// supported on a given backend. Used by the Docker registry stub
// during the Docker-executor rollout (BuildTarget returns nil,
// other write methods that depend on the containerd snapshotter
// being in place may return this until step 5 lands).
var ErrNotImplemented = errors.New("not implemented")
