package validate_test

import (
	"strings"
	"testing"

	"github.com/openotters/agentfile/pkg/agentfile/validate"
)

func TestValidate_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{
			name: "minimal",
			input: `FROM scratch
NAME test
`,
		},
		{
			name: "full",
			input: `# syntax=openotters/agentfile:1

FROM scratch

RUNTIME ghcr.io/openotters/runtime:latest
MODEL anthropic/claude-haiku-4-5-20251001
NAME meteo

LABEL description="Weather assistant"

CONTEXT SOUL "Core instructions" <<EOF
You are a weather assistant.
EOF

CONFIG max-tokens=1024 "Max tokens"

BIN wget ghcr.io/openotters/tools/wget:latest "Fetch URL content"

ADD cities.json /data/cities.json "Known cities"
`,
		},
		{
			name: "with args",
			input: `FROM scratch
ARG MODEL=anthropic/claude-haiku-4-5-20251001
MODEL ${MODEL}
`,
		},
		{
			name: "context from file",
			input: `FROM scratch
CONTEXT IDENTITY file://identity.md
`,
		},
		{
			name: "context override",
			input: `FROM scratch
CONTEXT SOUL <<EOF
first
EOF
CONTEXT SOUL <<EOF
second
EOF
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if !validate.Validate(strings.NewReader(tt.input)) {
				t.Error("expected valid agentfile")
			}
		})
	}
}

func TestValidate_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "empty",
			input: "",
		},
		{
			name:  "unknown instruction",
			input: "UNKNOWN value\n",
		},
		{
			name:  "FROM not first",
			input: "NAME test\nFROM scratch\n",
		},
		{
			name: "reserved context name",
			input: `FROM scratch
CONTEXT AGENT <<EOF
reserved
EOF
`,
		},
		{
			name: "required config with default",
			input: `FROM scratch
CONFIG key!=value "Should fail"
`,
		},
		{
			name: "unterminated heredoc",
			input: `FROM scratch
CONTEXT SOUL <<EOF
no end marker
`,
		},
		{
			name:  "bin missing image",
			input: "FROM scratch\nBIN wget\n",
		},
		{
			name:  "label missing value",
			input: "FROM scratch\nLABEL key\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if validate.Validate(strings.NewReader(tt.input)) {
				t.Error("expected invalid agentfile")
			}
		})
	}
}
