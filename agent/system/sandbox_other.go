//go:build !darwin && !linux

package system

// wrapperKind reports the sandbox impl name. Always "none" on
// platforms without a supported sandbox primitive (Windows, BSD).
func wrapperKind() string { return "none" }

// newWrapper returns the no-op wrapper on platforms without a
// supported sandbox primitive (Windows, BSD). Provider startup
// emits a single warning so the operator knows agents are running
// unsandboxed; subsequent calls are a no-op.
func newWrapper(_ sandboxParams) wrapper {
	return noopWrapper{}
}
