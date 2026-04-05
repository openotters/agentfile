# Agentfile Specification

An Agentfile is a declarative build specification for OpenOtters agents. It describes everything an agent needs —
runtime, model, personality, binaries, data, memory — in a single file that can be built into an OCI artifact.

<!-- TOC -->
* [Agentfile Specification](#agentfile-specification)
  * [Syntax Directive](#syntax-directive)
  * [Instruction Reference](#instruction-reference)
    * [FROM](#from)
      * [Inheritance](#inheritance)
    * [RUNTIME](#runtime)
    * [MODEL](#model)
    * [NAME](#name)
    * [CONTEXT](#context)
    * [CONFIG](#config)
    * [BIN](#bin)
      * [Binary OCI Image Structure](#binary-oci-image-structure)
        * [Annotations](#annotations)
        * [Binary Resolution](#binary-resolution)
        * [Design Rationale](#design-rationale)
        * [Examples](#examples)
    * [ADD](#add)
    * [LABEL](#label)
    * [ARG](#arg)
  * [Complete Example](#complete-example)
  * [Agent Filesystem Layout](#agent-filesystem-layout)
    * [Reserved Context: AGENT.md](#reserved-context-agentmd)
  * [OCI Artifact Structure](#oci-artifact-structure)
    * [Manifest](#manifest)
    * [Config Blob](#config-blob)
    * [Layers](#layers)
    * [Example](#example)
  * [Push & Pull](#push--pull)
    * [Push](#push)
    * [Pull](#pull)
  * [Export & Import](#export--import)
    * [Export](#export)
    * [Import](#import)
  * [Out of Scope: Channels & Neighbors](#out-of-scope-channels--neighbors)
    * [Channels](#channels)
    * [Neighbors (Inter-Agent Communication)](#neighbors-inter-agent-communication)
    * [Why This Separation Matters](#why-this-separation-matters)
  * [Design Principles](#design-principles)
<!-- TOC -->

## Syntax Directive

Optional. Must be the very first line, before any instruction.

```agentfile
# syntax=openotters/agentfile:1
```

If omitted, `openotters/agentfile:1` is assumed.

## Instruction Reference

### FROM

**Required. Must be the first instruction.**

Declares the base for the agent. Either `FROM scratch` (empty agent) or `FROM <agent-ref>` (inherit from a parent
agent artifact).

```agentfile
FROM scratch
FROM ghcr.io/openotters/agents/base-researcher:v1.0
```

An agent can only inherit from one parent (no diamond dependencies).

#### Inheritance

When using `FROM <agent-ref>`, the child inherits the parent's full definition and can override or extend it:

| Instruction             | Behavior                                           |
|-------------------------|----------------------------------------------------|
| `RUNTIME`               | Overrides parent, clears all accumulated `CONFIG`s |
| `MODEL`, `NAME`         | Overrides parent                                   |
| `CONTEXT`               | Same-name overrides parent, new names appended     |
| `CONFIG`                | Appended (cleared if `RUNTIME` is overridden)      |
| `BIN`                   | Appended to parent's binary list                   |
| `ADD`                   | Appended to parent's files                         |
| `LABEL`                 | Merged (child wins on key conflicts)               |

### RUNTIME

Specifies the OCI image containing the agent runtime binary. The image **must** follow the
[Binary OCI Image Structure](#binary-oci-image-structure) — the same `vnd.openotters.bin.*` annotation contract
used by `BIN` instructions. The executor pulls the image, extracts the binary, and places it at
`usr/local/bin/runtime` in the agent filesystem.

Setting `RUNTIME` overrides any previous `RUNTIME` instruction and **clears all accumulated `CONFIG` entries**,
since configuration keys are runtime-specific.

```agentfile
RUNTIME ghcr.io/openotters/runtime:latest
```

### MODEL

Specifies the LLM model. Format: `{provider}/{model}`. Credentials are resolved externally (env vars, provider
config) — the Agentfile never contains API keys.

```agentfile
MODEL anthropic/claude-haiku-4-5-20251001
MODEL openai/gpt-4o
```

### NAME

Sets the agent identity.

```agentfile
NAME meteo
```

### CONTEXT

Defines a named context file that shapes the agent's behavior. Each context has a name, an optional description, and
content provided inline (heredoc) or from a file reference.

```agentfile
# Inline with description
CONTEXT SOUL "Agent personality and core instructions" <<EOF
You are a weather assistant.
Always report temperature in °C.
EOF

# Inline without description
CONTEXT IDENTITY <<EOF
Name: Meteo Bot
EOF

# From file (path relative to the Agentfile directory)
CONTEXT KNOWLEDGE file://knowledge/cities.md

# From file with description
CONTEXT SAFETY "Safety guidelines" file://safety/rules.md
```

Format: `CONTEXT <name> [description] [file://<path> | <<MARKER ... MARKER]`

- `name` — identifier (used as filename: `{name}.md`)
- `description` — optional quoted string
- `file://<path>` — read content from a file, path relative to the Agentfile directory
- `<<MARKER` — inline content via heredoc, terminated by `MARKER` on its own line

If the same name appears more than once, the last definition wins (override semantics).

Well-known context names:

- `SOUL` — personality, tone, core instructions
- `IDENTITY` — name, role, self-description
- `AGENT` — **reserved**, auto-generated at runtime (tools, data files, filesystem layout)

### CONFIG

Declares configuration options. Each key has an optional default value and description. Config keys are tunable
parameters that can be overridden at deploy time.

```agentfile
CONFIG max-tokens=1024 "Maximum output tokens per response"
CONFIG max-iterations=10 "Maximum tool iterations per turn"
CONFIG memory-strategy=summarize "Memory compaction strategy"

# Required (no default, trailing !)
CONFIG api-base! "API base URL for the LLM provider"

# Optional with no default
CONFIG custom-header "Custom HTTP header for tool requests"
```

Format: `CONFIG <key[!]>[=default] [description]`

- Trailing `!` marks the key as required — deploy fails if no value is provided.

### BIN

Declares a binary available to the agent. A binary has a name and an OCI image reference. Description and usage
guidelines are optional. Binary images are resolved at deploy time, not at build time.

```agentfile
BIN wget ghcr.io/openotters/tools/wget:latest
BIN jq ghcr.io/openotters/tools/jq:latest "Extract fields from JSON"
BIN cat ghcr.io/openotters/tools/cat:latest "Read file contents"

# With usage guidelines
BIN jq ghcr.io/openotters/tools/jq:latest "JSON processor" <<EOF
First line is the jq expression (e.g. .current.temperature_2m).
Rest of the input is the JSON to process.
EOF
```

Format: `BIN <name> <oci-ref> [description] [<<MARKER usage MARKER]`

- `name` — binary identifier presented to the LLM
- `oci-ref` — OCI image reference (pulled at deploy time)
- `description` — optional one-line quoted string
- `usage` — optional multi-line guidelines via heredoc

#### Binary OCI Image Structure

A bin is a **regular OCI image** — any image that carries the `vnd.openotters.bin.*` annotations. There is no
special base image requirement: the image can be built `FROM scratch`, `FROM alpine`, or any other base. The
annotations tell the runtime where to find the binary and its metadata inside the image filesystem.

It is recommended to set an `ENTRYPOINT` in the Dockerfile so the image remains usable as a standalone container
(e.g. `docker run ghcr.io/openotters/tools/jq:latest`). However, the Agentfile executor **ignores** the
entrypoint — binary resolution relies exclusively on the `vnd.openotters.bin.*` annotations. This removes
ambiguity: an image may have multiple executables, shell wrappers, or symlinks, but the annotations define exactly
which binary the agent uses.

##### Annotations

The image manifest **must** carry annotations that describe the bin:

| Annotation                       | Required | Type   | Default     | Description                                  |
|----------------------------------|----------|--------|-------------|----------------------------------------------|
| `vnd.openotters.bin.name`        | yes      | string | —           | Binary name (e.g. `wget`, `jq`)              |
| `vnd.openotters.bin.path`        | no       | path   | `/`         | Directory containing the binary in the image |
| `vnd.openotters.bin.description` | no       | string | —           | One-line description for the LLM             |
| `vnd.openotters.bin.usage`       | no       | path   | `/USAGE.md` | Path to a USAGE.md file inside the image     |

The runtime resolves the binary location as `{path}/{name}` (e.g. `/bin/wget` when `path=/bin` and `name=wget`,
or `/wget` when path is defaulted).

- `vnd.openotters.bin.description` is a **string value** directly in the annotation.
- `vnd.openotters.bin.usage` points to a **file inside the image** — usage guidelines can be rich, multiline
  markdown that the runtime injects directly into the agent's context.

This makes bin images **self-describing**: a registry can be browsed for available binaries without needing an
Agentfile. When the Agentfile `BIN` instruction provides a description or usage, the **Agentfile wins** (explicit
override over embedded default).

The `vnd.openotters.bin.*` annotations are a **public convention** — any OCI image can adopt them to declare
that it contains an executable binary with associated metadata. This allows tooling outside of the Agentfile
ecosystem (registries, CI pipelines, other agent frameworks) to discover and consume bin images using the same
annotation contract.

##### Binary Resolution

The runtime uses the `vnd.openotters.bin.name` and `vnd.openotters.bin.path` annotations to locate the binary:

1. Compute the full path: `{path}/{name}` (with path defaulting to `/`)
2. Extract the binary from the image filesystem at that path
3. Place it at `usr/bin/{name}` in the agent filesystem

##### Design Rationale

- **Regular OCI images** — bins are standard images, buildable with any Dockerfile or OCI build tool. No special
  image format or scratch-only constraint.
- **Annotation-driven discovery** — the `vnd.openotters.bin.*` annotations make the binary location explicit.
  No entrypoint metadata, PATH resolution, or symlink traversal needed.
- **Minimal recommended** — while any base is supported, `FROM scratch` with a static binary produces images in
  the single-digit MB range. This keeps pull times fast and storage cheap.
- **Multi-arch support** — bin images can use OCI image indexes (manifest lists) for multi-platform support.
  The runtime resolves the correct platform manifest automatically (matching `GOOS`/`GOARCH`).

##### Examples

Scratch-based (binary at root):

```
image
  annotations:
    vnd.openotters.bin.name: jq
    # vnd.openotters.bin.path defaults to "/"  → binary at /jq
    # vnd.openotters.bin.usage defaults to "/USAGE.md"
  filesystem:
    /jq
    /USAGE.md
```

Standard layout (binary in /bin):

```
image
  annotations:
    vnd.openotters.bin.name: jq
    vnd.openotters.bin.path: /bin
    vnd.openotters.bin.usage: /doc/USAGE.md
  filesystem:
    /bin/jq
    /doc/USAGE.md
```

### ADD

Adds local files into the agent artifact at build time. These become data files in `etc/data/`. An optional
description is included in the auto-generated `AGENT.md` so the agent knows what each file contains.

```agentfile
ADD cities.json /data/cities.json "Known cities with lat/lon coordinates"
ADD prompts/system.txt /data/system.txt "System prompt template"
ADD config.yaml /data/config.yaml
```

Format: `ADD <src> <dst> [description]`

- `src` — local file path, relative to the Agentfile directory
- `dst` — destination path within the agent's data directory
- `description` — optional quoted string (presented to the agent via AGENT.md)

At runtime, ADD files are placed in `etc/data/` and tools execute with that as their working directory, so agents
can reference files by their basename directly.

### LABEL

OCI annotations on the output artifact.

```agentfile
LABEL description="Weather assistant using Open-Meteo API"
LABEL maintainer="romain@openotters.io"
LABEL org.opencontainers.image.version="1.0.0"
```

Format: `LABEL <key>=<value>`

### ARG

Build-time variables with optional defaults. Substituted as `${VAR}` in any subsequent instruction value.

```agentfile
ARG MODEL=anthropic/claude-haiku-4-5-20251001
ARG MAX_TOKENS=1024

MODEL ${MODEL}
CONFIG max-tokens=${MAX_TOKENS}
```

Format: `ARG <key>[=default]`

ARGs are expanded in all instruction values that follow the ARG declaration. Undefined variables are left as-is.

## Complete Example

```agentfile
# syntax=openotters/agentfile:1

FROM scratch

RUNTIME ghcr.io/openotters/runtime:latest
MODEL anthropic/claude-haiku-4-5-20251001
NAME meteo

LABEL description="Weather assistant using Open-Meteo API"
LABEL maintainer="romain.dary@gmail.com"

CONTEXT SOUL "Agent personality and core instructions" <<EOF
You are a weather assistant. You provide current weather conditions for known cities.

Use wget to fetch from the Open-Meteo API:
https://api.open-meteo.com/v1/forecast?latitude={lat}&longitude={lon}&current=temperature_2m,wind_speed_10m

Then use jq to extract the relevant fields.

Only provide weather for cities listed in cities.json.
Always report temperature in °C and wind speed in km/h.
EOF

CONTEXT IDENTITY <<EOF
Name: Meteo Bot
EOF

CONFIG max-tokens=1024 "Maximum output tokens per response"
CONFIG max-iterations=10 "Maximum tool iterations per turn"

BIN wget ghcr.io/openotters/tools/wget:latest "Fetch URL content"
BIN jq ghcr.io/openotters/tools/jq:latest "Extract fields from JSON"
BIN cat ghcr.io/openotters/tools/cat:latest "Read file contents"

ADD cities.json /data/cities.json "Known cities with lat/lon coordinates"
```

## Agent Filesystem Layout

At deploy or run time, an agent is materialized as a directory following Linux FHS conventions. Immutable paths can
be mounted read-only in containerized deployments.

```
<agent-root>/
├── etc/
│   ├── agent.yaml                # spec-level agent config (generated by executor)
│   ├── context/                  # from CONTEXT instructions + auto-generated AGENT.md
│   │   ├── AGENT.md              # auto-generated (reserved)
│   │   ├── SOUL.md
│   │   └── IDENTITY.md
│   └── data/                     # from ADD instructions
│       └── cities.json
├── usr/
│   ├── local/
│   │   └── bin/
│   │       └── runtime           # runtime binary (pulled from RUNTIME OCI image)
│   └── bin/                      # tool binaries (pulled from BIN OCI images)
│       ├── wget
│       ├── jq
│       └── cat
├── workspace/                    # agent working directory (read-write)
├── tmp/                          # ephemeral scratch space (read-write)
└── var/
    └── lib/
        └── memory.db             # SQLite conversation store (read-write)
```

| Path                    | Access     | Source                 | Purpose                          |
|-------------------------|------------|------------------------|----------------------------------|
| `etc/agent.yaml`        | read-only  | executor-generated     | Agent config (name, model, tools)|
| `etc/context/`          | read-only  | `CONTEXT` instructions | System prompt context files      |
| `etc/context/AGENT.md`  | read-only  | auto-generated         | Agent metadata, bins, data       |
| `etc/data/`             | read-only  | `ADD` instructions     | Static data files                |
| `usr/local/bin/runtime` | read-only  | `RUNTIME` OCI image    | Runtime binary                   |
| `usr/bin/`              | read-only  | `BIN` OCI images       | Tool binaries                    |
| `workspace/`            | read-write | —                      | General-purpose working dir      |
| `tmp/`                  | read-write | —                      | Ephemeral scratch space          |
| `var/lib/`              | read-write | —                      | Persistent state (memory.db)     |

### Reserved Context: AGENT.md

`AGENT.md` is auto-generated and cannot be used as a `CONTEXT` name. It contains:

- Agent name and description (from `NAME` and `LABEL description`)
- Available binaries with descriptions and usage (from `BIN`)
- Available data files with descriptions (from `ADD`)
- Filesystem layout (read-write paths)

## OCI Artifact Structure

The built artifact follows the [OCI Image Manifest](https://github.com/opencontainers/image-spec/blob/main/manifest.md)
spec with a custom artifact type.

### Manifest

```
manifest (schemaVersion: 2)
├── mediaType:    application/vnd.oci.image.manifest.v1+json
├── artifactType: application/vnd.openotters.agent.v1
├── config blob
├── layers[]
└── annotations
```

| Field           | Value                                                                           |
|-----------------|---------------------------------------------------------------------------------|
| `schemaVersion` | `2`                                                                             |
| `mediaType`     | `application/vnd.oci.image.manifest.v1+json`                                    |
| `artifactType`  | `application/vnd.openotters.agent.v1`                                           |
| `annotations`   | Merged from `LABEL` instructions + `org.opencontainers.image.title` from `NAME` |

### Config Blob

The manifest's `config` descriptor contains the **full serialized Agentfile** as JSON. This is the complete,
lossless representation of the parsed Agentfile — including configs with their required flags and descriptions,
context content, binary references, labels, and args.

| Field  | Media Type                                        |
|--------|---------------------------------------------------|
| Config | `application/vnd.openotters.agent.config.v1+json` |

```json
{
  "syntax": "openotters/agentfile:1",
  "agent": {
    "from": "scratch",
    "runtime": "ghcr.io/openotters/runtime:latest",
    "model": "anthropic/claude-haiku-4-5-20251001",
    "name": "meteo",
    "contexts": [
      {
        "name": "SOUL",
        "description": "Agent personality and core instructions",
        "content": "You are a weather assistant..."
      },
      {
        "name": "IDENTITY",
        "content": "Name: Meteo Bot"
      }
    ],
    "configs": [
      {
        "key": "max-tokens",
        "value": "1024",
        "description": "Maximum output tokens per response"
      },
      {
        "key": "max-iterations",
        "value": "10",
        "description": "Maximum tool iterations per turn"
      }
    ],
    "bins": [
      {
        "name": "wget",
        "image": "ghcr.io/openotters/tools/wget:latest",
        "description": "Fetch URL content"
      },
      {
        "name": "jq",
        "image": "ghcr.io/openotters/tools/jq:latest",
        "description": "Extract fields from JSON"
      }
    ],
    "adds": [
      {
        "src": "cities.json",
        "dst": "/data/cities.json",
        "description": "Known cities with lat/lon coordinates"
      }
    ],
    "labels": {
      "description": "Weather assistant using Open-Meteo API"
    }
  }
}
```

This means `pull` simply deserializes the config blob — no reconstruction from layers needed. The context and
file layers exist for deploy-time extraction, but the config blob is the source of truth.

### Layers

Each `CONTEXT` and `ADD` instruction produces one layer in the manifest.

| Source    | Media Type                              | Title Annotation                            |
|-----------|-----------------------------------------|---------------------------------------------|
| `CONTEXT` | `application/vnd.openotters.context.v1` | `{name}.md` (e.g. `SOUL.md`)                |
| `ADD`     | `application/octet-stream`              | destination path (e.g. `/data/cities.json`) |

Layers are ordered: all context layers first, then all file layers. Each layer carries an
`org.opencontainers.image.title` annotation identifying the file.

### Example

For the meteo agent example, the artifact looks like:

```
manifest (artifactType: application/vnd.openotters.agent.v1)
├── config (application/vnd.openotters.agent.config.v1+json)
│   └── full serialized Agentfile JSON (source of truth)
├── layer: SOUL.md (application/vnd.openotters.context.v1)
├── layer: IDENTITY.md (application/vnd.openotters.context.v1)
├── layer: /data/cities.json (application/octet-stream)
└── annotations: {"description":"Weather assistant...", "org.opencontainers.image.title":"meteo"}
```

## Push & Pull

Agent artifacts are stored in any OCI-compliant registry (Docker Hub, GitHub Container Registry, AWS ECR, etc.).

### Push

Uploads a built artifact to a registry. The reference follows standard OCI conventions:

```
<registry>/<repository>:<tag>
```

```bash
# Build then push
agentfile build -f Agentfile -t ghcr.io/openotters/agents/meteo:1.0.0
agentfile push ghcr.io/openotters/agents/meteo:1.0.0
```

Authentication uses Docker credential helpers (`~/.docker/config.json`). Localhost registries automatically use
plain HTTP.

### Pull

Downloads an agent artifact from a registry. The config blob contains the full serialized Agentfile, so pull is a
simple deserialization — no reconstruction from layers needed.

```bash
agentfile pull ghcr.io/openotters/agents/meteo:1.0.0
```

Binary images referenced in the config are **not** pulled at this stage — they are resolved later at deploy time.

## Export & Import

For environments without direct registry access, agent artifacts can be serialized to a single portable JSON file.

### Export

Serializes a built artifact (manifest + all blobs) into a self-contained JSON file. Analogous to `docker save`.

```bash
agentfile build -f Agentfile
agentfile export -o meteo.json
```

The exported file contains the manifest descriptor and every blob (config + layers) as base64-encoded data.

### Import

Reconstructs an in-memory OCI store from an exported JSON file. The result can then be pushed to a registry.
Analogous to `docker load`.

```bash
agentfile import meteo.json
agentfile push ghcr.io/openotters/agents/meteo:1.0.0
```

Use case: build on a CI runner, export as a build artifact, import and push from a deploy environment — no registry
connectivity needed at build time.

## Out of Scope: Channels & Neighbors

The Agentfile intentionally describes a **single agent as an isolated, deployable unit** — the equivalent of a
Dockerfile for containers. Two concerns are deliberately left out of this spec:

### Channels

Channels define how external systems communicate with an agent (Telegram, WebSocket, REST, etc.). These are
**runtime bindings**, not build-time properties: the same agent artifact can be exposed over different channels
depending on the deployment environment. Channels are configured at deploy time by the orchestrator, not baked
into the artifact.

### Neighbors (Inter-Agent Communication)

Neighbors allow agents to talk to each other. This is an **orchestration concern** — it requires knowledge of
which agents exist, how they are networked, and how they discover each other. A single Agentfile has no way to
express this because it only knows about itself.

Neighbor support will be provided by a higher-level composition tool — analogous to how `docker-compose` sits
above `Dockerfile`:

```
Dockerfile    → docker-compose.yml
Agentfile     → agent-compose.yml (planned)
```

The composition layer will define the agent topology (which agents exist, how they connect) and inject neighbor
information into each agent at deploy time. One approach is a dynamically generated `NEIGHBORS.md` context file
that the runtime keeps up to date as agents join or leave, giving each agent awareness of its peers without
coupling that knowledge into the build artifact.

### Why This Separation Matters

- **Portability** — an agent artifact works in any environment without modification
- **Composability** — the same agent can participate in different topologies
- **Single responsibility** — Agentfile = build, composition layer = orchestration

## Design Principles

- **One file = one deployable unit**
- **OCI-native** — output is an OCI artifact, stored in any registry
- **Lazy resolution** — binary images are references, not embedded; resolved at deploy time
- **Single inheritance** — one parent via `FROM`, no diamond dependencies
- **Credentials are external** — MODEL names the model, secrets provide the keys
- **Familiar syntax** — Dockerfile-like instructions, minimal learning curve
