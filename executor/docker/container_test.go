//nolint:testpackage // tests unexported helpers
package docker

import (
	"strings"
	"testing"

	"github.com/openotters/agentfile/executor"
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
	// Cmd should pass --root /workspace --addr 0.0.0.0:9999
	joined := strings.Join(cfg.Cmd, " ")
	if !strings.Contains(joined, "--root /workspace") || !strings.Contains(joined, "--addr 0.0.0.0:9999") {
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
		HostGRPCPort:  "65432",
		NetworkAccess: true,
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
		"/workspace":     "bind:/host/path/agent-root",
		"/opt/runtime":   "image:ghcr.io/openotters/runtime:latest",
		"/opt/bins/ping": "image:ghcr.io/openotters/tools/ping:latest",
		"/data":          "bind:/host/data",
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
