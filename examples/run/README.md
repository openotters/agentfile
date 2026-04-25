# run

Parses an Agentfile, materializes the agent workspace into a temp chroot,
pulls the runtime binary from its OCI ref (or reuses a local override),
starts the runtime subprocess, and blocks until interrupted. The most
end-to-end of the examples — if it works, your Agentfile + runtime + model
endpoint are all wired up correctly.

## Usage

```sh
go run ./examples/run/ \
  [--runtime <path>] [--model PROVIDER/MODEL] \
  [--api-key KEY] --api-base URL \
  <path-to-Agentfile>
```

| Flag          | Purpose                                                                 |
|---------------|-------------------------------------------------------------------------|
| `--runtime`   | Override the RUNTIME OCI ref with a local binary path (skips the pull). |
| `--model`     | Override the Agentfile's `MODEL` directive.                             |
| `--api-key`   | LLM provider API key (may also come from the provider's own env var).   |
| `--api-base`  | **Required.** Base URL the runtime should dial for the LLM endpoint.    |

## Examples

```sh
# Local Ollama + a local runtime binary
go run ./examples/run/ \
  --runtime ./runtime \
  --model ollama/qwen3:8b \
  --api-base http://localhost:11434/v1 \
  demo/meteo/Agentfile

# Anthropic, with the runtime pulled from its OCI ref
go run ./examples/run/ \
  --api-key "$ANTHROPIC_API_KEY" \
  --api-base https://api.anthropic.com \
  demo/meteo/Agentfile
```

Press Ctrl-C to stop. The runtime subprocess is sent SIGINT and given a
bounded drain period before exit.
