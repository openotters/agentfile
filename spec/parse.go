package spec

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

func ParseFile(path string) (*Agentfile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening agentfile: %w", err)
	}
	defer f.Close()

	return Parse(f)
}

func Parse(r io.Reader) (*Agentfile, error) {
	p := &parser{scanner: bufio.NewScanner(r)}

	return p.parse()
}

type parser struct {
	scanner          *bufio.Scanner
	line             int
	firstInstruction string
}

func (p *parser) parse() (*Agentfile, error) {
	af := &Agentfile{Agent: newAgent()}

	for p.scanner.Scan() {
		p.line++
		trimmed := strings.TrimSpace(p.scanner.Text())

		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "#") {
			if strings.HasPrefix(trimmed, "# syntax=") {
				af.Syntax = strings.TrimPrefix(trimmed, "# syntax=")
			}

			continue
		}

		line, heredoc, err := p.extractHeredoc(trimmed)
		if err != nil {
			return nil, p.errorf("%v", err)
		}

		line = expandArgs(line, af.Agent.Args)

		inst, parseErr := instructionParser.ParseString("", normalizeKeyword(line))
		if parseErr != nil {
			return nil, p.errorf("%v", parseErr)
		}

		if p.firstInstruction == "" {
			p.firstInstruction = instructionType(inst)
		}

		applyInstruction(af.Agent, inst, heredoc)
	}

	if err := p.scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading agentfile: %w", err)
	}

	if p.firstInstruction != "" && p.firstInstruction != "FROM" {
		return nil, fmt.Errorf("FROM must be the first instruction")
	}

	if af.Syntax == "" {
		af.Syntax = DefaultSyntax
	}

	return validate(af)
}

// applyInstruction is a long switch over the parsed instruction
// variants. Splitting per-directive helpers would obscure the per-line
// dispatch the parser runs.
//
//nolint:funlen,gocognit,gocyclo,cyclop // exhaustive directive switch — flat assignments
func applyInstruction(agent *Agent, inst *instruction, heredoc string) {
	switch {
	case inst.From != nil:
		agent.From = *inst.From
	case inst.Runtime != nil:
		agent.Runtime = *inst.Runtime
		agent.Configs = nil
	case inst.Model != nil:
		agent.Model = *inst.Model
	case inst.Name != nil:
		agent.Name = *inst.Name

	case inst.Context != nil:
		ctx := &Context{Name: inst.Context.Name}
		if inst.Context.Desc != nil {
			ctx.Description = *inst.Context.Desc
		}
		if inst.Context.File != nil {
			ctx.File = *inst.Context.File
		}
		if heredoc != "" {
			ctx.Content = heredoc
		}

		replaced := false
		for i, existing := range agent.Contexts {
			if existing.Name == ctx.Name {
				agent.Contexts[i] = ctx
				replaced = true

				break
			}
		}

		if !replaced {
			agent.Contexts = append(agent.Contexts, ctx)
		}

	case inst.Config != nil:
		cfg := &Config{
			Key:      inst.Config.Key,
			Required: inst.Config.Required,
		}
		if inst.Config.QuotedValue != nil {
			cfg.Value = *inst.Config.QuotedValue // quoted = always string
		} else if inst.Config.Value != nil {
			cfg.Value = parseConfigValue(*inst.Config.Value) // unquoted = type-inferred
		}
		if inst.Config.Desc != nil {
			cfg.Description = *inst.Config.Desc
		}
		agent.Configs = append(agent.Configs, cfg)

	case inst.Bin != nil:
		bin := &Bin{
			Name:  inst.Bin.Name,
			Image: inst.Bin.Image,
		}
		if inst.Bin.Desc != nil {
			bin.Description = *inst.Bin.Desc
		}
		if heredoc != "" {
			bin.Usage = heredoc
		}
		agent.Bins = append(agent.Bins, bin)

	case inst.Add != nil:
		add := &Add{
			Src: inst.Add.Src,
			Dst: inst.Add.Dst,
		}
		if inst.Add.Desc != nil {
			add.Description = *inst.Add.Desc
		}
		agent.Adds = append(agent.Adds, add)

	case inst.Label != nil:
		agent.Labels[inst.Label.Key] = inst.Label.Value

	case inst.Exec != nil:
		agent.Exec = inst.Exec.Args

	case inst.Arg != nil:
		if inst.Arg.Value != nil {
			agent.Args[inst.Arg.Key] = *inst.Arg.Value
		}

	case inst.Env != nil:
		e := &Env{Key: inst.Env.Key, Value: inst.Env.Value}
		if inst.Env.Desc != nil {
			e.Description = *inst.Env.Desc
		}
		agent.Envs = append(agent.Envs, e)

	case inst.Capability != nil:
		// One directive can list multiple names — the grammar
		// accepts CAPABILITY <name> [<name>…]. Deduplicate as we
		// go so repeating a name within the same line, across
		// lines, or via FROM inheritance is a no-op rather than
		// an additive multiplier.
		for _, name := range inst.Capability.Names {
			if name == "" {
				continue
			}
			already := false
			for _, existing := range agent.Capabilities {
				if existing == name {
					already = true
					break
				}
			}
			if !already {
				agent.Capabilities = append(agent.Capabilities, name)
			}
		}
	}
}

