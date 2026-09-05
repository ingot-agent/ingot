<p align="center">
  <img src="./asset/icon.png" alt="ingot logo" width="180">
</p>

# ingot

> Compose the agent you need from plugins. Ship it as one immutable binary.

[**English**](./README.md) · [**中文文档**](./docs/README.zh.md) · [Usage Guide](./docs/USAGE.md) · [使用说明](./docs/USAGE.zh.md)

ingot is a build-time composition system for agents. It treats an agent as a
graph of replaceable capabilities, not a fixed application with a handful of
extension points. The HTTP client, model providers and routing, tools, policy
interceptors, storage, prompt and context handling, agent loop, and the
user-facing application can all be supplied by plugins.

At build time, ingot resolves the selected plugins into a static Component
Graph, type-checks every connection, generates the wiring code, and compiles
the complete graph into a native Runtime Image. At runtime there is no plugin
discovery, graph resolution, dynamic loading, or reflection-based wiring: the
chosen code is already connected inside the executable.

This gives ingot its defining balance: **maximum flexibility while composing an
agent, minimum uncertainty while running it**.

## Why ingot

### Every layer is replaceable

Official plugins are useful defaults, not privileged implementations. A plugin
is an ordinary Go module with an `ingot.plugin.toml` manifest, and components
communicate through typed capability contracts. Any layer can be replaced as
long as the new component satisfies the capabilities required by the rest of
the graph.

| Layer | Official plugin examples | Possible replacement |
|---|---|---|
| Application / UI | `app.cli` | HTTP or WebSocket gateway, customer-service connector, chat platform adapter |
| Agent loop | `agent.default` | Triage workflow, domain-specific loop, deterministic orchestration |
| Model access | `http.default`, `model.openai-compatible`, `model.runtime` | Enterprise transport, another provider, custom routing or failover |
| Binary assets | `asset.local` | Object storage, shared media service, encrypted or remote immutable blobs |
| Tools | `tool.shell`, `tool.fs`, `tool.edit`, `tool.ask`, `tool.runtime` | CRM, order system, search, database, or internal APIs |
| Policy | `interceptor.approval`, `interceptor.script` | Audit, authorization, rate limits, organization-specific guardrails |
| State and context | `session.sqlite`, `context.compact`, `prompt.default` | Alternate session backends, retrieval, custom memory and prompting |

The boundary is deliberately broad: customization does not stop at tools or
model providers. It extends down to the HTTP client and up through the agent
loop to the application that exposes the agent.

### Flexible does not mean dynamic

ingot moves variability to build time and keeps the production runtime fixed:

- **Generated wiring, no reflection** — components are plain Go objects joined
  by generated `main.go` and `wiring_gen.go`.
- **Compile-time graph validation** — capability types, cardinality, missing or
  ambiguous providers, self-loops, cycles, and creation order are checked
  before an image is committed.
- **Self-contained delivery** — selected plugin implementations are compiled
  into the runtime executable. The target machine does not need Go, the ingot
  Builder, an SDK installation, or a separate plugin tree.
- **Immutable, traceable images** — exact module inputs and local sources are
  locked and hashed; the binary has its own artifact digest.
- **Safe image lifecycle** — startup validation, atomic activation, rollback,
  crash recovery, and garbage collection are built into the image workflow.
- **A small runtime surface** — startup instantiates a predetermined graph of
  ordinary Go values, and shutdown cleans them up in reverse creation order.

Runtime configuration, secrets, persistent state, and intentionally external
services remain external; what disappears is the deployment-time dependency on
the build system and plugin packages.

## More than a coding agent

The bundled profile produces a capable terminal-based coding agent, but that is
one composition of ingot, not its architectural limit.

For a customer-service agent, for example, replace `app.cli` with a network
plugin that receives conversations from the support system and streams replies
back. Replace shell and filesystem tools with plugins for tickets, CRM, orders,
and the knowledge base. Keep the default model runtime and agent loop, or swap
those too. The Builder verifies the new graph and emits the same kind of
self-contained Runtime Image, ready to distribute without shipping a plugin
framework alongside it.

The same pattern applies to internal assistants, data agents, workflow agents,
embedded agents, and other domains where the surrounding capabilities matter
as much as the model call.

## Quick start

Requires Go 1.24 or newer. One command installs the CLI, initializes the
home with the default profile (browser workspace: sessions, tools, streaming;
no filesystem tools, no approval interceptors), collects your model provider
settings, builds the runtime image, and offers to start the web UI:

