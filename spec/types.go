package spec

type Agentfile struct {
	Syntax string `json:"syntax"`
	Agent  *Agent `json:"agent"`
}

type Agent struct {
	From     string            `json:"from"`
	Runtime  string            `json:"runtime,omitempty"`
	Model    string            `json:"model,omitempty"`
	Name     string            `json:"name,omitempty"`
	Contexts []*Context        `json:"contexts,omitempty"`
	Configs  []*Config         `json:"configs,omitempty"`
	Bins     []*Bin            `json:"bins,omitempty"`
	Adds     []*Add            `json:"adds,omitempty"`
	Exec     []string          `json:"exec,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Args     map[string]string `json:"args,omitempty"`
	Envs     []*Env            `json:"envs,omitempty"`
}

type Context struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Content     string `json:"content,omitempty"`
	File        string `json:"file,omitempty"`
}

type Config struct {
	Key         string `json:"key"`
	Value       any    `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type Bin struct {
	Name        string `json:"name"`
	Image       string `json:"image"`
	Description string `json:"description,omitempty"`
	Usage       string `json:"usage,omitempty"`
}

// Env declares an OS environment variable to be set on the spawned
// agent process. Unlike Config (a runtime-SDK knob the agent reads
// via the runtime API) and Arg (build-time substitution), Env values
// land directly on os/exec's Cmd.Env (system executor) and
// container.Config.Env (docker executor).
//
// Reserved keys (PATH, HOME, XDG_*, TMPDIR, LANG, OTTERS_AGENT_ROOT,
// any *_API_KEY / *_API_BASE) are rejected by Validate to keep the
// locked-down env contract intact.
type Env struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

type Add struct {
	Src         string `json:"src"`
	Dst         string `json:"dst"`
	Description string `json:"description,omitempty"`
	Content     []byte `json:"-"`
}

// Override mutates a parsed Agentfile. Overrides are applied via Apply and
// are intended for runtime field replacements (model, runtime image, etc.)
// coming from CLI flags or daemon config. Kept distinct from spec.Config,
// which is a data record declared in the Agentfile itself.
type Override func(*Agentfile)

// WithRuntime overrides Agent.Runtime.
func WithRuntime(runtime string) Override {
	return func(agentfile *Agentfile) {
		agentfile.Agent.Runtime = runtime
	}
}

// WithModel overrides Agent.Model.
func WithModel(model string) Override {
	return func(agentfile *Agentfile) {
		agentfile.Agent.Model = model
	}
}

// Apply mutates a in place by running each override. Returns a for chaining.
func (a *Agentfile) Apply(overrides ...Override) *Agentfile {
	for _, o := range overrides {
		o(a)
	}

	return a
}
