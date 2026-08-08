//nolint:testpackage // tests unexported helpers
package docker

import (
	"strings"
	"testing"

	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/spec"
)

func TestContainerSpec_BuildConfig(t *testing.T) {
	t.Parallel()

	cs := containerSpec{
		Name:         "otters-test",
		BaseImage:    "gcr.io/distroless/static-debian12:nonroot",
		Provider:     "anthropic",
		APIKey:       "key",
		APIBase:      "",
		Model:        "anthropic/claude-haiku-4-5",
		HostGRPCPort: "65432",
	}

	cfg := cs.buildConfig()
	if cfg.Image != cs.BaseImage {
		t.Errorf("Image = %q, want %q", cfg.Image, cs.BaseImage)
	}
	if got := cfg.Entrypoint; len(got) != 1 || got[0] != "/opt/runtime/runtime" {
		t.Errorf("Entrypoint = %v, want [/opt/runtime/runtime]", got)
	}
	if got := cfg.WorkingDir; got != "/workspace" {
		t.Errorf("WorkingDir = %q, want /workspace", got)
	}
	if cfg.User != "65532:65532" {
		t.Errorf("User = %q, want 65532:65532", cfg.User)
	}
	// Cmd should pass --root / --addr 0.0.0.0:9999. The agent's
	// FHS lives directly at the container's filesystem root; no
	// /agent prefix.
	joined := strings.Join(cfg.Cmd, " ")
	if !strings.Contains(joined, "--root /") || !strings.Contains(joined, "--addr 0.0.0.0:9999") {
		t.Errorf("Cmd %q missing root/addr flags", joined)
	}
	// Env should contain the provider-prefixed key.
	envHas := func(prefix string) bool {
		for _, e := range cfg.Env {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}
	if !envHas("ANTHROPIC_API_KEY=") {
		t.Errorf("Env missing ANTHROPIC_API_KEY: %v", cfg.Env)
	}
	// Label must mark it as an openotters agent so cleanup can find it.
	if got := cfg.Labels["io.openotters.agent"]; got != "true" {
		t.Errorf("missing io.openotters.agent label: %v", cfg.Labels)
	}
}

func TestContainerSpec_BuildConfig_AppendsUserEnvs(t *testing.T) {
	t.Parallel()

	cs := containerSpec{
		BaseImage: "gcr.io/distroless/static-debian12:nonroot",
		Model:     "anthropic/m",
		Provider:  "anthropic",
		APIKey:    "k",
		BINImages: map[string]string{"ping": "ghcr.io/openotters/tools/ping:latest"},
		UserEnvs: []*spec.Env{
			{Key: "NODE_ENV", Value: "production"},
			{Key: "FEATURE_X", Value: "on"},
			// Reserved: filtered defensively even though
			// spec.Validate would reject these at build time.
			{Key: "PATH", Value: "/evil"},
			{Key: "OPENAI_API_KEY", Value: "sk_test"},
		},
	}

	cfg := cs.buildConfig()

	envHas := func(want string) {
		t.Helper()
		for _, e := range cfg.Env {
			if e == want {
				return
			}
		}
		t.Errorf("missing %q in env: %v", want, cfg.Env)
	}
	envHasNoKey := func(key string) {
		t.Helper()
		for _, e := range cfg.Env {
			if strings.HasPrefix(e, key+"=") && e != "PATH=/opt/bins" {
				t.Errorf("unexpected entry %q (key %s leaked from user envs)", e, key)
			}
		}
	}

	envHas("NODE_ENV=production")
	envHas("FEATURE_X=on")
	// PATH from the locked-down base survives; user-declared override is filtered.
	// PATH is the flat /opt/bins symlink dir, not /opt/bins/<name>.
	// Each `/opt/bins/<name>` IS the executable (a symlink to
	// /opt/bin-images/<name>/<name>), not a wrapper directory.
	envHas("PATH=/opt/bins")
	envHasNoKey("OPENAI_API_KEY")
}

func TestContainerSpec_BuildHostConfig(t *testing.T) {
	t.Parallel()

	cs := containerSpec{
		AgentRoot:    "/host/path/agent-root",
		RuntimeImage: "ghcr.io/openotters/runtime:latest",
		BINImages: map[string]string{
			"ping": "ghcr.io/openotters/tools/ping:latest",
		},
		UserMounts: []executor.Mount{
			{Host: "/host/data", Target: "/data"},
		},
		HostGRPCPort: "65432",
	}

	hc := cs.buildHostConfig()
	if hc == nil {
		t.Fatal("nil HostConfig")
	}

	// Every advertised mount must show up: workspace bind, runtime
	// image-mount, ping image-mount, user data bind. Layout enforced
	// by string match — exact field equality changes if oras-go or
	// moby renames any of these.
	gotByTarget := map[string]string{}
	for _, m := range hc.Mounts {
		gotByTarget[m.Target] = string(m.Type) + ":" + m.Source
	}

	wants := map[string]string{
		// Per-FHS-subdir mounts: each top-level piece of the host
		// agent tree surfaces at its standard Linux path. No /agent
		// prefix.
		"/etc/context":         "bind:/host/path/agent-root/etc/context",
		"/etc/data":            "bind:/host/path/agent-root/etc/data",
		"/etc/Agentfile":       "bind:/host/path/agent-root/etc/Agentfile",
		"/etc/agent.yaml":      "bind:/host/path/agent-root/etc/agent.yaml",
		"/home":                "bind:/host/path/agent-root/home",
		"/tmp":                 "bind:/host/path/agent-root/tmp",
		"/var/lib":             "bind:/host/path/agent-root/var/lib",
		"/workspace":           "bind:/host/path/agent-root/workspace",
		"/opt/runtime":         "image:ghcr.io/openotters/runtime:latest",
		"/opt/bin-images/ping": "image:ghcr.io/openotters/tools/ping:latest",
		// Per-BIN symlinks live on the host under <root>/opt/bins
		// and surface here as a read-only bind. Each entry inside
		// is a string-target symlink to /opt/bin-images/<name>/<name>.
		"/opt/bins": "bind:/host/path/agent-root/opt/bins",
		"/data":     "bind:/host/data",
	}

	for tgt, want := range wants {
		if got := gotByTarget[tgt]; got != want {
			t.Errorf("mount[%s] = %q, want %q", tgt, got, want)
		}
	}

	// Port mapping: 9999/tcp → 127.0.0.1:65432. We assert by
	// scanning the bindings rather than reconstructing the map
	// key (network.Port is content-addressed via ParsePort and
	// can't be cast from a string).
	foundPort := false
	for _, bindings := range hc.PortBindings {
		for _, b := range bindings {
			if b.HostIP.String() == "127.0.0.1" && b.HostPort == "65432" {
				foundPort = true
			}
		}
	}
	if !foundPort {
		t.Errorf("port binding for 9999/tcp → 127.0.0.1:65432 not found: %v", hc.PortBindings)
	}
}
