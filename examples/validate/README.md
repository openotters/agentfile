# validate

Parses an Agentfile and checks that it is syntactically and semantically valid, printing the result. Exits with code 1
if invalid.

## Usage

```sh
go run ./examples/validate/ <path-to-Agentfile>
```

## Example

```sh
go run ./examples/validate/ demo/meteo/Agentfile
```

```
valid
```
