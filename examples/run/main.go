// Run parses an Agentfile, builds the OCI artifact in memory, materializes the agent
// filesystem using the FileExecutor (including the runtime binary from the RUNTIME OCI
// image), and starts the runtime.
//
// This demonstrates the full local pipeline:
//
//	parse → build (memory store) → execute (FileExecutor) → spawn runtime
//
// The executor writes etc/agent.yaml (spec-level config) with tool definitions, model,
// and agent name. The runtime reads it automatically from the root directory.
//
// Usage:
//
//	go run ./examples/run/ [--runtime <path>] [--model MODEL] [--api-key KEY] [--api-base URL] <Agentfile>
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	osExec "os/exec"
	"path/filepath"

	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/oci"
)

func main() {
	runtimeOverride := flag.String("runtime", "", "override runtime binary path (skip OCI pull)")
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

	af, store, _, err := build.FromFile(context.Background(), agentfilePath)
	if err != nil {
		fatal(err)
	}

	if *modelFlag != "" {
		af.Agent.Model = *modelFlag
	}

	root, err := os.MkdirTemp("", "agentfile-*")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(root) //nolint:errcheck

	e := executor.NewFileExecutor(root)
	e.BinPuller = oci.RemotePuller()

	result, err := e.Execute(context.Background(), store, "latest")
	if err != nil {
		fatal(err)
	}

	runtimeBin := filepath.Join(root, result.RuntimeBin)
	if *runtimeOverride != "" {
		runtimeBin = *runtimeOverride
	}

	serveArgs := []string{"serve", "--root", root}

	if *apiKeyFlag != "" {
		serveArgs = append(serveArgs, "--api-key", *apiKeyFlag)
	}

	if *apiBaseFlag != "" {
		serveArgs = append(serveArgs, "--api-base", *apiBaseFlag)
	}

	cmd := osExec.CommandContext(context.Background(), runtimeBin, serveArgs...) //nolint:gosec
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if runErr := cmd.Run(); runErr != nil {
		if exitErr, ok := runErr.(*osExec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}

		fatal(runErr)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