func (p *parser) extractHeredoc(line string) (string, string, error) {
	parts := splitQuoted(line)
	if len(parts) == 0 {
		return line, "", nil
	}

	last := parts[len(parts)-1]
	if !strings.HasPrefix(last, "<<") {
		return line, "", nil
	}

	marker := strings.TrimPrefix(last, "<<")
	if marker == "" {
		return line, "", nil
	}

	idx := strings.LastIndex(line, last)
	cleanLine := strings.TrimSpace(line[:idx])

	var b strings.Builder

	for p.scanner.Scan() {
		p.line++
		text := p.scanner.Text()

		if strings.TrimSpace(text) == marker {
			return cleanLine, strings.TrimRight(b.String(), "\n"), nil
		}

		b.WriteString(text)
		b.WriteByte('\n')
	}

	return "", "", fmt.Errorf("unterminated heredoc, expected %s", marker)
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("line %d: "+format, append([]any{p.line}, args...)...)
}

func newAgent() *Agent {
	return &Agent{
		Labels: make(map[string]string),
		Args:   make(map[string]string),
	}
}

func instructionType(inst *instruction) string {
	switch {
	case inst.From != nil:
		return "FROM"
	case inst.Runtime != nil:
		return "RUNTIME"
	case inst.Model != nil:
		return "MODEL"
	case inst.Name != nil:
		return "NAME"
	case inst.Context != nil:
		return "CONTEXT"
	case inst.Config != nil:
		return "CONFIG"
	case inst.Bin != nil:
		return "BIN"
	case inst.Add != nil:
		return "ADD"
	case inst.Label != nil:
		return "LABEL"
	case inst.Exec != nil:
		return "EXEC"
	case inst.Arg != nil:
		return "ARG"
	case inst.Env != nil:
		return "ENV"
	case inst.Capability != nil:
		return "CAPABILITY"
	default:
		return ""
	}
}

func normalizeKeyword(line string) string {
	i := strings.IndexAny(line, " \t")
	if i == -1 {
		return strings.ToUpper(line)
	}

	return strings.ToUpper(line[:i]) + line[i:]
}

func splitQuoted(s string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false

	for i := range len(s) {
		c := s[i]

		switch {
		case c == '"' && !inQuote:
			inQuote = true
			current.WriteByte(c)
		case c == '"' && inQuote:
			inQuote = false
			current.WriteByte(c)
		case (c == ' ' || c == '\t') && !inQuote:
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(c)
		}
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

func expandArgs(s string, args map[string]string) string {
	if !strings.Contains(s, "${") {
		return s
	}

	for k, v := range args {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}

	return s
}

// parseConfigValue interprets an unquoted config value as a YAML primitive.
// Quoted values (containing spaces) are always strings.
func parseConfigValue(s string) any {
	// Boolean
	if s == "true" {
		return true
	}

	if s == "false" {
		return false
	}

	// Integer (no dot, no leading zero except "0" itself)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}

	// Float
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}

	// String
	return s
}

// Validate checks structural invariants on a programmatically constructed
// Agentfile: FROM is required, context name AGENT is reserved, and required
// configs cannot carry a default value. Parse already runs Validate; callers
// who build an Agentfile in code should call Validate themselves.
func Validate(af *Agentfile) error {
	if af == nil {
		return fmt.Errorf("agentfile is nil")
	}

	if af.Agent == nil {
		return fmt.Errorf("agent is nil")
	}

	if af.Agent.From == "" {
		return fmt.Errorf("FROM is required")
	}

	for _, ctx := range af.Agent.Contexts {
		if ctx.Name == "AGENT" {
			return fmt.Errorf("context name AGENT is reserved")
		}
	}

	for _, cfg := range af.Agent.Configs {
		if cfg.Required && cfg.Value != nil {
			return fmt.Errorf("config %s: required configs cannot have a default value", cfg.Key)
		}
	}

	for _, env := range af.Agent.Envs {
		if err := validateEnvKey(env.Key); err != nil {
			return err
		}
	}

	return nil
}

// reservedEnvKeys are produced by executor.BuildLockedEnv and must
// not be overridden by user-declared ENV — overriding them breaks
// the sandbox contract (PATH points at the agent's BIN dirs, HOME /
// XDG_* / TMPDIR / OTTERS_AGENT_ROOT anchor the materialised tree).
//
//nolint:gochecknoglobals // immutable allowlist consulted by validateEnvKey
var reservedEnvKeys = map[string]struct{}{
	"PATH":              {},
	"HOME":              {},
	"XDG_CONFIG_HOME":   {},
	"XDG_CACHE_HOME":    {},
	"XDG_DATA_HOME":     {},
	"TMPDIR":            {},
	"LANG":              {},
	"OTTERS_AGENT_ROOT": {},
}

// envKeyPattern matches POSIX-style env var names: leading letter or
// underscore, then letters / digits / underscores. Uppercase only —
// lowercase env vars work but are unconventional and almost always
// a typo for a config knob (which goes through CONFIG, not ENV).
var envKeyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func validateEnvKey(key string) error {
	if key == "" {
		return fmt.Errorf("ENV key cannot be empty")
	}

	if !envKeyPattern.MatchString(key) {
		return fmt.Errorf(
			"ENV %s: key must match %s (uppercase letters, digits, underscore; cannot start with a digit)",
			key, envKeyPattern,
		)
	}

	if _, reserved := reservedEnvKeys[key]; reserved {
		return fmt.Errorf(
			"ENV %s: key is reserved by the locked-down agent env "+
				"(PATH/HOME/XDG_*/TMPDIR/LANG/OTTERS_AGENT_ROOT)",
			key,
		)
	}

	if strings.HasSuffix(key, "_API_KEY") || strings.HasSuffix(key, "_API_BASE") {
		return fmt.Errorf(
			"ENV %s: keys ending in _API_KEY / _API_BASE are reserved for provider "+
				"credentials — declare a provider model instead",
			key,
		)
	}

	return nil
}

func validate(af *Agentfile) (*Agentfile, error) {
	if err := Validate(af); err != nil {
		return nil, err
	}

	return af, nil
}
