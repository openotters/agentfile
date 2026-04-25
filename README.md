# Agentfile

[![Go Reference](https://pkg.go.dev/badge/github.com/openotters/agentfile.svg)](https://pkg.go.dev/github.com/openotters/agentfile)
[![Go Report Card](https://goreportcard.com/badge/github.com/openotters/agentfile)](https://goreportcard.com/report/github.com/openotters/agentfile)
[![golangci-lint](https://github.com/openotters/agentfile/actions/workflows/golangci.yml/badge.svg)](https://github.com/openotters/agentfile/actions/workflows/golangci.yml)
[![License](https://img.shields.io/github/license/openotters/agentfile)](LICENSE)

**Dockerfile for AI agents** — define, build, and distribute autonomous agents as OCI artifacts.

<!-- TOC -->
* [Agentfile](#agentfile)
  * [Why not just a Dockerfile?](#why-not-just-a-dockerfile)
    * [The key insight](#the-key-insight)
  * [Quick start](#quick-start)
  * [Format](#format)
  * [Examples](#examples)
  * [Security](#security)
  * [Specification](#specification)
<!-- TOC -->

## Why not just a Dockerfile?

Dockerfiles build **containers** — generic process sandboxes. Agentfiles build **agents** — autonomous AI processes with
specific lifecycle requirements. The Agentfile is purpose-built for what agents actually need:

|                       | Dockerfile                                      | Agentfile                                                                                     |
|-----------------------|-------------------------------------------------|-----------------------------------------------------------------------------------------------|
| **Unit of work**      | Container (generic process)                     | Agent (LLM + tools + context)                                                                 |
| **Model declaration** | None — bake it into env vars or entrypoint args | First-class: `MODEL anthropic/claude-sonnet-4-20250514`                                       |
| **Tool management**   | `COPY` binaries, manage PATH, hope they work    | `BIN wget ghcr.io/openotters/tools/wget:latest` — pulled at deploy, isolated, self-describing |
| **System prompt**     | Doesn't exist                                   | `CONTEXT SOUL <<EOF ... EOF` — versioned, inheritable, composable                             |
| **Typed config**      | `ENV KEY=value` (strings only)                  | `CONFIG max-tokens=1024` — int, float, bool, string with validation                           |
| **Inheritance**       | `FROM` copies layers (build-time)               | `FROM parent-agent` merges contexts, tools, configs (semantic)                                |
| **Runtime contract**  | Any process, any protocol                       | gRPC API (`agent/api/v1/agent.proto`) — Chat, Stream, Health, Ready                           |
| **Secrets**           | Leaked into layers, need multi-stage builds     | Never in artifact — `MODEL` names the model, keys are injected at runtime                     |
| **Capabilities**      | Full Linux, must `drop` manually                | Zero by default — each `BIN` grants exactly one tool                                          |
| **Distribution**      | OCI image (layers, filesystem)                  | OCI artifact (structured: config blob + typed layers)                                         |
| **Composition**       | `docker-compose.yml`                            | `agent-compose.yml` (planned) — topology-aware agent networks                                 |

### The key insight

A Dockerfile answers: *"How do I package a process?"*

An Agentfile answers: *"What does this agent know, what can it do, and how does it think?"*

```agentfile
# This is not a container. This is an agent.
FROM scratch

RUNTIME ghcr.io/openotters/runtime:latest
MODEL ollama/qwen3:8b
NAME meteo

CONTEXT SOUL <<EOF
You are a weather assistant.
Always report temperature in °C.
EOF

BIN wget ghcr.io/openotters/tools/wget:latest "Fetch URL content"
BIN jq   ghcr.io/openotters/tools/jq:latest  "Extract fields from JSON"

ADD cities.json /data/cities.json "Known cities"

EXEC ["serve"]
```

The model, personality, tools, and data are **declarative, versionable, and inheritable** — not hidden in scripts,
environment variables, or Docker layers.

## Quick start

```bash
# Build an agent from an Agentfile
go run ./examples/build/ demo/meteo/Agentfile

# Run it locally (needs a runtime binary + model endpoint)
go run ./examples/run/ \
  --runtime ./runtime \
  --model ollama/qwen3:8b \
  --api-base http://localhost:11434/v1 \
  demo/meteo/Agentfile
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
CONFIG temperature=0.7

BIN wget ghcr.io/openotters/tools/wget:latest "Fetch URL content"
BIN jq   ghcr.io/openotters/tools/jq:latest  "Extract fields from JSON"

ADD cities.json /data/cities.json "Known cities"

EXEC ["serve"]

LABEL description="Weather assistant"
```

| Instruction | Purpose                                              |
|-------------|------------------------------------------------------|
| `FROM`      | Base agent — `scratch` or parent ref for inheritance |
| `RUNTIME`   | Runtime OCI image (follows bin spec)                 |
| `MODEL`     | LLM provider/model                                   |
| `NAME`      | Agent name                                           |
| `CONTEXT`   | System prompt context (inline, heredoc, `file://`)   |
| `CONFIG`    | Typed runtime parameter (int, float, bool, string)   |
| `BIN`       | Tool binary as an OCI image                          |
| `ADD`       | Data file bundled into the workspace                 |
| `EXEC`      | Runtime invocation args (JSON array)                 |
| `LABEL`     | OCI annotation                                       |
| `ARG`       | Build-time `${VAR}` substitution                     |

## Examples

Each example is a standalone `go run` program.

```sh
# Build and inspect
go run ./examples/build/ demo/meteo/Agentfile

# Push to registry
go run ./examples/push/ demo/meteo/Agentfile ghcr.io/openotters/agents/meteo:1.0.0

# Pull from registry
go run ./examples/pull/ ghcr.io/openotters/agents/meteo:1.0.0

# Run locally
go run ./examples/run/ --runtime ./runtime --model ollama/qwen3:8b --api-base http://localhost:11434/v1 demo/meteo/Agentfile
```

See [`examples/`](examples/) for the full list.

## Security

**What's enforced today:**

- **Closed capabilities** — zero tools by default, each `BIN` grants exactly one. No shell, no exec.
- **No secrets in artifacts** — `MODEL` names the model, API keys are injected by the runtime. Artifacts are safe to
  share and publish.
- **OCI supply chain** — runtime, tools, and base agents are content-addressed OCI refs. Pin a digest for full
  reproducibility. Registries provide signing and scanning.
- **Static binaries** — tools are single static binaries (`FROM scratch`). No interpreters, no dynamic linking, minimal
  attack surface.
- **Sandboxed filesystem layout** — `etc/` and `usr/bin/` are designed read-only, `workspace/` and `tmp/` are
  read-write.
- **Auditable** — the full capability set is visible in the Agentfile and preserved in the OCI config blob.
- **Runtime API contract** — runtimes must implement a defined gRPC API (`agent/api/v1/agent.proto`). No arbitrary
  process execution.

**Not yet enforced (requires containerized executor):**

- Read-only mounts — the local system executor uses the filesystem without enforcement. Docker/K8s executors would mount
  `etc/` and `usr/bin/` as immutable volumes.
- Namespace isolation — the agent root is not sandboxed on the local filesystem. Containerized executors provide process
  and network isolation.

## Specification

[`AGENTFILE-v1.0.0.md`](AGENTFILE-v1.0.0.md)