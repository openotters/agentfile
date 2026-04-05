# exec

Same pipeline as [examples/run](../run/) but sends a single prompt instead of starting a long-running server:

1. **Parse** the Agentfile (model, name, tools, runtime come from there)
2. **Build** the OCI artifact into an in-memory store
3. **Execute** with `FileExecutor` — materializes the agent including the runtime binary to a temp directory
4. **Spawn** the runtime with `prompt` command and exit

## Usage

```sh
go run ./examples/exec/ [--api-key KEY] [--api-base URL] [--model MODEL] [--runtime <path>] <Agentfile> <prompt>
```

- `--model` overrides the `MODEL` instruction from the Agentfile.
- `--runtime` overrides the runtime binary with a local path (skips OCI pull).

## Example

```sh
go run ./examples/exec/ --api-key $ANTHROPIC_API_KEY demo/meteo/Agentfile "What is the weather in Lyon?"
```
