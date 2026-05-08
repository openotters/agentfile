//nolint:testpackage // direct internal access for envIndex helper
package system

import "testing"

func TestEnvIndex_Helper(t *testing.T) {
	t.Parallel()

	env := []string{"FOO=1", "BAR=2", "HOME=/agents/x"}

	if got := envIndex(env, "BAR"); got != 1 {
		t.Errorf("envIndex(BAR) = %d, want 1", got)
	}
	if got := envIndex(env, "MISSING"); got != -1 {
		t.Errorf("envIndex(MISSING) = %d, want -1", got)
	}
	// Prefix match must be exact: "HOM" should not match "HOME=...".
	if got := envIndex(env, "HOM"); got != -1 {
		t.Errorf("envIndex(HOM) = %d, want -1 (prefix should require '=')", got)
	}
}
