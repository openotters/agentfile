# import

Loads an exported agent artifact JSON file (produced by [examples/export](../export/)) back into an in-memory OCI store.
With a registry reference, pushes the artifact to the target repository using Docker credentials.

## Usage

```sh
# Import into memory (print digest only)
go run ./examples/import/ <input.json>

# Import and push to a registry
go run ./examples/import/ <input.json> <registry-ref>

# Push to a plain HTTP registry
go run ./examples/import/ -plain-http <input.json> <registry-ref>
```

## Examples

```sh
# Roundtrip: export then import
go run ./examples/export/ demo/meteo/Agentfile meteo.json
go run ./examples/import/ meteo.json

# Import and push to a remote registry
go run ./examples/import/ meteo.json ghcr.io/openotters/agents/meteo:1.0.0
```
