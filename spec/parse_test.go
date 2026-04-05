package spec_test

import (
	"strings"

	"testing"

	"github.com/openotters/agentfile/spec"
)

func TestParse_CompleteExample(t *testing.T) {
	t.Parallel()

	input := `
# syntax=openotters/agentfile:1

FROM scratch

RUNTIME ghcr.io/openotters/runtime:latest
MODEL anthropic/claude-haiku-4-5-20251001
NAME meteo

LABEL description="Weather assistant using Open-Meteo API"

CONTEXT SOUL "Agent personality and core instructions" <<EOF
You are a weather assistant.
Always report temperature in °C.
EOF

CONTEXT IDENTITY <<EOF
Name: Meteo Bot
EOF

CONFIG max-tokens=1024 "Maximum output tokens per response"
CONFIG max-iterations=10 "Maximum tool iterations per turn"
CONFIG api-base! "API base URL for the LLM provider"

BIN wget ghcr.io/openotters/tools/wget:latest
BIN jq ghcr.io/openotters/tools/jq:latest "Extract fields from JSON"

ADD data/cities.json /data/workspace/cities.json
`

	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if af.Syntax != "openotters/agentfile:1" {
		t.Errorf("syntax = %q, want openotters/agentfile:1", af.Syntax)
	}

	a := af.Agent

	if a.From != "scratch" {
		t.Errorf("from = %q, want scratch", a.From)
	}

	if a.Runtime != "ghcr.io/openotters/runtime:latest" {
		t.Errorf("runtime = %q", a.Runtime)
	}

	if a.Model != "anthropic/claude-haiku-4-5-20251001" {
		t.Errorf("model = %q", a.Model)
	}

	if a.Name != "meteo" {
		t.Errorf("name = %q, want meteo", a.Name)
	}

	if a.Labels["description"] != "Weather assistant using Open-Meteo API" {
		t.Errorf("label description = %q", a.Labels["description"])
	}

	if len(a.Contexts) != 2 {
		t.Fatalf("contexts = %d, want 2", len(a.Contexts))
	}

	if a.Contexts[0].Name != "SOUL" {
		t.Errorf("context[0].name = %q, want SOUL", a.Contexts[0].Name)
	}

	if a.Contexts[0].Description != "Agent personality and core instructions" {
		t.Errorf("context[0].description = %q", a.Contexts[0].Description)
	}

	if !strings.Contains(a.Contexts[0].Content, "weather assistant") {
		t.Errorf("context[0].content missing expected text")
	}

	if a.Contexts[1].Name != "IDENTITY" {
		t.Errorf("context[1].name = %q, want IDENTITY", a.Contexts[1].Name)
	}

	if len(a.Configs) != 3 {
		t.Fatalf("configs = %d, want 3", len(a.Configs))
	}

	if a.Configs[0].Key != "max-tokens" || a.Configs[0].Value != "1024" {
		t.Errorf("config[0] = %s=%s", a.Configs[0].Key, a.Configs[0].Value)
	}

	if a.Configs[2].Key != "api-base" || !a.Configs[2].Required {
		t.Errorf("config[2] = %s, required=%v", a.Configs[2].Key, a.Configs[2].Required)
	}

	if len(a.Bins) != 2 {
		t.Fatalf("bins = %d, want 2", len(a.Bins))
	}

	if a.Bins[0].Name != "wget" || a.Bins[0].Image != "ghcr.io/openotters/tools/wget:latest" {
		t.Errorf("bin[0] = %s %s", a.Bins[0].Name, a.Bins[0].Image)
	}

	if a.Bins[1].Description != "Extract fields from JSON" {
		t.Errorf("bin[1].description = %q", a.Bins[1].Description)
	}

	if len(a.Adds) != 1 {
		t.Fatalf("adds = %d, want 1", len(a.Adds))
	}

	if a.Adds[0].Src != "data/cities.json" || a.Adds[0].Dst != "/data/workspace/cities.json" {
		t.Errorf("add = %s → %s", a.Adds[0].Src, a.Adds[0].Dst)
	}
}

func TestParse_ContextFromFile(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
CONTEXT IDENTITY file://identities/meteo.md
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ctx := af.Agent.Contexts[0]
	if ctx.File != "identities/meteo.md" {
		t.Errorf("file = %q, want identities/meteo.md", ctx.File)
	}
}

func TestParse_BinWithUsage(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
BIN jq ghcr.io/openotters/tools/jq:latest "Extract fields from JSON" <<EOF
First line is the jq expression.
Rest is the JSON input.
EOF
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	bin := af.Agent.Bins[0]
	if bin.Usage == "" {
		t.Fatal("expected usage content")
	}

	if !strings.Contains(bin.Usage, "jq expression") {
		t.Errorf("usage = %q", bin.Usage)
	}
}

