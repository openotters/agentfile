// Run parses an Agentfile, builds it, materializes the agent filesystem, and starts
// the runtime.
//
// Usage:
//
//	go run ./examples/agentfile/run/ --runtime <path> [--model MODEL] [--api-key KEY] [--api-base URL] <Agentfile>
//
// Example:
//
//	go run ./examples/agentfile/run/ --runtime ./runtime --api-key $ANTHROPIC_API_KEY demo/meteo/Agentfile
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/openotters/agentfile/examples/agentfile/internal/prepare"
)

func main() {
	runtimeFlag := flag.String("runtime", "", "path to the runtime binary (required)")
	modelFlag := flag.String("model", "", "override model (provider/model)")
	apiKeyFlag := flag.String("api-key", "", "API key for the LLM provider")
	apiBaseFlag := flag.String("api-base", "", "custom API base URL")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 || *runtimeFlag == "" {
		fmt.Fprintln(os.Stderr,
			"usage: run --runtime <path> [--model MODEL] [--api-key KEY] [--api-base URL] <Agentfile>")
		os.Exit(1)
	}

	r, err := prepare.Run(args[0], *modelFlag)
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(r.Root) //nolint:errcheck

	serveArgs := prepare.ServeArgs(r, *apiKeyFlag, *apiBaseFlag)

	cmd := exec.CommandContext(context.Background(), *runtimeFlag, serveArgs...) //nolint:gosec
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if runErr := cmd.Run(); runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}

		fatal(runErr)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
