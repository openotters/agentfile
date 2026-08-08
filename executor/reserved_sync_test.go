//nolint:testpackage // white-box: asserts the unexported reservedRuntimeEnvKeys stays in sync with spec
package executor

import (
	"sort"
	"testing"

	"github.com/openotters/agentfile/spec"
)

// TestReservedEnvKeys_SetEquality guards against drift between the
// runtime filter (reservedRuntimeEnvKeys, which silently drops these
// keys at spawn) and the build-time reject list (spec.ReservedEnvKeys),
// in BOTH directions: a key added to one list but forgotten in the
// other fails here. The two lists live in different packages (executor
// imports spec, not vice versa) and are mirrored by hand — this test is
// the seam that keeps them honest.
func TestReservedEnvKeys_SetEquality(t *testing.T) {
	t.Parallel()

	specKeys := spec.ReservedEnvKeys()

	runtimeKeys := make([]string, 0, len(reservedRuntimeEnvKeys))
	for key := range reservedRuntimeEnvKeys {
		runtimeKeys = append(runtimeKeys, key)
	}

	sort.Strings(runtimeKeys)

	if len(specKeys) != len(runtimeKeys) {
		t.Fatalf("reserved key lists differ: spec=%v executor=%v", specKeys, runtimeKeys)
	}

	for i, key := range specKeys {
		if runtimeKeys[i] != key {
			t.Fatalf("reserved key lists differ: spec=%v executor=%v", specKeys, runtimeKeys)
		}
	}
}

// TestReservedRuntimeEnvKeys_RejectedAtBuild asserts every key the
// runtime would drop at spawn is already rejected by spec.Validate, so
// a bad ENV fails loudly at build instead of vanishing at spawn.
func TestReservedRuntimeEnvKeys_RejectedAtBuild(t *testing.T) {
	t.Parallel()

	for key := range reservedRuntimeEnvKeys {
		af := &spec.Agentfile{Agent: &spec.Agent{
			From: "scratch",
			Envs: []*spec.Env{{Key: key, Value: "x"}},
		}}
		if err := spec.Validate(af); err == nil {
			t.Errorf("ENV %s: runtime-reserved key must be rejected by spec.Validate at build time", key)
		}
	}
}
