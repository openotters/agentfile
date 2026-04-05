# parse

Reads an Agentfile and dumps the parsed structure as JSON. Useful for inspecting how instructions are resolved
(heredocs, ARG substitution, context file references) without building an OCI artifact.

## Usage

```sh
go run ./examples/parse/ <path-to-Agentfile>
```

## Example

```sh
go run ./examples/parse/ demo/meteo/Agentfile
```

```json
{
  "syntax": "openotters/agentfile:1",
  "agent": {
    "from": "scratch",
    "runtime": "ghcr.io/openotters/runtime:latest",
    "model": "anthropic/claude-haiku-4-5-20251001",
    "name": "meteo",
    "contexts": [ ... ],
    "bins": [ ... ],
    "adds": [ ... ]
  }
}
```
