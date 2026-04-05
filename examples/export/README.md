# export

Parses an Agentfile, builds the OCI artifact in memory, and exports it as a self-contained JSON file. The JSON contains
the full OCI manifest, config blob, and all layers — useful for offline transfer or inspection without a registry.

## Usage

```sh
go run ./examples/export/ <path-to-Agentfile> <output.json>
```

## Example

```sh
go run ./examples/export/ demo/meteo/Agentfile meteo.json
```

```
exported sha256:f99f92eb... → meteo.json (8118 bytes)
```
