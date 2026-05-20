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
    * [ENV](#env)
    * [BIN](#bin)
      * [Binary OCI Image Structure](#binary-oci-image-structure)
        * [Annotations](#annotations)
        * [Binary Resolution](#binary-resolution)
        * [Design Rationale](#design-rationale)
        * [Examples](#examples)
    * [EXEC](#exec)
    * [ADD](#add)
    * [LABEL](#label)
    * [ARG](#arg)
    * [CAPABILITY](#capability)
  * [Complete Example](#complete-example)
  * [Runtime API Contract](#runtime-api-contract)
    * [Service Definition](#service-definition)
    * [Execution](#execution)
    * [Health and Readiness](#health-and-readiness)
    * [Model Validation](#model-validation)
    * [Compatibility](#compatibility)
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
| `MODEL`, `NAME`, `EXEC` | Overrides parent                                   |
| `CONTEXT`               | Same-name overrides parent, new names appended     |
| `CONFIG`                | Same-key overrides parent, new keys appended (cleared if `RUNTIME` is overridden) |
| `BIN`                   | Appended to parent's binary list                   |
| `ADD`                   | Appended to parent's files                         |
| `LABEL`                 | Merged (child wins on key conflicts)               |
| `ARG`                   | Merged (child wins on key conflicts)               |
| `ENV`                   | Same-key overrides parent, new keys appended       |
| `CAPABILITY`            | De-duplicated union with parent's list             |

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

Declares free-form configuration entries. Each `CONFIG` line attaches a key, an optional value, and an optional
description to the agent. The Agentfile spec doesn't enumerate which keys are allowed — runtimes consume whatever
they recognise and ignore the rest. Use whatever keys your runtime documents.

```agentfile
# String (default type, quotes optional for single words)
CONFIG memory-strategy=summarize "Memory compaction strategy"
CONFIG greeting="hello world" "Default greeting message"

# Integer
CONFIG max-tokens=1024 "Maximum output tokens per response"
CONFIG max-iterations=10 "Maximum tool iterations per turn"

# Float
CONFIG temperature=0.7 "Sampling temperature"

# Boolean
CONFIG verbose=true "Enable verbose logging"
CONFIG stream=false "Disable streaming by default"

# Required (no default, trailing !)
CONFIG api-base! "API base URL for the LLM provider"

# Optional with no default
CONFIG custom-header "Custom HTTP header for tool requests"
```

Format: `CONFIG <key[!]>[=<value>] [description]`

- Keys MUST be DNS-1123 names: lowercase alphanumeric and `-`, start and end with an alphanumeric character, at
  most 63 characters (same shape as a Kubernetes resource name). Kebab case is the convention; runtimes split on
  `-` when mapping to nested fields.
- Values are free-form strings on the wire. Runtimes pick their own typing rules — quoted values arrive as
  strings; unquoted values look like YAML primitives but the runtime gets the raw text and interprets it.
- Trailing `!` marks the key as required — deploy fails if no value is provided.
- Required keys cannot have a default value.

CONFIG entries are passed to the runtime through **two** channels at agent start:

1. **The agent's `agent.yaml` `configs:` block.** Materialised to disk at agent create time. Runtimes read this
   as the primary source of truth.
2. **Spawn-env variables.** Every key is also exported on the runtime process as `RUNTIME_<UPPER_SNAKE_CASE>`,
   with `-` rewritten to `_`. Tools spawned by the runtime see them too. Useful for subprocess wrappers that want
   tunables without re-parsing YAML.

For example, `CONFIG max-tokens=2048` lands on the runtime as:

```yaml
# agent.yaml
configs:
  max-tokens: "2048"
```

and:

```
RUNTIME_MAX_TOKENS=2048
```

Both copies always agree — the env is derived from the same map at spawn time.

### ENV

Declares an OS environment variable to be set on the spawned agent process.
Unlike `CONFIG` (a runtime-SDK knob the agent reads via the runtime API)
and `ARG` (build-time substitution), `ENV` values land directly on the
spawned process's environment — `Cmd.Env` for the system executor,
`container.Config.Env` for the docker executor.

```agentfile
ENV NODE_ENV=production "Application environment"
ENV LOG_LEVEL=debug
ENV GREETING="hello world" "Quoted value with spaces"
ENV STRIPE_PUBLISHABLE_KEY=pk_live_abc123 "Public Stripe key (no _API_KEY suffix)"
```

Format: `ENV <KEY>=<value> [description]`

- Keys must be uppercase POSIX-style names matching `^[A-Z_][A-Z0-9_]*$`.
- Quoted values support whitespace; unquoted values are single tokens.
- Reserved keys are **rejected at build time** to keep the locked-down
  agent env intact:
    - `PATH`, `HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`,
      `XDG_DATA_HOME`, `TMPDIR`, `LANG`, `OTTERS_AGENT_ROOT`
    - any key ending in `_API_KEY` or `_API_BASE` (use a provider
      `MODEL` declaration instead — provider creds travel through a
      dedicated channel).
- Duplicate keys: last declaration wins (parallels `CONFIG` overwrite
  semantics).
- Use `ENV` for OS-level integration (`NODE_ENV`, `LOG_LEVEL`,
  third-party SDK config). Use `CONFIG` when the value should be
  visible to the agent's LLM behaviour through the runtime SDK.

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

### EXEC

Specifies how the runtime binary is invoked. The value is a list of arguments passed to the binary at
`usr/local/bin/runtime`. Supports `${VAR}` substitution from `ARG` values.

```agentfile
EXEC ["serve"]
EXEC ["serve", "--max-tokens", "1024"]
EXEC ["serve", "--max-tokens", "${MAX_TOKENS}"]
```

Format: `EXEC ["<arg>", ...]` (JSON array)

- Arguments are passed to the runtime binary at `usr/local/bin/runtime`
- `${VAR}` substitution from `ARG` values is applied inside quoted strings
- If omitted, defaults to `["serve"]`
- The executor appends additional flags (`--root`, `--model`, `--addr`, `--api-base`, `--api-key`) after the exec args

In inheritance, `EXEC` overrides the parent's value completely.

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

### CAPABILITY

Declares which runtime-provided LLM-facing tools the agent's model is allowed to call. Each line names one
capability; repeating the directive grants more.

```agentfile
CAPABILITY note-save
CAPABILITY note-list
CAPABILITY note-show
CAPABILITY job-submit
CAPABILITY job-wait
```

Format: `CAPABILITY <name>`

- Names MUST be DNS-1123 (same rule as `CONFIG` keys: lowercase alphanumeric and `-`, start/end alphanumeric,
  ≤63 chars).
- Free-form on the spec side. The Agentfile lists names; the daemon resolves them against the runtime's
  capability catalogue at agent-create time and produces the full per-capability entry (description, schema) on
  disk in `agent.yaml`.
- **No default.** An Agentfile with zero `CAPABILITY` directives grants the agent **no runtime tools at all**
  (the strict default). Tool images mounted via `BIN` are separate — they always work; `CAPABILITY` gates only
  the runtime's *own* tool surface (notes, async jobs, agent-to-agent calls, etc.).
- Names unknown to the daemon's catalogue at create time are rejected. Listing the same name twice is fine; the
  parser de-duplicates.
- Operators can override at run time with `otters run --cap <name>` to add caps the Agentfile didn't grant.

Capabilities are an **allowlist**, not a description. The runtime carries every capability it implements; the
Agentfile picks the subset this agent is allowed to expose to its model. The daemon's JWT carries the resolved
list and the runtime enforces it before dispatch.

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

## Runtime API Contract

Any binary referenced by the `RUNTIME` instruction **must** implement the gRPC API defined in
[`agent/api/v1/agent.proto`](agent/api/v1/agent.proto). This is the contract between the agent executor and the
runtime process. A runtime that does not implement this API is not compatible with the Agentfile specification.

### Service Definition

```protobuf
service AgentRuntime {
  // Chat: synchronous prompt/response
  rpc Chat(ChatRequest) returns (ChatResponse);

  // ChatStream: streaming prompt with step-by-step events
  rpc ChatStream(ChatStreamRequest) returns (stream ChatStreamEvent);

  // Session management
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);
  rpc DeleteSession(DeleteSessionRequest) returns (DeleteSessionResponse);

  // Health probes
  rpc Health(HealthRequest) returns (HealthResponse);
  rpc Ready(ReadyRequest) returns (ReadyResponse);
}
```

### Execution

The runtime binary is invoked with the arguments specified by the `EXEC` instruction (see below). The executor
appends additional flags (`--root`, `--model`, `--addr`, `--api-base`, `--api-key`) to the exec args.

The runtime **must**:

1. Read agent configuration from `<root>/etc/agent.yaml`
2. Load context files from `<root>/etc/context/`
3. Load tool binaries from `<root>/usr/bin/`
4. Start a gRPC server on the address specified by `--addr`
5. Block until the process receives `SIGINT` or `SIGTERM`, then shut down gracefully
6. Exit with code 0 on clean shutdown, non-zero on error

### Health and Readiness

The `Health` RPC returns the agent's name and model. The `Ready` RPC returns whether the agent is ready to accept
chat requests. Executors **should** poll `Ready` before routing traffic to a newly started agent.

### Model Validation

The runtime **should** validate that the specified model is available before starting the gRPC server. If the model
is not found (e.g. not pulled in Ollama, invalid API key), the runtime **must** exit immediately with a non-zero
exit code and a human-readable error message on stderr.

### Compatibility

The proto definition is versioned at `openotters.agent.v1`. Runtime implementations **must** implement all RPCs in
the service definition. Unimplemented RPCs **must** return `UNIMPLEMENTED` status, not silently succeed.

The canonical proto file is located at `agent/api/v1/agent.proto` in the agentfile module. Runtime implementations
should generate their gRPC server code from this file to ensure compatibility.

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
