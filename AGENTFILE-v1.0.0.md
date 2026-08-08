# Agentfile Specification

This document is the **Agentspec** — the language specification for Agentfiles. It defines what an Agentfile may
contain and how each instruction is interpreted. (In this repo's terminology the *Agentspec* is the grammar, an
*Agentfile* is a source document written against it, an *Image* is the built OCI artifact, and an *Agent* is a
running instance of an Image — see `AGENT.md`.)

An Agentfile is a declarative build specification for OpenOtters agents. It describes everything an agent needs —
runtime, model, personality, binaries, data — in a single file that can be built into an Image.

The specification is organised in three parts: **Part I** defines the Agentfile language (for authors and
parser/builder implementers), **Part II** the Image artifact (for builder and registry tooling), and **Part III**
the runtime environment (for executor and runtime implementers). It deliberately does not define the wire
protocol a running agent speaks — that is an orchestrator concern.

<!-- TOC -->
* [Agentfile Specification](#agentfile-specification)
  * [Document Conventions](#document-conventions)
    * [Requirement Keywords and Actors](#requirement-keywords-and-actors)
    * [Format Notation](#format-notation)
    * [Command Notation](#command-notation)
    * [Path Conventions](#path-conventions)
  * [Conformance](#conformance)
  * [Part I — The Agentfile Language](#part-i--the-agentfile-language)
    * [Syntax Directive](#syntax-directive)
    * [Lexical Conventions](#lexical-conventions)
    * [Validation Stages](#validation-stages)
    * [Merge Semantics](#merge-semantics)
    * [Instruction Reference](#instruction-reference)
      * [FROM](#from)
        * [Inheritance](#inheritance)
      * [NAME](#name)
      * [RUNTIME](#runtime)
      * [MODEL](#model)
      * [CONTEXT](#context)
      * [CONFIG](#config)
      * [ENV](#env)
      * [BIN](#bin)
        * [Binary OCI Image Structure](#binary-oci-image-structure)
      * [EXEC](#exec)
      * [ADD](#add)
      * [LABEL](#label)
      * [ARG](#arg)
      * [CAPABILITY](#capability)
    * [Reserved Names](#reserved-names)
    * [Complete Example](#complete-example)
  * [Part II — The Image](#part-ii--the-image)
    * [OCI Artifact Structure](#oci-artifact-structure)
      * [Manifest](#manifest)
      * [Config Blob](#config-blob)
      * [Layers](#layers)
      * [Artifact Example](#artifact-example)
    * [Distribution](#distribution)
  * [Part III — The Runtime Environment](#part-iii--the-runtime-environment)
    * [Execution](#execution)
    * [Provider Credentials](#provider-credentials)
    * [Daemon Callback](#daemon-callback)
    * [agent.yaml](#agentyaml)
    * [Agent Filesystem Layout](#agent-filesystem-layout)
      * [Reserved Context: AGENT.md](#reserved-context-agentmd)
    * [Readiness](#readiness)
    * [Model Validation](#model-validation)
  * [Security Considerations](#security-considerations)
  * [Out of Scope: Channels & Neighbors](#out-of-scope-channels--neighbors)
  * [Design Principles](#design-principles)
  * [Changelog](#changelog)
<!-- TOC -->

## Document Conventions

### Requirement Keywords and Actors

The key words **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT**, and **MAY** are to be interpreted as described
in [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119) and [RFC 8174](https://www.rfc-editor.org/rfc/rfc8174)
when they appear in bold capitals.

Every requirement binds one of five conformance actors:

- **author** — the person (or tool) writing an Agentfile. A violated author requirement makes the file invalid.
- **parser** — the component that turns Agentfile source into the parsed representation.
- **builder** — the component that produces an Image from a parsed Agentfile.
- **executor** — the component that materialises an Image on disk and spawns the runtime process.
- **runtime** — the binary referenced by `RUNTIME`, booting into the environment defined by [Part III](#part-iii--the-runtime-environment).

When a rule is violated, the [Validation Stages](#validation-stages) section says which actor rejects it, and when.

### Format Notation

Each instruction's `Format:` line uses this notation:

- `<x>` — a required element; `[x]` — an optional element; `a | b` — alternatives; `x …` — one or more repetitions.
- Element positions name the token class they accept: `IDENT`, `STRING`, `FILEREF`, or `HEREDOC`
  (defined in [Lexical Conventions](#lexical-conventions)). A position annotated `IDENT` accepts **only** an
  unquoted token — quoting is not permitted there. A position annotated `IDENT | STRING` accepts either.
- Literal characters (`=`, `!`, `[`, `]`) stand for themselves.
- Positions written `<oci-ref>` follow the
  [OCI distribution specification](https://github.com/opencontainers/distribution-spec) reference grammar —
  `<registry>/<repository>[:<tag>][@<digest>]` — and digests use the algorithms of the
  [OCI descriptor specification](https://github.com/opencontainers/image-spec/blob/main/descriptor.md).

### Command Notation

Command lines written as `agentfile <verb>` in this document denote the **primitive operations** this spec
defines — parse, validate, build, push, pull, export, import — as implemented by the builder module
(`github.com/openotters/agentfile`, a Go library with example programs under `examples/`). They describe *what
the layer does*, independent of how a given front-end spells it; there is no installed `agentfile` binary.
End-user tooling wraps these primitives under its own command names, which are outside this specification.

### Path Conventions

Two kinds of filesystem paths appear in this document; they are never interchangeable:

- **Agent-root-relative paths** (e.g. `etc/context/`, `usr/bin/wget`) describe the materialised agent tree on the
  host or in the container — see [Agent Filesystem Layout](#agent-filesystem-layout).
- **In-image absolute paths** (e.g. `/bin`, `/USAGE.md`) describe locations **inside an OCI image's own
  filesystem** — the `io.openotters.bin.path` annotation and a bin's `USAGE.md` location are of this kind. The
  executor reads them when extracting a binary and then places the result under the agent root.

## Conformance

| Actor    | Conforms by                                                                                       |
|----------|---------------------------------------------------------------------------------------------------|
| author   | producing files valid under [Part I](#part-i--the-agentfile-language)                             |
| parser   | implementing Part I: the lexical rules, instruction grammar, and the parse + validate stages       |
| builder  | implementing Part I's resolve + build stages and producing artifacts per [Part II](#part-ii--the-image) |
| executor | consuming Part II artifacts and implementing [Part III](#part-iii--the-runtime-environment)'s materialisation, spawn environment, and deploy stage |
| runtime  | booting into Part III: consuming `agent.yaml`, honouring the filesystem obligations, and the start stage |

A component claiming conformance for a role MUST satisfy every **MUST** bound to that actor in the corresponding
part, plus the cross-cutting rules in [Security Considerations](#security-considerations).

## Part I — The Agentfile Language

Everything in this part binds authors, parsers, and (for the resolve stage) builders.

### Syntax Directive

Optional. When present, it MUST be the very first line, before any instruction.

```agentfile
# syntax=openotters/agentfile:1
```

If omitted, `openotters/agentfile:1` is assumed. The parser MUST reject a syntax value it does not support, and
MUST reject a `# syntax=` line that appears after the first instruction.

The trailing `:1` is the **grammar major version**, versioned independently of this document (the `1.0.0` in the
document title is the semver of the specification prose — see [Changelog](#changelog)). Grammar evolution is
**additive within a major**:

- New instructions and new optional fields MAY be added within `:1`. An Agentfile that uses them requires a parser
  released after they were added — unknown keywords remain parse errors (see
  [Validation Stages](#validation-stages)) — but the version selector stays `:1`.
- A **breaking** change to an existing instruction (changed meaning, removed form, changed default) requires a new
  grammar major, `:2`.

### Lexical Conventions

An Agentfile is parsed line by line. These rules apply uniformly across every instruction.

**Encoding.** Agentfile source MUST be UTF-8 without a byte-order mark. Parsers MUST accept both LF and CRLF
line endings.

**Lines and comments.** One instruction per line. A line whose first non-whitespace character is `#` is a comment
and is ignored — comments are full-line only. A `#` (or any other unexpected token) after a complete instruction
is a **parse error**, not a comment and not part of the value. Blank lines are ignored. The sole special case is
`# syntax=…` on the very first line. Instruction keywords are matched case-insensitively (`from scratch` is
accepted); the canonical spelling is uppercase.

**Token classes.** Every instruction is a keyword followed by tokens drawn from these classes:

- `IDENT` — an unquoted token: one or more characters, terminated by whitespace, that may **not** contain
  whitespace, `"`, `=`, or `!`. Used for names, OCI references, and unquoted values. If a value needs any of the
  excluded characters, use a `STRING`.
- `STRING` — a double-quoted string (`"…"`) with backslash escapes: `\"` for a literal quote, `\\` for a
  backslash, plus `\n`, `\t`, `\r` and the other C-style escapes. An unrecognised escape sequence is a parse
  error. Descriptions are always `STRING`s.
- `FILEREF` — `file://` followed by a path, terminated by whitespace. The path is relative to the Agentfile
  directory. The `file://` prefix is recognised globally by the lexer, in every instruction position — do not
  start an `IDENT` with it.
- `HEREDOC` — multi-line inline content. The opening `<<MARKER` (marker: an `IDENT`) MUST be the **last token on
  the line**. Body lines are taken **verbatim** — no escape processing, no quote handling, and (unlike the
  instruction line) **no `${VAR}` substitution**. The body ends at the first subsequent line whose trimmed text
  equals `MARKER` exactly. Trailing newlines are stripped from the body (a final line containing only spaces or
  tabs is preserved). An unterminated heredoc is a parse error.

**Path-safe names.** Several positions name files that the executor materialises under the agent root: `NAME`,
`CONTEXT` names (→ `etc/context/{name}.md`), `BIN` names (→ `usr/bin/{name}`), and `ADD` names
(→ `etc/data/{name}`). These MUST match `^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$` (validate) — the shape forbids path
separators and dot-prefixed names, so no declared name can escape its directory (see
[Security Considerations](#security-considerations)).

**Line grammar** (informative EBNF; per-instruction shapes are given by each `Format:` line):

```ebnf
agentfile        = [ syntax-line ] , { blank-line | comment-line | instruction } ;
instruction      = keyword , { token } , [ heredoc-open ] , newline , [ heredoc-body ] ;
token            = IDENT | STRING | FILEREF | "=" | "!" | "[" | "]" ;
heredoc-open     = "<<" , IDENT ;
```

**`${VAR}` substitution** from `ARG` values is applied to the instruction line *before* tokenisation, so it works
in quoted and unquoted positions alike — but **not** inside heredoc bodies. Only `ARG`s declared **earlier in the
file** are in scope (see [ARG](#arg) for the inheritance interaction). Undefined variables are left as the
literal text `${VAR}`. Because substitution precedes tokenisation, a substituted value containing whitespace,
`"`, `=`, or `!` changes how the line tokenises — quote the position if the value may contain such characters.

### Validation Stages

Every rule in this specification is enforced at exactly one of six stages. An Agentfile can be valid at one stage
and still fail a later one — deliberately: a base Agentfile meant only for inheritance parses and builds even
though it cannot run.

| Stage | Actor | When | What is checked |
|---|---|---|---|
| **parse** | parser | reading source | tokenisation, per-instruction shape, unknown keywords, `FROM` first, heredoc termination |
| **validate** | parser | after parse, same invocation | name rules, reserved names, per-file semantic constraints |
| **resolve** | builder | `FROM <ref>` present | parent Image fetched, [Merge Semantics](#merge-semantics) applied, cycle detection |
| **build** | builder | producing the Image | `ADD` sources readable, layer assembly, canonical serialization |
| **deploy** | executor | materialising an Agent | required values present, capability names resolvable, `RUNTIME`/`BIN` images resolvable for the target platform |
| **start** | runtime | process startup | model available, runtime bound on `--addr` and ready |

Rule-to-stage mapping:

| Rule | Stage |
|---|---|
| Unknown instruction keyword (closed per parser release; see [Syntax Directive](#syntax-directive)) | parse |
| Trailing tokens after a complete instruction | parse |
| `FROM` present, first, and appearing exactly once | parse |
| Unterminated heredoc; `<<MARKER` not last token | parse |
| Unsupported `# syntax=` value; directive after first instruction | parse |
| `NAME`, `CONTEXT`/`BIN`/`ADD` names are path-safe ([Lexical Conventions](#lexical-conventions)) | validate |
| `CONFIG` keys / `CAPABILITY` names are DNS-1123 labels | validate |
| Required `CONFIG` key declared with a value | validate |
| `ENV` key shape, reserved keys, reserved suffixes ([Reserved Names](#reserved-names)) | validate |
| `CONTEXT` name reserved (`AGENT`, `WORKSPACE`, `MOUNTS`) | validate |
| `CONTEXT` with both `file://` and heredoc | validate |
| Parent ref resolvable; inheritance chain acyclic | resolve |
| `ADD` source file exists and is readable | build |
| Required `CONFIG` key still unset | deploy |
| `CAPABILITY` name unknown to the runtime's catalogue | deploy |
| `RUNTIME`/`BIN` image resolvable, platform match (see [Security Considerations](#security-considerations)) | deploy |
| `RUNTIME`, `MODEL`, `NAME` present (possibly inherited) | deploy / start |
| Model available to the runtime | start |

### Merge Semantics

Four merge behaviors cover every instruction, both for **duplicates within one file** and for
**`FROM` inheritance** (child merged over parent). Each instruction's rules use exactly these terms:

- **replace** — the later (or child's) value wins entirely; the earlier value is discarded.
- **append** — entries accumulate in order: parent's first, then child's (or earlier lines first, then later).
  Duplicates are retained at the spec level.
- **keyed-merge** — entries are keyed; the last writer for a key wins, new keys are added.
- **set-union** — entries form a set; duplicates collapse, order of first appearance is kept.

Where an instruction is *append* at the spec level but consumers need one value per key (`CONFIG`, `ENV`), the
accumulated list is **flattened by keyed-merge (last wins) at materialisation** — so "later wins" is the
observable behavior even though the parsed representation keeps every entry. The required-key check for `CONFIG`
is evaluated against the flattened view, so a later entry that supplies a value satisfies an earlier
required-and-unset declaration of the same key.

### Instruction Reference

The requiredness of each instruction, and the stage that enforces it:

| Instruction  | Required                              | Default        | Duplicate / inheritance behavior                      |
|--------------|---------------------------------------|----------------|-------------------------------------------------------|
| `FROM`       | yes — parse; exactly once, first      | —              | n/a (single occurrence)                               |
| `NAME`       | yes — deploy (may be inherited)       | —              | replace                                               |
| `RUNTIME`    | yes — deploy (may be inherited)       | —              | replace; clears accumulated `CONFIG`s (see below)     |
| `MODEL`      | yes — start (may be inherited)        | —              | replace                                               |
| `EXEC`       | no                                    | `["serve"]`    | replace                                               |
| `CONTEXT`    | no                                    | —              | keyed-merge by name                                   |
| `CONFIG`     | no                                    | —              | append; flattened keyed-merge at materialisation      |
| `ENV`        | no                                    | —              | append; flattened keyed-merge at spawn                |
| `BIN`        | no                                    | —              | append; keyed-merge by name at materialisation        |
| `ADD`        | no                                    | —              | append; keyed-merge by `<name>` at materialisation    |
| `LABEL`      | no                                    | —              | keyed-merge                                           |
| `ARG`        | no                                    | —              | keyed-merge                                           |
| `CAPABILITY` | no                                    | none granted   | set-union                                             |

`RUNTIME`, `MODEL`, and `NAME` are required for an Agent that can actually be **run**, but the parser does not
enforce their presence — they may be supplied by a parent via `FROM`, so a base Agentfile meant only to be
inherited from can legally omit them. The `EXEC` default is applied by the **executor** at spawn, not by the
parser: a parsed Agentfile that omits `EXEC` carries no exec value.

#### FROM

**Required. MUST be the first instruction and MUST appear exactly once (parse).**

Declares the base for the agent. Either `FROM scratch` (empty agent) or `FROM <ref>` (inherit from a parent
Image).

Format: `FROM <ref:IDENT>` — `scratch` or an `<oci-ref>`.

```agentfile
FROM scratch
FROM ghcr.io/openotters/agents/base-researcher:v1.0
```

Inheritance is **single**: exactly one parent per Agentfile, and the ancestry MUST be acyclic — a cycle in the
parent chain is a resolve error. Inheritance operates on the parent Image's [config blob](#config-blob), whose
schema is open, so a parent built by a newer grammar than the child's parser does not itself break resolution —
unknown blob fields are ignored.

##### Inheritance

When using `FROM <ref>`, the child inherits the parent's full definition and can override or extend it. The
behavior per instruction uses the terms defined in [Merge Semantics](#merge-semantics):

| Instruction             | Behavior                                                                     |
|-------------------------|------------------------------------------------------------------------------|
| `RUNTIME`               | replace; if the child sets it, **all** parent `CONFIG`s are dropped          |
| `MODEL`, `NAME`, `EXEC` | replace                                                                      |
| `CONTEXT`               | keyed-merge by name                                                          |
| `CONFIG`                | append (dropped entirely if the child sets `RUNTIME`)                        |
| `BIN`                   | append                                                                       |
| `ADD`                   | append                                                                       |
| `LABEL`                 | keyed-merge                                                                  |
| `ARG`                   | keyed-merge                                                                  |
| `ENV`                   | append; flattened keyed-merge at spawn                                       |
| `CAPABILITY`            | set-union                                                                    |

#### NAME

Sets the agent identity. Becomes the Image's `org.opencontainers.image.title` annotation (see
[Manifest](#manifest)).

Format: `NAME <name:IDENT>` — a [path-safe name](#lexical-conventions).

```agentfile
NAME meteo
```

#### RUNTIME

Specifies the OCI image containing the agent runtime binary. The image MUST follow the
[Binary OCI Image Structure](#binary-oci-image-structure) — the same `io.openotters.bin.*` annotation contract
used by `BIN` instructions.

Format: `RUNTIME <oci-ref:IDENT>`

Setting `RUNTIME` replaces any previous `RUNTIME` instruction and **clears all `CONFIG` entries accumulated so
far**, since configuration keys are runtime-specific. This clearing is **order-sensitive**:

- **Within one Agentfile**, only `CONFIG` lines that appear *before* the `RUNTIME` line are dropped; `CONFIG`
  lines *after* it survive. Declare `RUNTIME` first, then its `CONFIG`s:

  ```agentfile
  RUNTIME ghcr.io/openotters/runtime:latest
  CONFIG max-tokens=1024
  ```

  Here `max-tokens` is kept because it is declared after `RUNTIME`; the same line placed before `RUNTIME` would
  be dropped.
- **Across `FROM` inheritance**, a child that declares *any* `RUNTIME` drops **all** of the parent's `CONFIG`s
  (regardless of order) and keeps only its own; a child that does not set `RUNTIME` appends its `CONFIG`s to the
  parent's.

#### MODEL

Specifies the LLM model. The segment before the first `/` is the **provider**; the remainder is the
provider-scoped model name (which may itself contain `/`). Credentials are resolved externally and delivered
through the [provider credential channel](#provider-credentials) — the Agentfile never contains API keys.

Format: `MODEL <provider/model:IDENT>`

```agentfile
MODEL anthropic/claude-haiku-4-5-20251001
MODEL openai/gpt-4o
```

#### CONTEXT

Defines a named context file that shapes the agent's behavior. Each context has a name, an optional description,
and content provided inline (heredoc) **or** from a file reference — declaring both on one line is a validate
error.

Format: `CONTEXT <name:IDENT> [<description:STRING>] ( <FILEREF> | <HEREDOC> )`

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

- `name` — a [path-safe name](#lexical-conventions), used as filename `{name}.md`; the names in
  [Reserved Names](#reserved-names) are rejected at validate.
- Duplicates: keyed-merge by name — the last definition wins, within a file and across inheritance.

Well-known context names:

- `SOUL` — personality, tone, core instructions
- `IDENTITY` — name, role, self-description

#### CONFIG

Declares free-form configuration entries. Each `CONFIG` line attaches a key, an optional value, and an optional
description to the agent. The Agentspec doesn't enumerate which keys are allowed — runtimes consume whatever they
recognise and ignore the rest. Use whatever keys your runtime documents.

Format: `CONFIG <key:IDENT>[!] [= <value:IDENT | STRING>] [<description:STRING>]`

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
CONFIG webhook-url! "Endpoint the agent notifies on completion"

# Optional with no default
CONFIG custom-header "Custom HTTP header for tool requests"
```

- Keys MUST be DNS-1123 **labels**: lowercase alphanumeric and `-`, starting and ending with an alphanumeric
  character, at most 63 characters. Kebab case is the convention; runtimes split on `-` when mapping to nested
  fields. Enforced at validate.
- Values are typed at parse: a **quoted** value is always a string; an **unquoted** value is interpreted as a
  YAML-style primitive (`true`/`false` → boolean, integer, float, else string) and serializes as that native
  JSON type in the [config blob](#config-blob). The `configs:` block of [`agent.yaml`](#agentyaml) presents the
  flattened view with every value stringified — runtimes reading that channel apply their own typing rules.
- **Unset vs empty.** `CONFIG key` (no `=`) leaves the value *unset*. `CONFIG key=""` sets it to the empty
  string. The required-key check tests set-ness, not emptiness.
- Trailing `!` marks the key as **required**: the parser accepts a bare required key (its value is meant to
  arrive by deploy), but materialising an Agent fails while a required key is still unset in the flattened view
  (deploy stage — fail-closed, never an empty/garbage value). A required key declared *with* a value is a
  validate error. Deploy tooling MAY supply or override `CONFIG` values (e.g. a repeatable
  `--config <key>=<value>` flag); a supplied value satisfies the requirement and replaces any declared value.

CONFIG entries are passed to the runtime through **two** channels at agent start:

1. **The `configs:` block of [`agent.yaml`](#agentyaml).** Materialised to disk at deploy. Runtimes read this as
   the primary source of truth. The block is the flattened keyed-merge view — one value per key.
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

Because the spawn-env copy is namespaced under `RUNTIME_`, it never collides with the locked-down base env or
provider credentials. The one way to collide is a deliberate `ENV RUNTIME_<NAME>` that targets the same resulting
variable; in that case **`ENV` wins** — see [ENV](#env).

#### ENV

Declares an OS environment variable to be set on the spawned agent process. Unlike `CONFIG` (a runtime-SDK knob
the agent reads via the runtime API) and `ARG` (build-time substitution), `ENV` values land directly on the
spawned process's environment.

Format: `ENV <KEY:IDENT> = <value:IDENT | STRING> [<description:STRING>]`

```agentfile
ENV NODE_ENV=production "Application environment"
ENV LOG_LEVEL=debug
ENV GREETING="hello world" "Quoted value with spaces"
ENV STRIPE_PUBLISHABLE_KEY=pk_live_abc123 "Public Stripe key (no _API_KEY suffix)"
```

- Keys MUST be uppercase POSIX-style names matching `^[A-Z_][A-Z0-9_]*$` (validate).
- Reserved keys and suffixes are rejected at validate — see [Reserved Names](#reserved-names). The executor
  filters the same set at spawn, so a bad key fails loudly at build instead of being silently dropped later.
- Duplicates: append, flattened keyed-merge at spawn — the last declaration wins, within a file and across
  inheritance (same shape as `CONFIG`).
- Precedence over `CONFIG`: `ENV` is applied to the spawn env *after* the `CONFIG` `RUNTIME_*` export, so an
  `ENV` whose key matches a `CONFIG`-derived variable (e.g. `ENV RUNTIME_MAX_TOKENS=…` vs
  `CONFIG max-tokens=…`) **overrides** it.
- `ENV` *values* are not written into [`agent.yaml`](#agentyaml) — only the key and description are recorded on
  disk; values travel via the spawn environment.
- Use `ENV` for OS-level integration (`NODE_ENV`, `LOG_LEVEL`, third-party SDK config). Use `CONFIG` when the
  value should be visible to the agent's LLM behaviour through the runtime SDK.

#### BIN

Declares a binary available to the agent. A binary has a name and an OCI image reference. Description and usage
guidelines are optional. Binary images are resolved at deploy, not at build.

Format: `BIN <name:IDENT> <oci-ref:IDENT> [<description:STRING>] [<HEREDOC>]`

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

- `name` — a [path-safe name](#lexical-conventions); the binary identifier presented to the LLM
- `oci-ref` — OCI image reference (pulled at deploy)
- `description` — optional one-line quoted string
- usage heredoc — optional multi-line guidelines
- Duplicates: append; a repeated name resolves by keyed-merge (last wins) at materialisation.

##### Binary OCI Image Structure

A bin is a **regular OCI image** — any image that carries the `io.openotters.bin.*` annotations. There is no
special base image requirement: the image can be built `FROM scratch`, `FROM alpine`, or any other base. The
annotations tell the executor where to find the binary and its metadata inside the image filesystem.

It is recommended to set an `ENTRYPOINT` in the Dockerfile so the image remains usable as a standalone container
(e.g. `docker run ghcr.io/openotters/tools/jq:latest`). However, the executor **ignores** the entrypoint — binary
resolution relies exclusively on the `io.openotters.bin.*` annotations. This removes ambiguity: an image may have
multiple executables, shell wrappers, or symlinks, but the annotations define exactly which binary the agent
uses.

The image manifest MUST carry annotations that describe the bin:

| Annotation                      | Required | Type   | Default     | Description                                  |
|---------------------------------|----------|--------|-------------|----------------------------------------------|
| `io.openotters.bin.name`        | yes      | string | —           | Binary name (e.g. `wget`, `jq`)              |
| `io.openotters.bin.path`        | no       | path   | `/`         | Directory containing the binary in the image |
| `io.openotters.bin.description` | no       | string | —           | One-line description for the LLM             |
| `io.openotters.bin.usage`       | no       | path   | `/USAGE.md` | Path to a USAGE.md file inside the image     |

The executor resolves the binary as `{path}/{name}` (both in-image absolute paths — see
[Path Conventions](#path-conventions)), extracts it from the image filesystem, and places it at
`usr/bin/{name}` in the agent tree.

- `io.openotters.bin.description` is a **string value** directly in the annotation.
- `io.openotters.bin.usage` points to a **file inside the image** — usage guidelines can be rich, multiline
  markdown that the runtime injects directly into the agent's context.
- When the Agentfile `BIN` instruction provides a description or usage, the **Agentfile wins** (explicit
  override over embedded default).
- Bin images MAY use OCI image indexes (manifest lists) for multi-platform support; the executor resolves the
  platform manifest matching the host at deploy (see
  [Security Considerations](#security-considerations) for the no-match case).

The `io.openotters.bin.*` annotations are a **public convention** — any OCI image can adopt them to declare that
it contains an executable binary with associated metadata. This makes bin images self-describing (a registry can
be browsed for available binaries without an Agentfile) and lets tooling outside the Agentfile ecosystem discover
and consume bin images using the same contract. `FROM scratch` images with a static binary are recommended —
single-digit MB, fast pulls — but not required.

Example (binary in `/bin`, usage doc in `/doc`):

```
image
  annotations:
    io.openotters.bin.name: jq
    io.openotters.bin.path: /bin
    io.openotters.bin.usage: /doc/USAGE.md
  filesystem:
    /bin/jq
    /doc/USAGE.md
```

With `path` and `usage` defaulted, the binary sits at `/jq` and the usage doc at `/USAGE.md`.

#### EXEC

Specifies how the runtime binary is invoked. The value is a list of arguments passed to the binary at
`usr/local/bin/runtime`.

Format: `EXEC [ <STRING> … ]` — a JSON-style array whose elements MUST be quoted strings (`EXEC [serve]` is a
parse error).

```agentfile
EXEC ["serve"]
EXEC ["serve", "--max-tokens", "1024"]
EXEC ["serve", "--max-tokens", "${MAX_TOKENS}"]
```

- `${VAR}` substitution from `ARG` values is applied inside the quoted strings.
- If omitted, the **executor** applies the default `["serve"]` at spawn (the parsed Agentfile carries no value).
- The executor appends its own flags after the exec args — see [Execution](#execution).
- Whatever `EXEC` invokes MUST still satisfy [Part III](#part-iii--the-runtime-environment) — a runtime that binds
  `--addr` implementing the service definition. `EXEC` selects *how* the runtime starts; it is not a
  general-purpose entrypoint.
- Duplicates and inheritance: replace.

#### ADD

Adds a local file into the Image at build time. Added files become data files in `etc/data/`; an optional
description is included in the auto-generated `AGENT.md` so the agent knows what each file contains.

Format: `ADD <src:IDENT> [<name:IDENT>] [<description:STRING>]`

```agentfile
ADD cities.json "Known cities with lat/lon coordinates"
ADD prompts/system.txt system-prompt.txt "System prompt template"
ADD config.yaml
```

- `src` — local file path, relative to the Agentfile directory (readable at build, or the build fails)
- `name` — a [path-safe name](#lexical-conventions): the flat filename the file materialises as,
  `etc/data/<name>`; defaults to `basename(src)`. There is no directory tree under `etc/data/`.
- `description` — optional quoted string (presented to the agent via `AGENT.md`). Since `name` is an `IDENT` and
  `description` a `STRING`, `ADD cities.json "desc"` is unambiguous.
- Duplicates: append; colliding `name`s resolve by keyed-merge (last wins) at materialisation.
- `name` becomes the layer's `org.opencontainers.image.title` annotation in the artifact (see [Layers](#layers)).

The runtime process's working directory is `workspace/`, not `etc/data/` — agents reference added files at
`etc/data/<name>` under the agent root (see [Agent Filesystem Layout](#agent-filesystem-layout)).

#### LABEL

OCI annotations on the output Image.

Format: `LABEL <key:IDENT> = <value:IDENT | STRING>`

```agentfile
LABEL description="Weather assistant using Open-Meteo API"
LABEL maintainer="romain@openotters.io"
LABEL org.opencontainers.image.version="1.0.0"
```

Duplicates and inheritance: keyed-merge. Note that `NAME` takes precedence over a
`LABEL org.opencontainers.image.title` (see [Manifest](#manifest)).

#### ARG

Build-time variables with optional defaults. Substituted as `${VAR}` in any subsequent instruction value (see
[Lexical Conventions](#lexical-conventions) for scoping and heredoc exclusion).

Format: `ARG <key:IDENT> [= <value:IDENT | STRING>]`

```agentfile
ARG MODEL=anthropic/claude-haiku-4-5-20251001
ARG MAX_TOKENS=1024

MODEL ${MODEL}
CONFIG max-tokens=${MAX_TOKENS}
```

- Duplicates and inheritance: keyed-merge.
- An `ARG` declared without a value participates in substitution only once a value is supplied; the build tooling
  MAY provide values for declared `ARG`s at build time (analogous to `--build-arg`), which replace declared
  defaults.
- **Substitution is per-file, at parse — before inheritance merge.** A parent's `ARG` values never substitute
  into a child's lines; each Agentfile is expanded against only its own `ARG`s. The keyed-merged `args` map in
  the [config blob](#config-blob) is a record of declarations, not an active mechanism.

#### CAPABILITY

Declares which runtime-provided LLM-facing tools the agent's model is allowed to call. A single directive can
list one capability or several; repeating the directive grants more.

Format: `CAPABILITY <name:IDENT> …`

```agentfile
# One name per line — the recommended shape when granting a single cap.
CAPABILITY note-save

# Multiple names on one line — cluster related caps together.
CAPABILITY note-list note-show
CAPABILITY job-submit job-wait job-list
```

- Names MUST be DNS-1123 labels (same rule as `CONFIG` keys) — validate.
- Free-form on the spec side: the Agentfile lists names; at deploy they are resolved against the **capability
  catalogue the runtime advertises**, producing the full per-capability entry (description, schema) in
  [`agent.yaml`](#agentyaml). Names unknown to the catalogue are rejected at deploy.
- **No default.** An Agentfile with zero `CAPABILITY` directives grants the agent **no runtime tools at all**
  (the strict default). Tool images mounted via `BIN` are separate — they always work; `CAPABILITY` gates only
  the runtime's *own* tool surface (notes, async jobs, agent-to-agent calls, etc.).
- Duplicates and inheritance: set-union — listing the same name twice, on one line or across files, is fine.
- Deploy tooling MAY let operators grant additional caps at run time, beyond what the Agentfile declares.

Capabilities are an **allowlist**, not a description. The runtime carries every capability it implements; the
Agentfile picks the subset this agent is allowed to expose to its model. The resolved allowlist is delivered to
the runtime, which MUST enforce it before dispatch.

### Reserved Names

A single rollup of every name this spec reserves. All are enforced at the stage shown.

| Namespace | Reserved | Stage | Why |
|---|---|---|---|
| `FROM` ref | `scratch` | parse | denotes the empty base, never a registry ref |
| `CONTEXT` names | `AGENT`, `WORKSPACE`, `MOUNTS` | validate | auto-generated context files (see [Reserved Context](#reserved-context-agentmd)) |
| `ENV` keys | `PATH`, `HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `XDG_DATA_HOME`, `TMPDIR`, `LANG`, `OTTERS_AGENT_ROOT` | validate | the locked-down base env the executor constructs (see [Execution](#execution)) |
| `ENV` keys | `OTTERSD_URL`, `OTTERS_AGENT_TOKEN` | validate | the [daemon callback](#daemon-callback) pair — injected by the executor, not user-settable |
| `ENV` key suffixes | `*_API_KEY`, `*_API_BASE` | validate | provider credentials travel through a [dedicated channel](#provider-credentials) |
| Instruction keywords | all, case-insensitively | parse | `from` and `FROM` are the same keyword |

The `RUNTIME_*` spawn-env namespace is *derived* from `CONFIG` keys rather than reserved — an `ENV RUNTIME_<X>`
is legal and deliberately overrides the derived value (see [ENV](#env)).

### Complete Example

```agentfile
# syntax=openotters/agentfile:1

FROM scratch

NAME meteo
RUNTIME ghcr.io/openotters/runtime:latest
MODEL anthropic/claude-haiku-4-5-20251001

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

ADD cities.json "Known cities with lat/lon coordinates"
```

## Part II — The Image

This part binds builders and registry tooling: what a built Image contains and how it moves.

### OCI Artifact Structure

The built Image follows the
[OCI Image Manifest](https://github.com/opencontainers/image-spec/blob/main/manifest.md) spec with a custom
artifact type. An Image contains no native binaries — the runtime and every `BIN` are lazy OCI references
resolved at deploy — so the artifact carries no `platform` and is architecture-neutral. A single Image runs
anywhere; only the referenced runtime/bin images (which may be multi-arch indexes) are matched to the host
platform at resolution time.

#### Manifest

```
manifest (schemaVersion: 2)
├── mediaType:    application/vnd.oci.image.manifest.v1+json
├── artifactType: application/vnd.openotters.agent.v1
├── config blob
├── layers[]
└── annotations
```

| Field           | Value                                        |
|-----------------|----------------------------------------------|
| `schemaVersion` | `2`                                          |
| `mediaType`     | `application/vnd.oci.image.manifest.v1+json` |
| `artifactType`  | `application/vnd.openotters.agent.v1`        |

Manifest annotations are assembled in this order (later writers win):

1. every `LABEL` key/value;
2. `org.opencontainers.image.title` from `NAME` — overriding a `LABEL` of the same key;
3. `org.opencontainers.image.created`, auto-stamped by the builder unless a `LABEL` already set it.

**Build determinism.** Because of the auto-stamped `created` annotation, the default build is **not**
reproducible — the same Agentfile yields a different manifest digest on every build. A build is reproducible
**iff** `org.opencontainers.image.created` is pinned via `LABEL`; builders MUST NOT introduce any other source of
nondeterminism into the canonical serialization ([Config Blob](#config-blob)) or the layer contents.

#### Config Blob

The manifest's `config` descriptor contains the **full serialized Agentfile** as JSON. This is the complete,
lossless representation of the parsed Agentfile — context content, configs (with required flags and
descriptions), binary references, ADD metadata, exec args, labels, args, envs, and capabilities. The only thing
it does **not** carry is `ADD` file *bytes* (see below).

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
        "value": 1024,
        "description": "Maximum output tokens per response"
      },
      {
        "key": "max-iterations",
        "value": 10,
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
      },
      {
        "name": "cat",
        "image": "ghcr.io/openotters/tools/cat:latest",
        "description": "Read file contents"
      }
    ],
    "adds": [
      {
        "src": "cities.json",
        "name": "cities.json",
        "description": "Known cities with lat/lon coordinates"
      }
    ]
  }
}
```

**Canonical serialization.** The config blob is content-addressed — its digest is part of the artifact — so its
byte-level form is normative: two-space-indented JSON with fields in exactly this order: `from`, `runtime`,
`model`, `name`, `contexts`, `configs`, `bins`, `adds`, `exec`, `labels`, `args`, `envs`, `capabilities`. Every
field except `from` is omitted when empty, so a minimal agent serializes to just `from` plus whatever it declares
(the example above shows the [Complete Example](#complete-example)'s blob — `exec`, `labels`, `args`, `envs`, and
`capabilities` are present in the schema but appear only when declared). Consumers MUST ignore unknown fields —
the blob schema evolves additively, like the grammar.

The config blob is the source of truth for the agent's **definition**: deserializing it reconstructs the parsed
Agentfile without walking the layers. Heredoc-declared `CONTEXT` entries carry their full `content` inline in the
blob; a `file://`-declared context carries only its `file` reference — its bytes live in the corresponding
context layer, exactly like `ADD` data.

It likewise does **not** embed `ADD` file bytes — `adds[]` carries only `src`/`name`/`description` metadata; the
file content lives exclusively in the corresponding `application/octet-stream` layer. So reading only the config
blob recovers the agent definition and every inline context, but **not** file-context or data-file bytes.
Materializing an Agent at deploy requires extracting those layers (a *hydrated* load).

#### Layers

The manifest carries three kinds of layers, in this order: the Agentfile source first, then one layer per
`CONTEXT`, then one per `ADD`.

| Source           | Media Type                               | Title Annotation                   |
|------------------|------------------------------------------|------------------------------------|
| Agentfile source | `application/vnd.openotters.agentfile`   | `Agentfile`                        |
| `CONTEXT`        | `application/vnd.openotters.context.v1`  | `{name}.md` (e.g. `SOUL.md`)       |
| `ADD`            | `application/octet-stream`               | `<name>` (e.g. `cities.json`)      |

Each layer carries an `org.opencontainers.image.title` annotation identifying the file. The source layer holds
the verbatim Agentfile text and materialises at `etc/Agentfile`; it preserves exactly what the operator wrote
(comments included), which the config blob — a *parsed* representation — cannot.

#### Artifact Example

For the meteo agent example, the artifact looks like:

```
manifest (artifactType: application/vnd.openotters.agent.v1)
├── config (application/vnd.openotters.agent.config.v1+json)
│   └── full serialized Agentfile JSON (source of truth)
├── layer: Agentfile (application/vnd.openotters.agentfile)
├── layer: SOUL.md (application/vnd.openotters.context.v1)
├── layer: IDENTITY.md (application/vnd.openotters.context.v1)
├── layer: cities.json (application/octet-stream)
└── annotations: {"description":"Weather assistant...", "org.opencontainers.image.title":"meteo"}
```

### Distribution

Images are standard OCI artifacts: any OCI-compliant registry and transport works, with references following the
OCI distribution grammar (see [Format Notation](#format-notation)). Two properties are normative:

- **Pull copies the whole artifact graph** — manifest, config blob, and every layer (source + context + `ADD`
  data). Nothing is left behind to fetch later; the config blob alone reconstructs the agent *definition* (see
  [Config Blob](#config-blob)), while the layers carry the file-context and `ADD` bytes it does not embed.
- **`RUNTIME` and `BIN` references stay lazy** — they are **not** pulled with the Image. The executor resolves
  them at deploy.

For environments without registry access, the builder module additionally provides registry-less transport: an
Image can be exported to — and re-imported from — a single self-contained JSON file carrying the manifest
descriptor and every blob. Export/import are builder-module primitives (see
[Command Notation](#command-notation)).

## Part III — The Runtime Environment

This part binds executors and runtimes: how an Image becomes a running Agent, and the **environment** the runtime
boots into — where its config lands, how it is invoked, and what it may rely on finding on disk and in the
process environment.

It deliberately does **not** define the wire protocol a runtime then speaks. How a client, supervisor, or peer
talks to a running agent is an **orchestrator** concern — a runtime binding, like [Channels and
Neighbors](#out-of-scope-channels--neighbors) — and the orchestrator that pairs a runtime with a way to drive it
owns that contract (openotters' runtimes serve an [A2A](https://a2a-protocol.org) endpoint; other orchestrators
MAY define their own). The Agentspec's obligation is only that a runtime boots into the environment described
here; what it serves on `--addr` is out of scope.

### Execution

The runtime binary is invoked with the arguments specified by the `EXEC` instruction (default `["serve"]`), after
which the executor appends its own flags — the one place this list is defined:

- `--root <agent-root>` — the materialised agent tree
- `--model <provider/model>` — the resolved `MODEL` value
- `--addr <addr>` — the address the runtime binds its server on, when the executor assigns one (the protocol it
  serves there is the orchestrator's concern, not the Agentspec's)

Credentials are **never** passed on the command line — argv leaks through process listings. They arrive through
the environment (see [Provider Credentials](#provider-credentials)).

The spawn environment is **locked down**: the executor constructs it from scratch (no host-environment
inheritance) with exactly `PATH` (the agent's bin dirs), `HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`,
`XDG_DATA_HOME`, `TMPDIR` (all rooted inside the agent tree), `LANG`, `OTTERS_AGENT_ROOT`, the daemon keys
`OTTERSD_URL` / `OTTERS_AGENT_TOKEN` when applicable, provider credentials, the `RUNTIME_*` `CONFIG` export, and
declared `ENV`s — in that order, so `ENV` wins on collisions.

The runtime process is spawned with `workspace/` as its working directory; tools it spawns inherit it.

The runtime MUST:

1. Read agent configuration from `<root>/etc/agent.yaml`
2. Load context files from `<root>/etc/context/`
3. Discover tool binaries from `agent.yaml`'s `tools[].binary` paths (the system executor materialises them
   under `<root>/usr/bin/`; a container backend may place them elsewhere and record the path there)
4. Serve its interface on the address specified by `--addr` (protocol orchestrator-defined)
5. Block until the process receives `SIGINT` or `SIGTERM`, then shut down gracefully
6. Exit with code 0 on clean shutdown, non-zero on error

### Provider Credentials

Provider credentials never appear in an Agentfile, an Image, or `agent.yaml`. The executor resolves them (from
daemon configuration or host environment) and injects them into the spawn environment as
`<PROVIDER>_API_KEY` / `<PROVIDER>_API_BASE`, where `<PROVIDER>` is the uppercased provider segment of `MODEL`
(e.g. `MODEL anthropic/…` → `ANTHROPIC_API_KEY`). This is why `ENV` keys with those suffixes are reserved: user
declarations must not shadow or leak the credential channel.

### Daemon Callback

Some capabilities (notes, async jobs, agent-to-agent calls, persistent chat state) are backed by the
orchestrator daemon rather than the runtime process itself. The channel for them is the **daemon callback**: an
agent-side connection from the runtime back to the daemon, configured entirely through two reserved
environment variables the executor injects at spawn:

- `OTTERSD_URL` — the orchestrator endpoint, in one of exactly two forms: `unix://<path>` (a bind-mounted socket)
  or `http://<host>:<port>` (port defaults to 80 when omitted). Any other form is invalid.
- `OTTERS_AGENT_TOKEN` — an opaque token minted by the orchestrator for this agent; the runtime presents it on
  every callback per the orchestrator's protocol.

The executor MUST inject the pair **both-or-neither** — one without the other is useless, since the callback
requires both.

The agent-facing API spoken over this channel is defined by the orchestrator and is out of scope for the
Agentspec. This spec defines only the channel contract — the two reserved env vars and their semantics:

- **The token is the agent's identity and authority.** It carries the agent reference and the resolved
  capability allowlist; the orchestrator enforces the allowlist on every callback. Runtimes MUST NOT persist the
  token to disk — it exists only in the spawn environment.
- **Rotation is paired with restart.** The token is read only at spawn; when the orchestrator re-issues it (link
  changes, capability changes), it restarts the agent. Runtimes MUST NOT assume any in-process refresh
  mechanism.
- **Absent callback = graceful degradation.** When the pair is not injected, the runtime MUST still start:
  daemon-backed capability tools are simply absent from the model's tool surface, and conversation state is not
  persisted. No error, no crash — a runtime that refuses to start without a daemon is non-conformant.
- **Liveness flows the other way.** The orchestrator probes the runtime over its own protocol; agents do not
  register, announce, or heartbeat over the callback channel.

### agent.yaml

`etc/agent.yaml` is the executor-generated, resolved runtime configuration — the runtime's primary read path. It
is **not** the Agentfile (the verbatim source sits next to it at `etc/Agentfile`): it carries the flattened,
deploy-resolved view. The `id`, `image`/`runtime` provenance, and `mounts` fields are **deploy-state** —
populated by the executor at materialise time, not derivable from the Agentfile. Schema (normative; `?` marks
optional fields):

```yaml
id: <uuid>                    # deploy-state: agent instance identity
name: <string>                # from NAME
model: <string>               # from MODEL
workspace: <path>             # the runtime's working directory (agent-root-relative)
image:                        # deploy-state: the Image this agent came from
  ref: <oci-ref>
  digest: <digest>?
runtime:                      # deploy-state: the resolved RUNTIME image
  ref: <oci-ref>
  digest: <digest>?
  binary: <path>?             # where the executor placed the runtime binary
configs:                      # flattened CONFIG view (one value per key)
  <key>: <string>
capabilities:                 # resolved CAPABILITY entries (name + description)
  - name: <string>
    description: <string>?
envs:                         # declared ENV keys + descriptions — values are
  - key: <string>             # NOT persisted here; they travel via spawn env
    description: <string>?
mounts:                       # deploy-state: operator bind-mounts
  - target: <path>
    description: <string>?
    read_only: <bool>?
context:                      # declared context files
  - name: <string>
    file: <path>              # agent-root path to the materialised markdown
    description: <string>?
tools:                        # resolved BIN surface
  - name: <string>
    description: <string>?
    binary: <path>            # agent-root path to the extracted binary
    ref: <oci-ref>?
    digest: <digest>?
    usage: <path>?            # agent-root path to the bin's USAGE.md
```

Unknown fields MUST be ignored by runtimes (the schema is open for additive evolution).

### Agent Filesystem Layout

At deploy, an Agent is materialized as a directory following Linux FHS conventions. Paths in this section are
agent-root-relative (see [Path Conventions](#path-conventions)).

```
<agent-root>/
├── etc/
│   ├── Agentfile                 # verbatim source (from the artifact's source layer)
│   ├── agent.yaml                # resolved runtime config (generated by executor)
│   ├── context/                  # from CONTEXT instructions + auto-generated files
│   │   ├── AGENT.md              # auto-generated (reserved)
│   │   ├── WORKSPACE.md          # auto-generated (reserved)
│   │   ├── SOUL.md
│   │   └── IDENTITY.md
│   └── data/                     # from ADD instructions (+ per-BIN usage docs)
│       └── cities.json
├── home/                         # HOME of the runtime process
├── usr/
│   ├── local/
│   │   └── bin/
│   │       └── runtime           # runtime binary (pulled from RUNTIME OCI image)
│   └── bin/                      # tool binaries (pulled from BIN OCI images)
│       ├── wget
│       ├── jq
│       └── cat
├── workspace/                    # process working directory (read-write)
├── tmp/                          # ephemeral scratch space (read-write)
└── var/
    └── lib/                      # runtime-managed persistent state (read-write)
```

The runtime process starts with `workspace/` as its working directory. A runtime MUST NOT write outside
`workspace/`, `home/`, `tmp/`, and `var/lib/`; executors SHOULD enforce this by mounting the remaining paths
read-only where the backend supports it.

`var/lib/` is the directory the runtime owns for durable state across restarts. Its internal layout is a
**runtime implementation detail** — the reference runtime keeps a SQLite conversation store there, but the spec
guarantees only the directory, not any file name or storage engine.

| Path                    | Access     | Source                 | Purpose                            |
|-------------------------|------------|------------------------|------------------------------------|
| `etc/Agentfile`         | read-only  | artifact source layer  | Verbatim Agentfile source          |
| `etc/agent.yaml`        | read-only  | executor-generated     | Resolved runtime config            |
| `etc/context/`          | read-only  | `CONTEXT` instructions | System prompt context files        |
| `etc/context/AGENT.md`  | read-only  | auto-generated         | Agent metadata, bins, data         |
| `etc/data/`             | read-only  | `ADD` instructions     | Static data files                  |
| `usr/local/bin/runtime` | read-only  | `RUNTIME` OCI image    | Runtime binary                     |
| `usr/bin/`              | read-only  | `BIN` OCI images       | Tool binaries                      |
| `home/`                 | read-write | —                      | Process HOME                       |
| `workspace/`            | read-write | —                      | Working directory                  |
| `tmp/`                  | read-write | —                      | Ephemeral scratch space            |
| `var/lib/`              | read-write | —                      | Runtime-managed persistent state   |

#### Reserved Context: AGENT.md

`AGENT.md` is auto-generated by the executor and cannot be used as a `CONTEXT` name (nor can `WORKSPACE` or
`MOUNTS`, the other executor-generated context files — see [Reserved Names](#reserved-names)). It contains:

- Agent name and description (from `NAME` and `LABEL description`)
- Available binaries with descriptions and usage (from `BIN`)
- Available data files with descriptions (from `ADD`)
- Filesystem layout (read-write paths)

Its exact wording is executor-defined, not part of this contract.

### Readiness

The runtime becomes reachable on `--addr` once it is ready to serve. How readiness is probed and how long the
orchestrator waits is defined by the orchestrator's protocol (openotters' runtimes answer a `Ready` check; the
**supervising orchestrator** owns the retry loop and startup deadline, treating a runtime that never becomes
ready as failed). The Agentspec requires only that the runtime bind `--addr` when ready and not before.

### Model Validation

The runtime SHOULD validate that the specified model is available before it begins serving. If the model is not
found (e.g. not pulled in Ollama, invalid API key), the runtime MUST exit immediately with a non-zero exit code
and a human-readable error message on stderr.

## Security Considerations

- **Names are path-constrained.** `NAME`, `CONTEXT`, `BIN`, and `ADD` names materialize as paths under the agent
  root. The path-safe rule ([Lexical Conventions](#lexical-conventions)) exists so that no declared name can
  contain separators or traversal sequences and escape the tree; validators MUST enforce it.
- **Pin references by digest.** `RUNTIME` and `BIN` references are resolved and *executed* at deploy; a mutable
  tag (`:latest`) means the code an agent runs can change between deploys without the Image changing. Production
  Agentfiles SHOULD pin `RUNTIME` and `BIN` refs by digest (`@sha256:…`).
- **Platform resolution MUST fail closed.** When a referenced image is a multi-arch index with no manifest
  matching the host platform, the executor MUST report an error — never silently select an arbitrary manifest.
- **Secrets never enter the artifact.** API keys and other credentials MUST NOT appear in an Agentfile, the
  config blob, any layer, or `agent.yaml`. Provider credentials travel exclusively through the
  [provider credential channel](#provider-credentials); the reserved `ENV` suffixes exist to keep user
  declarations out of it, and credentials are never placed on argv.
- **The spawn environment is closed by construction.** The executor builds it from scratch — host variables
  (`SSH_AUTH_SOCK`, cloud credentials, the host `PATH`) never reach the agent process.
- **The agent token is spawn-env-only.** `OTTERS_AGENT_TOKEN` is the agent's identity and carries its capability
  allowlist ([Daemon Callback](#daemon-callback)); runtimes MUST NOT write it to disk, and it never appears in
  the artifact or `agent.yaml`. The orchestrator re-issues it across restarts, so a leaked token's lifetime is
  the orchestrator's to bound.

## Out of Scope: Channels & Neighbors

The Agentfile intentionally describes a **single agent as an isolated, deployable unit** — the equivalent of a
Dockerfile for containers. Two concerns are deliberately left out of this spec:

- **Channels** define how external systems communicate with an agent (Telegram, WebSocket, REST, etc.). These are
  **runtime bindings**, not build-time properties: the same Image can be exposed over different channels
  depending on the deployment environment. Channels are configured at deploy by the orchestrator, not baked into
  the artifact.
- **Neighbors** allow agents to talk to each other. This is an **orchestration concern** — it requires knowledge
  of which agents exist, how they are networked, and how they discover each other. A single Agentfile has no way
  to express this because it only knows about itself. Neighbor support belongs to a higher-level composition
  layer, outside this specification. (The [daemon callback](#daemon-callback) *channel* an agent uses to reach
  the orchestrator IS in scope — it is part of the runtime contract; which agents are linked over it, and the
  topology they form, is not.)

This separation is what buys **portability** (an Image works in any environment without modification),
**composability** (the same agent can participate in different topologies), and **single responsibility**
(Agentfile = build; composition layer = orchestration).

## Design Principles

- **One file = one deployable unit**
- **OCI-native** — output is an OCI artifact, stored in any registry
- **Lazy resolution** — binary images are references, not embedded; resolved at deploy
- **Single inheritance** — exactly one parent via `FROM`; the ancestry is always a straight line
- **Credentials are external** — MODEL names the model, the environment provides the keys
- **Platform-neutral artifact** — an Image carries no native binaries (the runtime and every `BIN` are lazy OCI
  references), so the artifact itself is architecture-independent. Platform selection happens only when those
  referenced images are resolved at deploy
- **Additive evolution** — new instructions and fields arrive within a grammar major; consumers ignore unknown
  config-blob fields; majors are reserved for breaking changes
- **Familiar syntax** — Dockerfile-like instructions, minimal learning curve

## Changelog

This document's version (the `1.0.0` in the title) is the semver of the specification prose, independent of the
`:1` grammar major (see [Syntax Directive](#syntax-directive)).

| Version | Status | Changes |
|---|---|---|
| 1.0.0 | unreleased | Restructured as a formal spec in three parts (language / Image / runtime environment) with document conventions (RFC 2119, format notation, actors), a conformance table, validation stages, merge semantics, path-safe name rules, a reserved-names rollup, a build-determinism statement, and security considerations. Annotation keys renamed `vnd.openotters.bin.*` → `io.openotters.bin.*`. `ADD` simplified to `ADD <src> [<name>]` (flat `etc/data/`). `CAPABILITY` accepts multiple names per directive. Credentials moved off argv onto the spawn env. `agent.yaml` schema and provider-credential channel specified. Push/pull/export/import condensed into Distribution. Daemon Callback channel specified (`OTTERSD_URL` / `OTTERS_AGENT_TOKEN`, graceful degradation, rotation-by-restart), with the protocol left opaque. Tool discovery clarified to run through `agent.yaml` `tools[].binary` (executor-defined paths). **Wire protocol removed from the spec entirely:** Part III is "The Runtime Environment" and defines only the boot contract (invocation, env, `agent.yaml`, filesystem); the protocol a running agent speaks — both executor↔runtime and the daemon callback — is the orchestrator's concern (openotters runtimes serve A2A). Grammar evolution declared additive within a major. |
| 0.0.1 | frozen | Initial draft — see `AGENTFILE-v0.0.1.md`. |
