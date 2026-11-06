# Relay Developments (`relay-developments/`)

Staging directory for experimental modules, transport benchmarks, and feature tests prior to merging into `main`.

## Workflow Rule
1. Develop and validate new features on the `develop` or `experiment/*` branch inside `relay-developments/`.
2. Run `go vet ./...` and `go test -race ./...`.
3. Once verified stable, promote the module into `relay-ssh/`, `relay-security/`, `relay-web/`, or `relay-utilities/` and merge into `main`.