func TestParse_DefaultSyntax(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
NAME test
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if af.Syntax != "openotters/agentfile:1" {
		t.Errorf("syntax = %q, want default", af.Syntax)
	}
}

func TestParse_ArgSubstitution(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
ARG MODEL=anthropic/claude-haiku-4-5-20251001
ARG MAX_TOKENS=2048
MODEL ${MODEL}
CONFIG max-tokens=${MAX_TOKENS} "Max tokens"
NAME test-${MODEL}
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	a := af.Agent

	if a.Args["MODEL"] != "anthropic/claude-haiku-4-5-20251001" {
		t.Errorf("arg MODEL = %q", a.Args["MODEL"])
	}

	if a.Model != "anthropic/claude-haiku-4-5-20251001" {
		t.Errorf("model = %q, want anthropic/claude-haiku-4-5-20251001", a.Model)
	}

	if len(a.Configs) != 1 {
		t.Fatalf("configs = %d, want 1", len(a.Configs))
	}

	if a.Configs[0].Value != "2048" {
		t.Errorf("config max-tokens = %q, want 2048", a.Configs[0].Value)
	}

	if a.Name != "test-anthropic/claude-haiku-4-5-20251001" {
		t.Errorf("name = %q", a.Name)
	}
}

func TestParse_ArgWithoutDefault_LeavesUnexpanded(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
ARG PROVIDER
NAME agent-${PROVIDER}
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if af.Agent.Name != "agent-${PROVIDER}" {
		t.Errorf("name = %q, want agent-${PROVIDER} (should not expand undefined arg)", af.Agent.Name)
	}
}

func TestParse_ContextOverride(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
CONTEXT SOUL <<EOF
first
EOF
CONTEXT SOUL <<EOF
second
EOF
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(af.Agent.Contexts) != 1 {
		t.Fatalf("contexts = %d, want 1", len(af.Agent.Contexts))
	}

	if af.Agent.Contexts[0].Content != "second" {
		t.Errorf("content = %q, want second", af.Agent.Contexts[0].Content)
	}
}

func TestParse_ValidateReservedContext(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
CONTEXT AGENT <<EOF
should fail
EOF
`
	_, err := spec.Parse(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for reserved context name")
	}

	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error = %q, want reserved", err)
	}
}

func TestParse_UnknownInstruction(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
UNKNOWN value
`
	_, err := spec.Parse(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for unknown instruction")
	}
}

func TestParse_ValidateRequiredConfigWithDefault(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
CONFIG api-base!=https://example.com "Should fail"
`
	_, err := spec.Parse(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for required config with default value")
	}

	if !strings.Contains(err.Error(), "required configs cannot have a default") {
		t.Errorf("error = %q", err)
	}
}

func TestParse_FROMNotFirst(t *testing.T) {
	t.Parallel()

	input := `NAME test
FROM scratch
`
	_, err := spec.Parse(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error when FROM is not first instruction")
	}

	if !strings.Contains(err.Error(), "FROM must be the first instruction") {
		t.Errorf("error = %q", err)
	}
}

func TestParse_RuntimeOverridesClearsConfigs(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
RUNTIME ghcr.io/openotters/runtime:v1
CONFIG max-tokens=1024
CONFIG max-iterations=10
RUNTIME ghcr.io/openotters/runtime:v2
CONFIG timeout=30
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if af.Agent.Runtime != "ghcr.io/openotters/runtime:v2" {
		t.Errorf("runtime = %q, want v2", af.Agent.Runtime)
	}

	if len(af.Agent.Configs) != 1 {
		t.Fatalf("configs = %d, want 1 (only configs after second RUNTIME)", len(af.Agent.Configs))
	}

	if af.Agent.Configs[0].Key != "timeout" {
		t.Errorf("config[0].key = %q, want timeout", af.Agent.Configs[0].Key)
	}
}

func TestParse_RuntimeOverrideNoConfigs(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
CONFIG max-tokens=1024
RUNTIME ghcr.io/openotters/runtime:latest
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if af.Agent.Runtime != "ghcr.io/openotters/runtime:latest" {
		t.Errorf("runtime = %q", af.Agent.Runtime)
	}

	if len(af.Agent.Configs) != 0 {
		t.Errorf("configs = %d, want 0 (RUNTIME should clear prior configs)", len(af.Agent.Configs))
	}
}

func TestParse_FROMFirstWithComments(t *testing.T) {
	t.Parallel()

	input := `# this is a comment
# syntax=openotters/agentfile:1

FROM scratch
NAME test
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if af.Agent.From != "scratch" {
		t.Errorf("from = %q, want scratch", af.Agent.From)
	}
}
