//nolint:testpackage // tests unexported helpers
package system

import "testing"

func TestAgent_OptionSetters(t *testing.T) {
	t.Parallel()

	a := &Agent{ws: workspace{}}

	WithDigestResolver(func(string) string { return "abc" })(a)
	if a.ws.digestResolver == nil {
		t.Error("WithDigestResolver didn't set field")
	}

	WithImageRef("ghcr.io/foo:bar")(a)
	if a.ws.imageRef != "ghcr.io/foo:bar" {
		t.Errorf("imageRef = %q", a.ws.imageRef)
	}
}
