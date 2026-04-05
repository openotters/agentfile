# push

Implementation example showing how to build an agent artifact and copy it **from** a local in-memory store **to** a
remote OCI registry using `oras.Copy` with Docker credentials.

In practice, the source and destination can be any `oras.Target` — another registry, an OCI layout on disk, etc.

## Usage

```sh
go run ./examples/push/ <path-to-Agentfile> <registry-ref>

# Plain HTTP registry (e.g. localhost)
go run ./examples/push/ -plain-http <path-to-Agentfile> <registry-ref>
```

## Examples

```sh
# Push to ghcr.io
go run ./examples/push/ demo/meteo/Agentfile ghcr.io/openotters/agents/meteo:1.0.0

# Push to a local registry
go run ./examples/push/ -plain-http demo/meteo/Agentfile localhost:5000/meteo:latest
```