```sh
./scripts/install.sh
# non-interactive alternative: set the provider first
INGOT_BASE_URL=https://api.deepseek.com INGOT_API_KEY=sk-... \
  INGOT_MODEL=deepseek-v4-flash ./scripts/install.sh
```

Manual steps, equivalent to what the installer does:

```sh
# 1. Build the CLI (or install with ./scripts/install.sh)
go build -o ingot ./cmd/ingot

# 2. Initialize a home with the official plugin set and config template
./ingot init

# 3. Set your model provider in ~/.ingot/config.toml, then compose an image
./ingot apply

# 4. Start the browser workspace
./ingot web        # then open http://127.0.0.1:7316/
```

`ingot init` materializes the official plugins under `bundled-plugins/` and
writes `builder.toml`, `plugins.toml`, and a `config.toml` template. Pass
`--profile minimal` for the smallest runnable graph (terminal CLI, no tools).
See the [Usage Guide](./docs/USAGE.md) for installation options and the full
workflow.

For the browser workspace, replace the CLI with [app.backend](./plugins/app-webui/README.md).
Its Vue + Tailwind frontend is embedded in the native Runtime Image and includes
conversations, streaming, approvals, attachments, execution details, and operations.
It is intended for trusted local, single-user use.

## How build-time composition works

```mermaid
flowchart LR
    Plugins["Plugin Go Modules<br/>(go.mod + ingot.plugin.toml)"] --> Resolve
    Desired["plugins.toml<br/>(selected composition)"] --> Resolve
    Runtime["ingot ABI<br/>(fixed host ABI)"] --> Resolve
    Resolve["Resolve + type-check<br/>Component Graph"] --> Lock["plugins.lock<br/>(exact build facts)"]
    Lock --> Generate["Generate static wiring"]
    Generate --> Compile["Compile + startup check"]
    Compile --> Image["Immutable Runtime Image<br/>(native executable + provenance)"]
    Config["config.toml<br/>(runtime values)"] --> Image
```

The composition passes through three distinct states:

1. `plugins.toml` states what you want.
2. `plugins.lock` records exactly what was resolved, including the full Go
   module graph, source digests, the pinned Runtime ABI, and build flags.
3. `images/<ImageID>/` contains the immutable native executable and its
   provenance manifest.

Changing a runtime value only changes `config.toml`. Changing an implementation
means changing the plugin set and building a new image; the old image remains
available for rollback.

## The plugin model

| Concept | Meaning |
|---|---|
| **Plugin** | A Go module declaring `ingot.plugin.toml`; the unit of distribution, versioning, configuration, and user operations. |
| **Component** | A node in the static graph; it declares typed dependencies and exports and constructs plain Go values with `New`. |
| **Capability** | A stable Go contract exchanged by components and checked at build time with `go/packages` and `go/types`. |
| **Runtime Image** | One resolved, generated, compiled, checked, and immutable agent composition. |

Components do not register themselves in a global container. They expose
ordinary named structs and a constructor:

```go
type Dependencies struct {
    // Capabilities consumed by this component.
}

type Exports struct {
    // Capabilities provided by this component.
}

func New(
    ctx context.Context,
    cfg Config,
    deps Dependencies,
) (Exports, ingotabi.Cleanup, error)
```

