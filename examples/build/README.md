# build

Parses an Agentfile and builds the OCI artifact **into an in-memory store**.
Prints the resolved reference and the build descriptor as JSON. No network
I/O — use [`examples/push`](../push/) to publish the result to a registry
or [`examples/export`](../export/) to write it to a self-contained JSON file.

## Usage

```sh
go run ./examples/build/ <path-to-Agentfile>
```

## Example

```sh
go run ./examples/build/ demo/meteo/Agentfile
```

```
ref: meteo:latest@sha256:85819d2a…
{
  "Reference": { "Name": "meteo", "Tag": "latest" },
  "Digest":    "sha256:85819d2a…"
}
```
