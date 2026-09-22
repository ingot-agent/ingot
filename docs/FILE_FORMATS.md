# Current Builder file formats

This reference describes the checked-in Core implementation. Use it with the
[usage guide](USAGE.md), rather than copying configuration from older design
proposals. The parser source and tests linked below define strict validation.

## Ownership and versions

| File | Owner / location | Current version |
|---|---|---|
| `builder.toml` | Core, under managed Home | `builder_config_version = 1` |
| `plugins.toml` | Project's ordered direct plugin recipe | `plugins_version = 1` |
| `ingot.plugin.toml` | Plugin module root | `manifest_version = 1` |
| `plugins.lock` | Generated project resolution | `lock_version = 3` |
| Collection TOML | Local file or HTTPS recipe | `collection_schema = 1` |
| Image `manifest.json` | Generated immutable image provenance | schema 3 |
| Plugin `config.toml` and other state | Plugin's assigned Runtime state scope | Defined by that plugin |

These versions describe different formats. The Core binary version, Builder
version, `ingot` manifest compatibility version, ABI module version and plugin
module version are separate identities. `ingot version` reports the Core and
protocol identities; do not infer one from another.

## builder.toml

The complete accepted configuration is:

```toml
builder_config_version = 1
```

There are currently no user-defined Go build/environment tables or SDK lists.
Unknown keys/tables and unsupported versions are rejected. A missing file uses
the embedded default. `ingot setup` initializes managed Home; `ingot init`
creates a project recipe and ensures Home exists. See
[config.go](../internal/builder/config.go) and [config tests](../internal/builder/config_test.go).

The Builder pins `github.com/ingot-agent/ingot-abi@v0.1.0`. Ordinary SDK/contract
modules participate through plugin imports and Go type identity, without Core
configuration. Development workspace overrides must remain development inputs;
release checks use independently resolvable module versions.

## plugins.toml

```toml
plugins_version = 1

[[plugins]]
module = "github.com/ingot-agent/plugins/http-default"
version = "v0.1.0"

[[plugins]]
module = "example.com/team/custom-plugin"
path = "../custom-plugin"
```

This illustrates source forms, not a complete runnable agent composition.

| Key | Rule |
|---|---|
| `plugins_version` | Required integer `1` |
| `[[plugins]]` | At least one entry; array order is significant |
| `module` | Valid Go module path, unique in the direct recipe |
| `version` | Exact canonical Go module version, including semantic import-major rules |
| `path` | Local module locator, resolved relative to the recipe when relative |

Each entry has **exactly one** of `version` and `path`. Version ranges and
`latest` are not persisted recipe versions. CLI commands may resolve a selector
to an exact version before writing it. The module declared by a local `go.mod`
must match the recipe identity. There are no plugin-private settings or secrets
in this file. See [desired.go](../internal/builder/desired.go) and
[schema tests](../internal/builder/schema_test.go).

## ingot.plugin.toml

