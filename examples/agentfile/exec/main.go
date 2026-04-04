// Exec parses an Agentfile, builds it, materializes the agent filesystem, and sends
// a one-shot prompt via `runtime prompt`.
//
// Usage:
//
//	go run ./examples/agentfile/exec/ --runtime <path> [--model MODEL] [--api-key KEY] [--api-base URL] <Agentfile> <prompt>
//
// Example:
//
//	go run ./examples/agentfile/exec/ --runtime ./runtime --api-key $ANTHROPIC_API_KEY demo/meteo/Agentfile "What is the weather in Lyon?"
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
	if len(args) < 2 || *runtimeFlag == "" {
		fmt.Fprintln(os.Stderr, "usage: exec --runtime <path> [flags] <Agentfile> <prompt>")
		os.Exit(1)
	}

	agentfilePath := args[0]
	prompt := args[1]

	r, err := prepare.Run(agentfilePath, *modelFlag)
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(r.Root) //nolint:errcheck

	promptArgs := []string{
		"prompt",
		"--root", r.Root,
		"--name", r.Agentfile.Agent.Name,
		"--model", r.Agentfile.Agent.Model,
	}

	if *apiKeyFlag != "" {
		promptArgs = append(promptArgs, "--api-key", *apiKeyFlag)
	}

	if *apiBaseFlag != "" {
		promptArgs = append(promptArgs, "--api-base", *apiBaseFlag)
	}

	promptArgs = append(promptArgs, prompt)

	cmd := exec.CommandContext(context.Background(), *runtimeFlag, promptArgs...) //nolint:gosec
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