The Builder reads these contracts, resolves `ONE`, `OPTIONAL`, and `MANY`
dependencies, establishes a deterministic creation order, and writes the calls
that ordinary Go code would make by hand. The Component ABI primitives
(`Cleanup`, `Optional`, `Named`) and every runtime-owned host contract
(invocation metadata, lifecycle shutdown, plugin state scope) live in the
fixed [ingot ABI](https://github.com/ingot-agent/ingot-abi). The
replaceable agent capability contracts live in the separate
[ingot SDK](https://github.com/ingot-agent/sdk) or any other domain contract
module; no contract module needs Builder configuration.

To add or replace a plugin:

```sh
ingot plugin add github.com/example/my-plugin@v1.2.3
ingot plugin add --path ../my-local-plugin
ingot plugin remove app.cli
ingot apply
```

If the new composition has a missing, duplicate, or cyclic capability, the
build fails before it can become the active image.

## Build guarantees

- **Strict canonical inputs** — `builder.toml`, `plugins.toml`, `plugins.lock`,
  and `ingot.plugin.toml` are parsed strictly and represented by canonical
  digests.
- **Fixed Runtime ABI** — the Builder pins the exact ingot ABI module path,
  version, and source identity; production builds refuse an unpinned or
  MVS-upgraded ingot ABI.
- **Ordinary contract modules** — agent and domain SDKs need no Builder
  configuration; they participate in the Component Graph through plain Go
  type identity and are locked as ordinary modules.
- **Content-addressed identity** — `ImageID` identifies the complete build
  inputs; `ArtifactDigest` identifies the final executable bytes.
- **Reproducibility checks** — rebuilding an existing `ImageID` must reproduce
  its artifact digest instead of silently replacing different bytes.
- **Transactional activation** — `apply` resolves, builds, validates, and then
  switches `current` atomically. Failed builds never replace the running image.

## The ingot home

Everything lives in `~/.ingot` by default; use `--home PATH` to select another
location.

| Path | Role |
|---|---|
| `builder.toml` | Builder configuration (no SDK list; the ingot ABI is fixed). |
| `plugins.toml` | The desired plugin composition. |
| `plugins.lock` | Exact resolution, source hashes, module graph, and build flags. |
| `config.toml` | Runtime values, including provider configuration and secrets. |
| `bundled-plugins/` | Materialized sources for the official plugin set. |
| `current` | Atomic pointer to the active image. |
| `images/<ImageID>/` | Immutable runtime executable and `manifest.json`. |

## Commands at a glance

```text
ingot [--home PATH] <command>

init        Initialize a home with an official plugin profile
bundle      Check or update the official plugin bundle
resolve     Resolve plugins.toml and refresh plugins.lock
build       Build the locked composition without activating it
apply       Resolve + build + atomically activate
status      Show desired, locked, built, and current state as JSON
inspect     Inspect the environment or one plugin as JSON
rollback    Activate the previous image
gc          Remove old images while preserving rollback safety
plugin      add | remove | update | reorder | list | inspect
<other>     Dispatch to the active image, for example ingot chat
```

See the [Usage Guide](./docs/USAGE.md) or
[使用说明](./docs/USAGE.zh.md) for the complete command reference.

## Documentation

- [中文 README](./docs/README.zh.md)
- [Contributing guide](./CONTRIBUTING.md) · [贡献指南](./docs/CONTRIBUTING.zh.md)
- [Usage Guide](./docs/USAGE.md) · [使用说明](./docs/USAGE.zh.md)
- [Architecture design v0.3](./docs/ingot_架构设计_v0.3.md) (Chinese)
- [Plugin manifest design](./docs/ingot.plugin.toml_设计方案_v0.1.md) (Chinese)
- [`plugins.toml` design](./docs/ingot_plugins.toml_v0.1_设计方案.md) (Chinese)
- [`builder.toml` design](./docs/ingot_builder.toml_v0.1_设计方案.md) (Chinese)
- [`plugins.lock` design](./docs/ingot_plugins.lock_v0.1_设计方案.md) (Chinese)
- [SDK design v0.1](./docs/ingot_SDK_v0.1_设计方案.md) (Chinese)
- [SDK multimodal v0.2 migration](../ingot_SDK_多模态协议迁移方案.md) (Chinese)
- [ingot ABI design v0.1](./docs/ingot_ABI_v0.1_设计提案.md) (Chinese)
- [`ingot init` design](./docs/ingot_init_设计方案_v0.1.md) (Chinese)

## Repository layout

- `cmd/ingot` — CLI entry point.
- `internal/cli` — command parsing and user-facing output.
- `internal/home` — desired/locked/current state, plugin mutations, image
  switching, rollback, GC, transactions, runtime dispatch, and initialization.
- `internal/bundle` — official plugin profiles and source materialization.
- `internal/builder` — resolution, type analysis, component graph, code
  generation, reproducible build, and image validation.
- `plugins/` — official plugins; every directory is an independent Go module.
- `scripts/` — Unix and PowerShell installation scripts.

For local development, place the ingot ABI repository beside this
repository; the included `go.work` selects it with a workspace replacement.

## Development

Run the Builder, integration, SDK, and plugin tests from this directory:

```sh
go test -race ./...
for plugin_dir in plugins/*; do
  if [ "$plugin_dir" = plugins/app-cli ]; then
    (cd "$plugin_dir" && GOWORK=off go test -race ./...)
  else
    (cd "$plugin_dir" && go test -race ./...)
  fi
done
(cd ../sdk && go test -race ./...)
```

`app-cli` remains on SDK v0.1.3 until its planned rewrite, so its legacy suite
is intentionally isolated from the v0.2 workspace.

## Roadmap

- [x] `ingot init` — create a runnable plugin profile and configuration.
- [ ] `ingot doctor` — validate plugin completeness, configuration, and the
  active image.

## License

[MIT](./LICENSE)
