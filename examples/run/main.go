// Run parses an Agentfile, materializes the agent workspace, and starts
// the runtime. Blocks until interrupted.
//
// Usage:
//
//	go run ./examples/run/ [--runtime <path>] [--model MODEL] [--api-key KEY] [--api-base URL] <Agentfile>
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/google/uuid"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/agent/system"
	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

func main() {
	runtimeFlag := flag.String("runtime", "", "override runtime binary path (skip OCI pull)")
	modelFlag := flag.String("model", "", "override model (provider/model)")
	apiKeyFlag := flag.String("api-key", "", "API key for the LLM provider")
	apiBaseFlag := flag.String("api-base", "", "custom API base URL")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr,
			"usage: run [--runtime <path>] [--model MODEL] [--api-key KEY] [--api-base URL] <Agentfile>")
		os.Exit(1)
	}

	agentfilePath := args[0]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *apiBaseFlag == "" {
		fatal(errors.New("must specify --api-base"))
	}

	store := memory.New()

	ref, err := build.FromFile(ctx, agentfilePath, store)
	if err != nil {
		fatal(err)
	}

	root, err := osfs.New(os.TempDir()).Chroot("agentfile")
	if err != nil {
		fatal(err)
	}

	agentRoot, err := root.Chroot(uuid.New().String())
	if err != nil {
		fatal(err)
	}

	opts := []system.AgentOption{
		system.WithStore(store),
		system.WithReference(ref.Reference),
		system.WithStaticModelResolver(*apiBaseFlag, *apiKeyFlag),
	}

	if *runtimeFlag != "" {
		opts = append(opts, system.WithAgentLocalRuntime(*runtimeFlag), system.WithAgentPuller(oci.NoopPuller()))
	}

	if *modelFlag != "" {
		opts = append(opts, system.WithOverrides(spec.WithModel(*modelFlag)))
	}

	a := system.NewAgent(uuid.New(), agentRoot, opts...)

	if err := a.Run(ctx); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
