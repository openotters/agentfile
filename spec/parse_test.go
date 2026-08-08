package spec_test

import (
	"strings"

	"testing"

	"github.com/openotters/agentfile/spec"
)

func TestRequiredConfigsProvided(t *testing.T) {
	t.Parallel()

	val := "https://example.com"

	tests := []struct {
		name    string
		configs []*spec.Config
		wantErr bool
		wantKey string
	}{
		{
			name:    "required with value passes",
			configs: []*spec.Config{{Key: "api-base", Required: true, Value: val}},
			wantErr: false,
		},
		{
			name:    "required without value fails",
			configs: []*spec.Config{{Key: "api-base", Required: true}},
			wantErr: true,
			wantKey: "api-base",
		},
		{
			name:    "optional without value passes",
			configs: []*spec.Config{{Key: "custom-header"}},
			wantErr: false,
		},
		{
			name: "lists every missing required key",
			configs: []*spec.Config{
				{Key: "zeta", Required: true},
				{Key: "alpha", Required: true},
				{Key: "set", Required: true, Value: val},
			},
			wantErr: true,
			wantKey: "alpha, zeta", // sorted
		},
		{
			name:    "nil entries ignored",
			configs: []*spec.Config{nil, {Key: "ok", Value: val}},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := spec.RequiredConfigsProvided(tt.configs)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantKey != "" && !strings.Contains(err.Error(), tt.wantKey) {
				t.Errorf("error %q does not name %q", err, tt.wantKey)
			}
		})
	}
}

func TestWithConfig(t *testing.T) {
	t.Parallel()

	t.Run("overrides declared key and satisfies required", func(t *testing.T) {
		t.Parallel()

		af := &spec.Agentfile{Agent: &spec.Agent{
			From:    "scratch",
			Configs: []*spec.Config{{Key: "api-base", Required: true}},
		}}

		// Required-but-unset fails before the override.
		if err := spec.RequiredConfigsProvided(af.Agent.Configs); err == nil {
			t.Fatal("expected required config to be unsatisfied before override")
		}

		af.Apply(spec.WithConfig(map[string]string{"api-base": "https://example.com"}))

		if got := af.Agent.Configs[0].Value; got != "https://example.com" {
			t.Errorf("value = %v, want override", got)
		}
		// Override makes the required config deployable.
		if err := spec.RequiredConfigsProvided(af.Agent.Configs); err != nil {
			t.Errorf("override should satisfy required config: %v", err)
		}
	})

	t.Run("appends undeclared key", func(t *testing.T) {
		t.Parallel()

		af := &spec.Agentfile{Agent: &spec.Agent{From: "scratch"}}
		af.Apply(spec.WithConfig(map[string]string{"new-key": "v"}))

		if len(af.Agent.Configs) != 1 || af.Agent.Configs[0].Key != "new-key" {
			t.Fatalf("expected appended config, got %+v", af.Agent.Configs)
		}
	})

	t.Run("empty values is a no-op", func(t *testing.T) {
		t.Parallel()

		af := &spec.Agentfile{Agent: &spec.Agent{
			From:    "scratch",
			Configs: []*spec.Config{{Key: "x", Value: "keep"}},
		}}
		af.Apply(spec.WithConfig(nil))

		if af.Agent.Configs[0].Value != "keep" {
			t.Errorf("nil override mutated existing value: %v", af.Agent.Configs[0].Value)
		}
	})
}

