# Agentfile

[![Go Reference](https://pkg.go.dev/badge/github.com/openotters/agentfile.svg)](https://pkg.go.dev/github.com/openotters/agentfile)
[![Go Report Card](https://goreportcard.com/badge/github.com/openotters/agentfile)](https://goreportcard.com/report/github.com/openotters/agentfile)
[![golangci-lint](https://github.com/openotters/agentfile/actions/workflows/golangci.yml/badge.svg)](https://github.com/openotters/agentfile/actions/workflows/golangci.yml)
[![License](https://img.shields.io/github/license/openotters/agentfile)](LICENSE)

Dockerfile for AI agents — define, build, and distribute autonomous agents as OCI artifacts.

```go
af, store, digest, err := build.BuildFromFile(ctx, "Agentfile")
```

<!-- TOC -->
* [Agentfile](#agentfile)
  * [Quick start](#quick-start)
  * [Format](#format)
  * [Examples](#examples)
  * [Security](#security)
  * [Specification](#specification)
<!-- TOC -->

## Quick start

```go
// One-liner: parse → resolve → build
af, store, digest, err := build.BuildFromFile(ctx, "Agentfile")

// Materialize to disk
e := executor.NewFileExecutor("/tmp/agent")
e.BinPuller = oci.RemotePuller()
result, err := e.Execute(ctx, store, "latest")
```

## Format

```agentfile
FROM scratch

RUNTIME ghcr.io/openotters/runtime:latest
MODEL anthropic/claude-haiku-4-5-20251001
NAME meteo

CONTEXT SOUL "Agent personality" <<EOF
You are a weather assistant.
EOF

CONFIG max-tokens=1024

BIN wget ghcr.io/openotters/tools/wget:latest "Fetch URL content"
BIN jq   ghcr.io/openotters/tools/jq:latest  "Extract fields from JSON"

ADD cities.json /data/cities.json "Known cities"

LABEL description="Weather assistant"
```

| Instruction | Purpose                                              |
|-------------|------------------------------------------------------|
| `FROM`      | Base agent — `scratch` or parent ref for inheritance |
| `RUNTIME`   | Runtime OCI image (follows bin spec)                 |
| `MODEL`     | LLM provider/model                                   |
| `NAME`      | Agent name                                           |
| `CONTEXT`   | System prompt context (inline, heredoc, `file://`)   |
| `CONFIG`    | Runtime key/value parameter                          |
| `BIN`       | Tool binary as an OCI image                          |
| `ADD`       | Data file bundled into the workspace                 |
| `LABEL`     | OCI annotation                                       |
| `ARG`       | Build-time `${VAR}` substitution                     |

## Examples

Each example is a standalone `go run` program.

```sh
# Build and inspect
go run ./examples/build/ demo/meteo/Agentfile

# Build and push
go run ./examples/build/ demo/meteo/Agentfile ghcr.io/openotters/agents/meteo:1.0.0

# Pull from registry
go run ./examples/pull/ ghcr.io/openotters/agents/meteo:1.0.0

# Run end-to-end (pulls runtime + tools from OCI)
go run ./examples/exec/ --api-key $ANTHROPIC_API_KEY demo/meteo/Agentfile "What is the weather in Lyon?"
```

See [`examples/`](examples/) for the full list: parse, validate, build, push, pull, export, import, run, exec.

## Security

**What's enforced today:**

- **Closed capabilities** — zero tools by default, each `BIN` grants exactly one. No shell, no exec.
- **No secrets in artifacts** — `MODEL` names the model, API keys are injected by the runtime. Artifacts are safe to share and publish.
- **OCI supply chain** — runtime, tools, and base agents are content-addressed OCI refs. Pin a digest for full reproducibility. Registries provide signing and scanning.
- **Static binaries** — tools are single static binaries (`FROM scratch`). No interpreters, no dynamic linking, minimal attack surface.
- **Sandboxed filesystem layout** — `etc/` and `usr/bin/` are designed read-only, `workspace/` and `tmp/` are read-write.
- **Auditable** — the full capability set is visible in the Agentfile and preserved in the OCI config blob.

**Not yet enforced (requires containerized executor):**

- Read-only mounts — `FileExecutor` uses the local filesystem without enforcement. Docker/K8s executors would mount `etc/` and `usr/bin/` as immutable volumes.
- Namespace isolation — the agent root is not sandboxed on the local filesystem. Containerized executors provide process and network isolation.

## Specification

[`specs/AGENTFILE-v0.0.1.md`](specs/AGENTFILE-v0.0.1.md)
