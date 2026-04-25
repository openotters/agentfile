// Package validate exposes Agentfile validation entry points for files,
// readers, and programmatically constructed structs. All forms delegate to
// spec.Validate so there is a single source of truth for validation rules.
package validate

import (
	"io"

	"github.com/openotters/agentfile/spec"
)

// ValidateFile parses and validates an Agentfile at the given path.
func ValidateFile(path string) error { //nolint:revive // public API
	_, err := spec.ParseFile(path)
	return err
}

// Validate parses and validates an Agentfile from a reader.
func Validate(r io.Reader) error {
	_, err := spec.Parse(r)
	return err
}

// Struct validates a programmatically constructed Agentfile.
func Struct(af *spec.Agentfile) error {
	return spec.Validate(af)
}