func TestParse_CompleteExample(t *testing.T) {
	t.Parallel()

	input := `# syntax=openotters/agentfile:1

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

ADD data/cities.json cities-known.json
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

	if a.Configs[0].Key != "max-tokens" || a.Configs[0].Value != int64(1024) {
		t.Errorf("config[0] = %s=%v", a.Configs[0].Key, a.Configs[0].Value)
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

	if a.Adds[0].Src != "data/cities.json" || a.Adds[0].Name != "cities-known.json" {
		t.Errorf("add = %s → %s", a.Adds[0].Src, a.Adds[0].Name)
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
ARG SUFFIX=v2
MODEL ${MODEL}
CONFIG max-tokens=${MAX_TOKENS} "Max tokens"
NAME test-${SUFFIX}
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

	if v, ok := a.Configs[0].Value.(int64); !ok || v != 2048 {
		t.Errorf("config max-tokens = %v (%T), want int64(2048)", a.Configs[0].Value, a.Configs[0].Value)
	}

	if a.Name != "test-v2" {
		t.Errorf("name = %q, want test-v2", a.Name)
	}
}

func TestParse_ArgWithoutDefault_LeavesUnexpanded(t *testing.T) {
	t.Parallel()

	// The unexpanded ${VAR} lands in a LABEL value — a position with
	// no name-shape rule — because path-safe positions (NAME etc.)
	// reject the literal `${…}` at validate, by design.
	input := `FROM scratch
ARG PROVIDER
LABEL provider=agent-${PROVIDER}
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if af.Agent.Labels["provider"] != "agent-${PROVIDER}" {
		t.Errorf("label = %q, want agent-${PROVIDER} (should not expand undefined arg)",
			af.Agent.Labels["provider"])
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

func TestParse_Exec(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
EXEC ["serve", "--max-tokens", "1024"]
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(af.Agent.Exec) != 3 {
		t.Fatalf("exec = %v, want 3 args", af.Agent.Exec)
	}

	if af.Agent.Exec[0] != "serve" {
		t.Errorf("exec[0] = %q, want serve", af.Agent.Exec[0])
	}

	if af.Agent.Exec[1] != "--max-tokens" {
		t.Errorf("exec[1] = %q, want --max-tokens", af.Agent.Exec[1])
	}

	if af.Agent.Exec[2] != "1024" {
		t.Errorf("exec[2] = %q, want 1024", af.Agent.Exec[2])
	}
}

func TestParse_ExecWithArgSubstitution(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
ARG MAX_TOKENS=2048
EXEC ["serve", "--max-tokens", "${MAX_TOKENS}"]
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(af.Agent.Exec) != 3 {
		t.Fatalf("exec = %v, want 3 args", af.Agent.Exec)
	}

	if af.Agent.Exec[2] != "2048" {
		t.Errorf("exec[2] = %q, want 2048", af.Agent.Exec[2])
	}
}

func TestParse_ExecSingleArg(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
EXEC ["serve"]
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(af.Agent.Exec) != 1 {
		t.Fatalf("exec = %v, want 1 arg", af.Agent.Exec)
	}

	if af.Agent.Exec[0] != "serve" {
		t.Errorf("exec[0] = %q, want serve", af.Agent.Exec[0])
	}
}

func TestParse_ExecDefault(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
NAME test
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(af.Agent.Exec) != 0 {
		t.Errorf("exec = %v, want empty (default applied by runtime, not parser)", af.Agent.Exec)
	}
}

func TestParse_ExecOverrides(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
EXEC ["serve", "--v1"]
EXEC ["serve", "--v2"]
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(af.Agent.Exec) != 2 {
		t.Fatalf("exec = %v, want 2 args", af.Agent.Exec)
	}

	if af.Agent.Exec[1] != "--v2" {
		t.Errorf("exec[1] = %q, want --v2 (last EXEC should win)", af.Agent.Exec[1])
	}
}

func TestParse_ConfigTypedValues(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
CONFIG max-tokens=1024 "Integer value"
CONFIG temperature=0.7 "Float value"
CONFIG verbose=true "Boolean true"
CONFIG stream=false "Boolean false"
CONFIG strategy=summarize "String value"
CONFIG greeting="hello world" "Quoted string"
CONFIG port="8080" "Quoted number stays string"
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	configs := af.Agent.Configs
	if len(configs) != 7 {
		t.Fatalf("configs = %d, want 7", len(configs))
	}

	// Integer
	if v, ok := configs[0].Value.(int64); !ok || v != 1024 {
		t.Errorf("max-tokens = %v (%T), want int64(1024)", configs[0].Value, configs[0].Value)
	}

	// Float
	if v, ok := configs[1].Value.(float64); !ok || v != 0.7 {
		t.Errorf("temperature = %v (%T), want float64(0.7)", configs[1].Value, configs[1].Value)
	}

	// Boolean true
	if v, ok := configs[2].Value.(bool); !ok || v != true {
		t.Errorf("verbose = %v (%T), want bool(true)", configs[2].Value, configs[2].Value)
	}

	// Boolean false
	if v, ok := configs[3].Value.(bool); !ok || v != false {
		t.Errorf("stream = %v (%T), want bool(false)", configs[3].Value, configs[3].Value)
	}

	// Unquoted string
	if v, ok := configs[4].Value.(string); !ok || v != "summarize" {
		t.Errorf("strategy = %v (%T), want string(summarize)", configs[4].Value, configs[4].Value)
	}

	// Quoted string (with spaces)
	if v, ok := configs[5].Value.(string); !ok || v != "hello world" {
		t.Errorf("greeting = %v (%T), want string(hello world)", configs[5].Value, configs[5].Value)
	}

	// Quoted number stays string
	if v, ok := configs[6].Value.(string); !ok || v != "8080" {
		t.Errorf("port = %v (%T), want string(8080)", configs[6].Value, configs[6].Value)
	}
}

func TestParse_EnvVars(t *testing.T) {
	t.Parallel()

	input := `FROM scratch
ENV NODE_ENV=production "Application environment"
ENV LOG_LEVEL=debug
ENV GREETING="hello world" "Quoted with spaces"
ENV STRIPE_PUBLISHABLE_KEY=pk_live_abc123
`
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	envs := af.Agent.Envs
	if len(envs) != 4 {
		t.Fatalf("envs = %d, want 4", len(envs))
	}

	cases := []struct {
		key, value, desc string
	}{
		{"NODE_ENV", "production", "Application environment"},
		{"LOG_LEVEL", "debug", ""},
		{"GREETING", "hello world", "Quoted with spaces"},
		{"STRIPE_PUBLISHABLE_KEY", "pk_live_abc123", ""},
	}

	for i, want := range cases {
		got := envs[i]
		if got.Key != want.key || got.Value != want.value || got.Description != want.desc {
			t.Errorf("envs[%d] = {%q, %q, %q}, want {%q, %q, %q}",
				i, got.Key, got.Value, got.Description, want.key, want.value, want.desc)
		}
	}
}

func TestParse_EnvReservedKeys(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, line, contains string
	}{
		{"PATH", `ENV PATH=/foo`, "reserved"},
		{"HOME", `ENV HOME=/tmp`, "reserved"},
		{"OTTERS_AGENT_ROOT", `ENV OTTERS_AGENT_ROOT=/x`, "reserved"},
		{"trailing_API_KEY", `ENV STRIPE_API_KEY=sk_test`, "_API_KEY"},
		{"trailing_API_BASE", `ENV STRIPE_API_BASE=https://api.stripe.com`, "_API_BASE"},
		{"lowercase", `ENV node_env=production`, "uppercase"},
		{"leading_digit", `ENV 1FOO=bar`, "uppercase"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			input := "FROM scratch\n" + tc.line + "\n"
			_, err := spec.Parse(strings.NewReader(input))
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.contains)
			}
		})
	}
}

