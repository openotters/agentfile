package validate

import (
	"fmt"
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

// ValidateStruct validates a programmatically constructed Agentfile.
func Struct(af *spec.Agentfile) error {
	if af == nil {
		return fmt.Errorf("agentfile is nil")
	}

	if af.Agent == nil {
		return fmt.Errorf("agent is nil")
	}

	a := af.Agent

	if a.From == "" {
		return fmt.Errorf("FROM is required")
	}

	for _, ctx := range a.Contexts {
		if ctx.Name == "AGENT" {
			return fmt.Errorf("context name AGENT is reserved")
		}
	}

	for _, cfg := range a.Configs {
		if cfg.Required && cfg.Value != "" {
			return fmt.Errorf("config %s: required configs cannot have a default value", cfg.Key)
		}
	}

	return nil
}
