package validate_test

import (
	"strings"
	"testing"

	"github.com/openotters/agentfile/spec"
	"github.com/openotters/agentfile/validate"
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

			if err := validate.Validate(strings.NewReader(tt.input)); err != nil {
				t.Errorf("expected valid agentfile: %v", err)
			}
		})
	}
}

func TestValidate_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty",
			input: "",
			want:  "FROM is required",
		},
		{
			name:  "unknown instruction",
			input: "UNKNOWN value\n",
			want:  "line 1",
		},
		{
			name:  "FROM not first",
			input: "NAME test\nFROM scratch\n",
			want:  "FROM must be the first instruction",
		},
		{
			name: "reserved context name",
			input: `FROM scratch
CONTEXT AGENT <<EOF
reserved
EOF
`,
			want: "reserved",
		},
		{
			name: "required config with default",
			input: `FROM scratch
CONFIG key!=value "Should fail"
`,
			want: "required configs cannot have a default",
		},
		{
			name: "unterminated heredoc",
			input: `FROM scratch
CONTEXT SOUL <<EOF
no end marker
`,
			want: "unterminated heredoc",
		},
		{
			name:  "bin missing image",
			input: "FROM scratch\nBIN wget\n",
			want:  "line 2",
		},
		{
			name:  "label missing value",
			input: "FROM scratch\nLABEL key\n",
			want:  "line 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validate.Validate(strings.NewReader(tt.input))
			if err == nil {
				t.Fatal("expected error")
			}

			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateStruct_Valid(t *testing.T) {
	t.Parallel()

	af := &spec.Agentfile{
		Agent: &spec.Agent{
			From:   "scratch",
			Name:   "test",
			Labels: map[string]string{},
			Args:   map[string]string{},
		},
	}

	if err := validate.Struct(af); err != nil {
		t.Errorf("expected valid: %v", err)
	}
}

func TestValidateStruct_NilAgent(t *testing.T) {
	t.Parallel()

	if err := validate.Struct(&spec.Agentfile{}); err == nil {
		t.Error("expected error for nil agent")
	}
}

func TestValidateStruct_ReservedContext(t *testing.T) {
	t.Parallel()

	af := &spec.Agentfile{
		Agent: &spec.Agent{
			From: "scratch",
			Contexts: []*spec.Context{
				{Name: "AGENT", Content: "reserved"},
			},
			Labels: map[string]string{},
			Args:   map[string]string{},
		},
	}

	err := validate.Struct(af)
	if err == nil {
		t.Fatal("expected error")
	}

	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error = %q", err)
	}
}