func TestParse_FROMFirstWithComments(t *testing.T) {
	t.Parallel()

	input := `# this is a comment
# another comment

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

func TestParse_SyntaxDirective(t *testing.T) {
	t.Parallel()

	t.Run("misplaced directive is an error", func(t *testing.T) {
		t.Parallel()

		input := `# a comment
# syntax=openotters/agentfile:1
FROM scratch
`
		if _, err := spec.Parse(strings.NewReader(input)); err == nil ||
			!strings.Contains(err.Error(), "very first line") {
			t.Fatalf("expected misplaced-directive error, got %v", err)
		}
	})

	t.Run("unsupported value is an error", func(t *testing.T) {
		t.Parallel()

		input := `# syntax=docker/dockerfile:1
FROM scratch
`
		if _, err := spec.Parse(strings.NewReader(input)); err == nil ||
			!strings.Contains(err.Error(), "unsupported syntax") {
			t.Fatalf("expected unsupported-syntax error, got %v", err)
		}
	})

	t.Run("first-line directive accepted", func(t *testing.T) {
		t.Parallel()

		input := `# syntax=openotters/agentfile:1
FROM scratch
`
		af, err := spec.Parse(strings.NewReader(input))
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}

		if af.Syntax != "openotters/agentfile:1" {
			t.Errorf("syntax = %q", af.Syntax)
		}
	})

	t.Run("BOM rejected", func(t *testing.T) {
		t.Parallel()

		input := "\ufeffFROM scratch\n"
		if _, err := spec.Parse(strings.NewReader(input)); err == nil ||
			!strings.Contains(err.Error(), "byte-order mark") {
			t.Fatalf("expected BOM error, got %v", err)
		}
	})
}

func TestParse_CapabilitySingleAndMultiName(t *testing.T) {
	t.Parallel()

	// Three forms in one Agentfile:
	//   1. Single name per line (the original shape).
	//   2. Multiple names on one line (the new shape).
	//   3. A duplicate spanning both forms — must be folded out.
	input := `FROM scratch
CAPABILITY note-save
CAPABILITY note-list note-show
CAPABILITY job-submit job-wait job-list
CAPABILITY note-save
`

	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	want := []string{
		"note-save",
		"note-list",
		"note-show",
		"job-submit",
		"job-wait",
		"job-list",
	}

	got := af.Agent.Capabilities
	if len(got) != len(want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("capabilities[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestParse_CapabilityDedupesWithinSameLine(t *testing.T) {
	t.Parallel()

	// Same name repeated on one line is a no-op, not a multiplier.
	input := `FROM scratch
CAPABILITY note-save note-save note-save
`

	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	got := af.Agent.Capabilities
	if len(got) != 1 || got[0] != "note-save" {
		t.Errorf("capabilities = %v, want [note-save]", got)
	}
}

func TestParse_PathSafeNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"NAME with slash", "FROM scratch\nNAME agents/meteo\n"},
		{"NAME dot-prefixed", "FROM scratch\nNAME .hidden\n"},
		{"CONTEXT traversal", "FROM scratch\nCONTEXT ../../etc/passwd <<EOF\nx\nEOF\n"},
		{"BIN with slash", "FROM scratch\nBIN tools/jq ghcr.io/x/jq:latest\n"},
		{"ADD name with slash", "FROM scratch\nADD cities.json /data/cities.json\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := spec.Parse(strings.NewReader(tt.input)); err == nil ||
				!strings.Contains(err.Error(), "path") {
				t.Fatalf("expected path-safe rejection, got %v", err)
			}
		})
	}
}

func TestParse_DNS1123Names(t *testing.T) {
	t.Parallel()

	t.Run("CONFIG key rejected", func(t *testing.T) {
		t.Parallel()

		input := "FROM scratch\nCONFIG Max.Tokens=1\n"
		if _, err := spec.Parse(strings.NewReader(input)); err == nil ||
			!strings.Contains(err.Error(), "DNS-1123") {
			t.Fatalf("expected DNS-1123 rejection, got %v", err)
		}
	})

	t.Run("CAPABILITY name rejected", func(t *testing.T) {
		t.Parallel()

		input := "FROM scratch\nCAPABILITY Note_Save\n"
		if _, err := spec.Parse(strings.NewReader(input)); err == nil ||
			!strings.Contains(err.Error(), "DNS-1123") {
			t.Fatalf("expected DNS-1123 rejection, got %v", err)
		}
	})

	t.Run("CAPABILITY trailing garbage rejected", func(t *testing.T) {
		t.Parallel()

		input := "FROM scratch\nCAPABILITY note-save # comment\n"
		if _, err := spec.Parse(strings.NewReader(input)); err == nil {
			t.Fatal("expected trailing tokens after CAPABILITY to be rejected")
		}
	})
}

func TestParse_FROMExactlyOnce(t *testing.T) {
	t.Parallel()

	input := "FROM scratch\nNAME a\nFROM scratch\n"
	if _, err := spec.Parse(strings.NewReader(input)); err == nil ||
		!strings.Contains(err.Error(), "exactly once") {
		t.Fatalf("expected duplicate-FROM error, got %v", err)
	}
}

func TestParse_ContextFileAndHeredocConflict(t *testing.T) {
	t.Parallel()

	input := "FROM scratch\nCONTEXT SOUL file://soul.md <<EOF\nx\nEOF\n"
	if _, err := spec.Parse(strings.NewReader(input)); err == nil ||
		!strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected file/heredoc conflict error, got %v", err)
	}
}

func TestParse_ReservedContextNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"AGENT", "WORKSPACE", "MOUNTS"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			input := "FROM scratch\nCONTEXT " + name + " <<EOF\nx\nEOF\n"
			if _, err := spec.Parse(strings.NewReader(input)); err == nil ||
				!strings.Contains(err.Error(), "reserved") {
				t.Fatalf("expected reserved-name error, got %v", err)
			}
		})
	}
}

func TestParse_IdentStartingWithEXEC(t *testing.T) {
	t.Parallel()

	input := "FROM scratch\nNAME EXECUTOR\nLABEL mode=EXECFAST\n"
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if af.Agent.Name != "EXECUTOR" || af.Agent.Labels["mode"] != "EXECFAST" {
		t.Errorf("name = %q, label = %q", af.Agent.Name, af.Agent.Labels["mode"])
	}
}

func TestParse_AddNameDefaultsToBasename(t *testing.T) {
	t.Parallel()

	input := "FROM scratch\nADD prompts/system.txt \"System prompt\"\n"
	af, err := spec.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	add := af.Agent.Adds[0]
	if add.Src != "prompts/system.txt" || add.Name != "system.txt" {
		t.Errorf("add = %s → %s, want name system.txt", add.Src, add.Name)
	}

	if add.Description != "System prompt" {
		t.Errorf("description = %q", add.Description)
	}
}

func TestRequiredConfigsProvided_FlattenedView(t *testing.T) {
	t.Parallel()

	t.Run("later value satisfies earlier required", func(t *testing.T) {
		t.Parallel()

		configs := []*spec.Config{
			{Key: "webhook-url", Required: true},
			{Key: "webhook-url", Value: "https://example.com"},
		}
		if err := spec.RequiredConfigsProvided(configs); err != nil {
			t.Errorf("child value should satisfy parent requirement: %v", err)
		}
	})

	t.Run("last writer unsetting fails", func(t *testing.T) {
		t.Parallel()

		configs := []*spec.Config{
			{Key: "webhook-url", Required: true, Value: nil},
			{Key: "webhook-url", Value: "v"},
			{Key: "webhook-url"},
		}
		if err := spec.RequiredConfigsProvided(configs); err == nil {
			t.Error("last-writer nil value should leave requirement unsatisfied")
		}
	})

	t.Run("set-but-empty counts as set", func(t *testing.T) {
		t.Parallel()

		configs := []*spec.Config{{Key: "webhook-url", Required: true, Value: ""}}
		if err := spec.RequiredConfigsProvided(configs); err != nil {
			t.Errorf("empty-string value should count as set: %v", err)
		}
	})
}

func TestReservedEnvKeys(t *testing.T) {
	t.Parallel()

	keys := spec.ReservedEnvKeys()
	if len(keys) == 0 {
		t.Fatal("expected non-empty reserved env key list")
	}

	for _, k := range keys {
		af := &spec.Agentfile{Agent: &spec.Agent{
			From: "scratch",
			Envs: []*spec.Env{{Key: k, Value: "x"}},
		}}
		if err := spec.Validate(af); err == nil {
			t.Errorf("ReservedEnvKeys lists %s but Validate accepts it", k)
		}
	}
}
