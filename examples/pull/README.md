# pull

Implementation example showing how to copy an agent artifact **from** a remote OCI registry **to** a local in-memory
store using `oras.Copy`, then load and inspect the Agentfile config.

In practice, the destination can be any `oras.Target` — another registry, an OCI layout on disk, etc.

## Usage

```sh
go run ./examples/pull/ <registry-ref>

# Plain HTTP registry (e.g. localhost)
go run ./examples/pull/ -plain-http <registry-ref>
```

## Examples

```sh
# Pull from ghcr.io
go run ./examples/pull/ ghcr.io/openotters/agents/meteo:1.0.0

# Pull from a local registry
go run ./examples/pull/ -plain-http localhost:5000/meteo:latest
```
