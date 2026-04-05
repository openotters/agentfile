# run

Implementation example showing the full local agent pipeline:

1. **Parse** the Agentfile (model, name, tools, runtime come from there)
2. **Build** the OCI artifact into an in-memory store
3. **Execute** with `FileExecutor` — materializes context files, data files, tool binaries, and the **runtime binary** (pulled from the `RUNTIME` OCI image following the bin spec) into a temp directory
4. **Spawn** the runtime binary at `usr/local/bin/runtime`

The executor is pluggable — this example uses `FileExecutor` (local filesystem), but the same `Executor` interface
can be implemented for Docker, Kubernetes, or other targets.

## Usage

```sh
go run ./examples/run/ [--api-key KEY] [--api-base URL] [--model MODEL] [--runtime <path>] <Agentfile>
```

- `--model` overrides the `MODEL` instruction from the Agentfile.
- `--runtime` overrides the runtime binary with a local path (skips OCI pull).

## Example

```sh
# Runtime pulled from the RUNTIME OCI image
go run ./examples/run/ --api-key $ANTHROPIC_API_KEY demo/meteo/Agentfile

# Override with a local runtime binary
go run ./examples/run/ --runtime ./openotters-runtime --api-key $ANTHROPIC_API_KEY demo/meteo/Agentfile
```