```toml
manifest_version = 1
name = "example.service"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

| Key | Rule |
|---|---|
| `manifest_version` | Required integer `1` |
| `name` | Required short name, 1–64 bytes, matching `[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*` |
| `ingot` | Supported compatibility interval checked against the Builder's Ingot contract version |
| `config_package` | Required module-relative root package locator; still a v1 schema field, **not** configuration injection |
| `components` | Nonempty ordered array of unique component names and package paths |
| `components[].name` | Same short-name grammar as the plugin |
| `components[].package` | `.` or a canonical `./...` package path |
| `[state]` | Optional `schema_version` and `min_reader_version`, both positive, with minimum ≤ current |
| `[meta]` | Optional `display_name`, `description`, `homepage`, `repository`, `license` |

Package paths must resolve inside the module directory, and Go package loading
must succeed. They cannot contain `internal`, `..`, backslashes, empty segments or trailing
slashes. Metadata is descriptive; it is excluded from the manifest's build
projection. State schema declarations do not implement migrations: the plugin
owns compatibility and file access. The `ingot` range is a whitespace-separated
AND of `=`, `>`, `>=`, `<` and `<=` comparators (bare versions mean equality),
with no `v` prefix, wildcard, caret, tilde or OR syntax. See
[manifest.go](../internal/builder/manifest.go).

Each component package declares named `Dependencies` and `Exports` structs and
this exact non-variadic constructor:

```go
func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error)
```

The former `New(ctx, cfg, deps)` signature is rejected. The Builder loads the
root package but does not require or pass a root `Config` value. It resolves
typed capabilities and injects exact ABI `invocation.Invocation`,
`lifecycle.Controller` and `state.Scope` dependencies as virtual host providers.
Plugins cannot export those host types. See [graph.go](../internal/builder/graph.go),
[generated wiring](../internal/builder/generate.go) and
[ADR 0003](adr/0003-plugin-configuration.md).

## plugins.lock and image provenance

The lock is generated by `ingot project resolve` or the build workflow. It
records ordered plugin identities, manifest/source digests, components, state
schema declarations, exact module graph and fixed ABI identity. Local sources
also record replacement locators and content identity. Do not hand-author it.

Lock v3 is **target-neutral**. Persisted lock TOML does not contain `[target]`,
`[toolchain]`, `[environment]` or `[build]` tables. Target-specific toolchain,
environment and build choices belong to the build manifest and image identity.
The in-memory Go `Lock` struct also carries build-time fields, but these are
marked `toml:"-"`; that does not make them accepted lock-file fields.

See [lock.go](../internal/builder/lock.go), [lock tests](../internal/builder/lock_test.go),
[build.go](../internal/builder/build.go) and [image types](../internal/image/types.go).
`ImageID` identifies canonical build inputs; `ArtifactDigest` identifies the
binary bytes. Use `ingot image show`/`verify` for inspection. Tags and Runtime
bindings are mutable references to immutable images, not edits to an image.

## Collections

```toml
collection_schema = 1
id = "example.com/team/collections/example"
version = "v1.0.0"

[metadata]
name = "Example collection"
description = "One released plugin reference"
homepage = "https://example.com"

[[plugins]]
module = "github.com/ingot-agent/plugins/http-default"
version = "v0.1.0"
```

`id` follows Go module-path syntax; Collection and plugin versions are exact
canonical module versions. Metadata name is required; description/homepage are
optional. Homepage must be an absolute HTTP(S) URL without credentials. Plugin
entries are nonempty, ordered and unique by module. No local paths, nested
Collections, runtime settings or secrets are accepted.

Use `ingot collection inspect`, `plan`, then `apply` as described in
[USAGE.md](USAGE.md). Applying a Collection edits the project's desired recipe;
it preflights changed candidates through module resolution and atomically
commits the recipe and lock, but does not build, bind or restart. Conflicts must
be resolved explicitly. See [Collection parser](../internal/collection/model.go),
[planner](../internal/collection/plan.go) and [ADR 0007](adr/0007-collection-v1.md).

## Plugin state and runtime configuration

Managed Runtime state is under `INGOT_HOME/runtimes/<runtime>/state/<manifest-name>/`.
For example, `app-webui` declares `app.backend`, so its scope is `state/app.backend/`.
Standalone Runtime Images use `INGOT_RUNTIME_HOME` when set; otherwise they use
`<absolute-executable-path>.home`. There is no standalone `--home` flag: Core's
`--home` selects managed Home, and application arguments are separate. All components in one plugin
share its state scope; plugins own file schemas, validation and persistence.

There is no global `[plugins.<name>]` runtime configuration envelope. Current
plugin examples belong to each plugin's file. Configuration Operations may
publish live updates or report that a restart is needed. Read the
[plugin configuration guide](https://github.com/ingot-agent/plugins/blob/main/docs/configuration.md)
for activation rules and the owning README for exact fields.
