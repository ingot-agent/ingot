# ingot Bootstrap and Builder

This module implements the Bootstrap and Builder described by the v0.3
architecture and v0.1 file formats in [`docs`](./docs/).

The Builder provides:

- strict `plugins.toml`, `plugins.lock`, and `ingot.plugin.toml` parsing;
- canonical desired, Manifest projection, BuildManifest, and SHA-256 identity;
- local source hashing and exact Go module graph restoration/verification;
- `go/packages`/`go/types` Component contract validation;
- ONE, OPTIONAL, and MANY resolution, self-loop/cycle diagnostics, and stable
  topological ordering;
- generated `main.go` and `wiring_gen.go`, reverse Cleanup, startup value
  validation, native compilation, and `--ingot-check`;
- immutable image creation with separate ImageID and ArtifactDigest.

The Bootstrap command provides plugin mutation, resolve/build/apply, status and
inspection, rollback, image GC, atomic `current` switching, transaction
recovery, and single-writer coordination.

```sh
go build ./cmd/ingot

./ingot plugin add github.com/example/plugin@v1.2.3
./ingot plugin add --path ../local-plugin
./ingot plugin reorder approval --before script
./ingot apply
./ingot status
```


## Repository layout

- `cmd/ingot`: CLI entry point.
- `internal/cli`: command-line parsing and user-facing output.
- `internal/home`: ingot home state, plugin mutation, image switching,
  rollback, GC, transactions, and runtime dispatch.
- `internal/builder`: resolution, component graph, code generation, and
  immutable image build.
- `github.com/ingot-agent/sdk` is a separate repository:
  https://github.com/ingot-agent/sdk. During local development it can be
  placed as a sibling checkout and selected with `go.work` or a temporary
  `replace` directive.

## Planned `ingot init`

`ingot init` is intended to give users an out-of-the-box default environment:
it will write a default `plugins.toml` and `config.toml` so users can go from
installation to a working agent without first assembling a plugin set manually.
See [`docs/ingot_init_设计方案_v0.1.md`](./docs/ingot_init_设计方案_v0.1.md).

The default home is `~/.ingot`; use `--home PATH` before the command to select a
different environment. Runtime configuration lives in `config.toml` and plugin
composition lives in `plugins.toml`.

Run all conformance and integration tests with:

```sh
go test -race ./...
(cd sdk && go test -race ./...)
```
