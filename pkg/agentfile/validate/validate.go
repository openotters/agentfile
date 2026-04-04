package validate

import (
	"io"

	"github.com/openotters/agentfile/pkg/agentfile/parse"
)

func ValidateFile(path string) bool { //nolint:revive // public API
	_, err := parse.ParseFile(path)
	return err == nil
}

func Validate(r io.Reader) bool {
	_, err := parse.Parse(r)
	return err == nil
}
