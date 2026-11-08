# Go backend with OpenAPI contracts

Every backend app (`api`, `worker`, `ledger`, `payments-mock`, `chaos`) is Go, in one module with a `cmd/` per app; only the web app is TypeScript. Go doesn't make `reserve` faster, since Redis runs commands one at a time and caps throughput. We chose it for steady p99 latency under a flood, use of every core in one process, built-in `pprof`, and single static binaries that keep `kill -9` and restart tests clean. Contracts: one OpenAPI spec for `api`, generating the Go server (`oapi-codegen`) and the TS client (`openapi-typescript`). Event payloads are Go structs in a shared internal package with a `schema_version` field.

## Considered Options

- **TypeScript everywhere with zod contracts** (the original plan): dropped with the move to Go.
- **Connect + protobuf**: rejected. The only boundary between languages is browser → `api`, where REST/JSON is easier to inspect and show, and Go-to-Go events already get compile-time safety from shared structs.
