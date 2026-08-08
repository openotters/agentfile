// Run parses an Agentfile, materializes the agent workspace, and starts the
// runtime on the chosen executor backend. Blocks until interrupted.
//
// Usage:
//
//	go run ./examples/run/ [--executor system|docker] [--runtime <path>] \
//	    [--model MODEL] [--api-key KEY] --api-base URL <Agentfile>
//
// The system backend runs the runtime as a local process; --runtime overrides
// its binary path (skipping the OCI pull). The docker backend runs it in a
// container and takes its runtime and BIN images from the Agentfile's RUNTIME /
// BIN refs (which must be present in the local Docker image store or pullable);
// --runtime does not apply there.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/google/uuid"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/executor/docker"
	"github.com/openotters/agentfile/executor/system"
	"github.com/openotters/agentfile/model"
	"github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

type options struct {
	executor string
	runtime  string
	model    string
	apiKey   string
	apiBase  string
}

func main() {
	var opts options

	flag.StringVar(&opts.executor, "executor", "system", "executor backend: system or docker")
	flag.StringVar(&opts.runtime, "runtime", "", "system only: override runtime binary path (skip OCI pull)")
	flag.StringVar(&opts.model, "model", "", "override model (provider/model)")
	flag.StringVar(&opts.apiKey, "api-key", "", "API key for the LLM provider")
	flag.StringVar(&opts.apiBase, "api-base", "", "custom API base URL")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: run [--executor system|docker] [--runtime <path>] "+
			"[--model MODEL] [--api-key KEY] --api-base URL <Agentfile>")
		os.Exit(1)
	}

	if opts.apiBase == "" {
		fatal(errors.New("must specify --api-base"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	store := memory.New()

	ref, err := build.FromFile(ctx, flag.Arg(0), store)
	if err != nil {
		fatal(err)
	}

	agent, cleanup, err := newAgent(ctx, opts, store, ref.Reference)
	if err != nil {
		fatal(err)
	}
	defer cleanup()

	if runErr := agent.Run(ctx); runErr != nil {
		fatal(runErr)
	}
}

// newAgent constructs the agent for the requested backend and returns a cleanup
// function to release backend resources.
func newAgent(
	ctx context.Context, opts options, store oras.ReadOnlyTarget, ref spec.Reference,
) (executor.Agent, func(), error) {
	switch opts.executor {
	case "system":
		agent, err := newSystemAgent(opts, store, ref)

		return agent, func() {}, err
	case "docker":
		return newDockerAgent(ctx, opts, store, ref)
	default:
		return nil, nil, fmt.Errorf("unknown --executor %q (want system or docker)", opts.executor)
	}
}

func newSystemAgent(opts options, store oras.ReadOnlyTarget, ref spec.Reference) (executor.Agent, error) {
	agentRoot, err := osfs.New(os.TempDir()).Chroot(filepath.Join("agentfile", uuid.New().String()))
	if err != nil {
		return nil, err
	}

	agentOpts := []system.AgentOption{
		system.WithStore(store),
		system.WithReference(ref),
		system.WithStaticModelResolver(opts.apiBase, opts.apiKey),
	}

	if opts.runtime != "" {
		agentOpts = append(agentOpts, system.WithAgentLocalRuntime(opts.runtime), system.WithAgentPuller(oci.NoopPuller()))
	}

	if opts.model != "" {
		agentOpts = append(agentOpts, system.WithOverrides(spec.WithModel(opts.model)))
	}

	return system.NewAgent(uuid.New(), agentRoot, agentOpts...), nil
}

func newDockerAgent(
	ctx context.Context, opts options, store oras.ReadOnlyTarget, ref spec.Reference,
) (executor.Agent, func(), error) {
	if opts.runtime != "" {
		fmt.Fprintln(os.Stderr, "note: --runtime is ignored for the docker backend (runtime comes from the RUNTIME image)")
	}

	// The agent root must live under $HOME so a VM-backed Docker (Colima,
	// Docker Desktop) can bind-mount it into the container.
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}

	root := osfs.New(filepath.Join(home, ".cache", "agentfile-run"))
	storeFor := func(spec.Reference) oras.ReadOnlyTarget { return store }

	var overrides []spec.Override
	if opts.model != "" {
		overrides = append(overrides, spec.WithModel(opts.model))
	}

	provider, err := docker.NewProvider(root, storeFor,
		docker.WithModelResolver(model.StaticResolver(opts.apiBase, opts.apiKey)),
	)
	if err != nil {
		return nil, nil, err
	}

	agent, err := provider.Create(ctx, uuid.New(), ref, overrides...)
	if err != nil {
		_ = provider.Close()

		return nil, nil, err
	}

	return agent, func() { _ = provider.Close() }, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
