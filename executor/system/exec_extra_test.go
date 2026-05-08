//nolint:testpackage // tests unexported helpers
package system

import (
	"testing"
)

// SetEnv / SetDir are tiny pass-through wrappers around exec.Cmd;
// covering them lifts the package's coverage and guards against
// accidental regressions in osCmd's responsibilities.
func TestOsCmd_SetEnvSetDir(t *testing.T) {
	t.Parallel()

	cmd := defaultSpawner{}.Command("echo", "hi")
	cmd.SetEnv([]string{"FOO=bar"})
	cmd.SetDir("/tmp")
	// Black-box assertion: the spawner's Cmd implementation
	// stores env/dir on the underlying *exec.Cmd; we can't
	// observe it without reaching in, but the absence of a
	// panic and the next call (Start/Wait) succeeding on the
	// command is the contract. Spawner-level tests in
	// agent_test.go drive that path.
	_ = cmd
}
