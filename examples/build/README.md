# build

Parses an Agentfile and builds the OCI artifact. Without a registry reference, prints the digest and resolved
configuration as JSON. With a registry reference, pushes the artifact using Docker credentials.

## Usage

```sh
# Build only (in-memory)
go run ./examples/build/ <path-to-Agentfile>

# Build and push to a registry
go run ./examples/build/ <path-to-Agentfile> <registry-ref>

# Push to a plain HTTP registry (e.g. localhost)
go run ./examples/build/ -plain-http <path-to-Agentfile> <registry-ref>
```

## Examples

```sh
# Inspect the build output
go run ./examples/build/ demo/meteo/Agentfile

# Push to ghcr.io
go run ./examples/build/ demo/meteo/Agentfile ghcr.io/openotters/agents/meteo:1.0.0

# Push to a local registry
go run ./examples/build/ -plain-http demo/meteo/Agentfile localhost:5000/meteo:latest
```
