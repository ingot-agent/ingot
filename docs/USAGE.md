# ingot Usage Guide

> English version · [中文版](./USAGE.zh.md)

ingot separates project recipes, immutable Images, persistent Runtimes, and
individual Processes. There is no global active image and unknown commands are
never dispatched implicitly.

## Install And Initialize

The official installers download the matching immutable core archive from
GitHub Releases, verify its SHA-256 digest, and install only the `ingot`
executable. Installation does not require Go and never creates or modifies
`INGOT_HOME`, plugins, Images, or Runtimes.

```sh
# Linux and macOS; default destination: ~/.local/bin/ingot
curl -fsSL https://github.com/ingot-agent/ingot/releases/latest/download/install.sh | sh
```

```powershell
# Windows PowerShell; default destination: %LOCALAPPDATA%\ingot\bin\ingot.exe
$installer = Join-Path $env:TEMP 'install-ingot.ps1'
Invoke-WebRequest https://github.com/ingot-agent/ingot/releases/latest/download/install.ps1 -OutFile $installer
& $installer
Remove-Item $installer
```

Both installers select the latest stable Release by default. Select an exact
version, including a prerelease, with `--version` on Unix or `-Version` on
Windows. Reinstalling the same version or downgrading requires the explicit
force option. The `v0.3.1` examples below illustrate version syntax; select a
version that is actually published on the [Releases page](https://github.com/ingot-agent/ingot/releases):

```sh
curl -fsSL https://github.com/ingot-agent/ingot/releases/latest/download/install.sh | \
  sh -s -- --version v0.3.1
```

```powershell
Invoke-WebRequest https://github.com/ingot-agent/ingot/releases/latest/download/install.ps1 -OutFile $installer
& $installer -Version v0.3.1 -Force
Remove-Item $installer
```

Unix options are `--prefix`, `--bindir`, `--destdir`, `--version`, and
`--force`. Their PowerShell equivalents are `-Prefix`, `-BinaryDir`,
`-DestDir`, `-Version`, and `-Force`. Existing pre-release Homes are left
untouched; the removed Home, profile, configuration, and Runtime installer
flags have no automatic replacement.

To build the Core from a checkout of this repository, Go 1.24.2 or newer is required:

```sh
GOWORK=off go build -o ingot ./cmd/ingot
```

The release workflow produces Linux, macOS, and Windows archives for `amd64`
and `arm64`. On Unix, ensure the installation directory is on `PATH` (the script
does not change your shell startup files). The Windows installer updates the
user and current-process `PATH` unless using `-DestDir` staging.

Installing or running an existing Image does not require Go. Building or
resolving a composition requires `go` on `PATH`, enough disk for the module
cache and build output, and access to the selected modules through `GOPROXY`
(or an already populated cache). The current Builder compiles and validates
Images for its own host OS/architecture with CGO disabled. It sets `GOWORK=off`
and `GOTOOLCHAIN=local`; it does not automatically download another Go toolchain.
Plugin dependencies may impose a newer minimum Go version than the Core.
Node is needed only when developing/rebuilding the browser frontend in the
plugins repository, not to run its embedded released assets.

For development, the Builder separately scans the CLI working directory's
ancestors for a `go.work` containing versionless local `replace` entries for
the ABI or selected dependencies. These can produce local replacements in the
lock even with `GOWORK=off`. Run released-module verification from a directory
outside such a workspace and inspect the lock; changing `--home` alone does
not isolate this source lookup. See [RELEASE.md](../RELEASE.md) for a fresh
released-profile verification procedure.

On PowerShell, the equivalent source-build command is:

```powershell
$env:GOWORK = 'off'
go build -o ingot.exe ./cmd/ingot
```

After either installation method, initialize managed Home explicitly:

```sh
ingot setup
```

`setup` initializes schema v2 in `INGOT_HOME`, or `~/.ingot` when that variable
is unset, and maintains the selected Official Profile recipe under `profiles/`.
The recipe pins released plugin modules to exact versions. `setup` never writes
to the current directory and does not create a Runtime.

Create a project-owned recipe in the current directory, or name another
directory explicitly:

```sh
ingot init [DIR] [--profile default|minimal] [--force]
```

`init` also ensures that managed Home exists. Existing project recipes are not
overwritten unless `--force` is present.

Use another managed Home with the global option. It overrides `INGOT_HOME`:

```sh
ingot --home /path/to/home setup
```

An old or non-empty incompatible Home is rejected. The current Core does not migrate the
pre-release `current`, top-level `state`, or old Image manifest layouts.

## Profiles And Configuration Ownership

The profile definitions shipped with this checkout are the authority for the
initial recipe: [`default.toml`](../internal/profiles/default.toml) and
[`minimal.toml`](../internal/profiles/minimal.toml). Both pin every selected
plugin to `v0.1.0`; a newer Core release can ship different exact selections.

| Profile | Selected plugins |
| --- | --- |
| `minimal` | `asset.local`, `http.default`, `model.openai-compatible`, `model.runtime`, `tool.runtime`, `prompt.default`, `session.sqlite`, `agent.default`, `app.backend` |
| `default` | Everything in `minimal`, plus `tool.shell` and `tool.ask` |

Both profiles open a browser application. `minimal` has a Tool Runtime but no
Tool providers. Other official plugins, including approval, script policy,
context compaction, usage tracking, edit tools, and subagents, must be selected
explicitly; being in the plugins repository does not add them to a profile.

```sh
ingot setup --profile minimal
ingot init ./my-agent --profile minimal
# Or build the managed profile directly without creating a project recipe:
ingot build --profile minimal
```

`setup` refreshes the selected managed profile when its shipped recipe changes.
`setup --force` also rewrites `builder.toml` with distribution defaults. Project
recipes created by `init` remain user-owned; updating Core or running `setup`
does not update an existing project's plugin versions.

There are three separate configuration boundaries:

| File | Owner and purpose |
| --- | --- |
| `<home>/builder.toml` | Core Builder settings. The current strict schema contains only `builder_config_version = 1`. |
| `<project>/plugins.toml` | Ordered plugin selection: each entry has `module` and exactly one of an exact `version` or a local `path`. Relative paths are relative to this recipe. |
| `<home>/runtimes/<name>/state/<plugin>/` | Plugin-owned configuration and persistent data, isolated by Runtime. The plugin decides filenames, validation, and whether a change is live or requires restart. |

There is no global `config.toml`, `[plugins.<name>]` configuration envelope,
Builder SDK list, or Core `config` command. Do not put API keys in the recipe or
Builder configuration. See [current file formats](./FILE_FORMATS.md) and the
[plugin documentation](https://github.com/ingot-agent/plugins/tree/main/docs)
for the relevant owner's current schema.

## Core Version And Updates

For an operational upgrade, first read [UPGRADING.md](UPGRADING.md): it covers
stopping all writers, backing up state and workspaces, Image rollback limits,
and recovery into a fresh Managed Home.

Core update checks are always explicit. `version` and `--version` are local
queries. Only the `update` commands below contact GitHub for updates; they
inherit the process HTTP(S) proxy configuration:

```text
ingot --version
ingot version
ingot update --check
ingot update
ingot update --version v0.3.1
ingot update --version v0.3.1 --force
```

`--version` prints a short Core version. `version` prints Core, ingot protocol,
Builder, and target identities. `ingot --json version` additionally exposes
provenance, including the Go version, source revision, and official/modified
flags. These identities evolve independently; the protocol identity is distinct
from the fixed `github.com/ingot-agent/ingot-abi` module version.

Without `--version`, `update` resolves the latest stable Release and never
selects a prerelease. An exact version may select a prerelease. Downgrades and
same-version reinstalls require `--force`; `--check` never changes the binary
and cannot be combined with `--force`. Version and update commands do not read
or write Home.

Before replacement, the updater verifies the archive digest and executes the
candidate to verify its version, official-build flag, source revision, clean
state, and platform target against `release-manifest.json`. Replacement is
serialized and atomic on Unix; Windows keeps a rollback copy until the new
core starts. Core updates do not modify plugins, Images, Runtime definitions,
state, or live Processes.

Release assets also carry GitHub artifact attestations. After downloading an
asset, users with GitHub CLI can verify its workflow provenance:

```sh
gh attestation verify ingot-v0.3.1-linux-amd64.tar.gz \
  --repo ingot-agent/ingot \
  --signer-workflow ingot-agent/ingot/.github/workflows/release.yml
```

## Storage Layout

Project-owned files:

```text
<project>/
  plugins.toml
  plugins.lock
```

Managed machine state:

```text
~/.ingot/
  home.json
  builder.toml
  profiles/
    <profile>.toml
    <profile>.lock
  cache/gomod/
  images/
    catalog.json
    <image-id>/
      manifest.json
      ingot-runtime[.exe]
  runtimes/<name>/
    runtime.json
    state/<plugin>/
    run/
    logs/
  .transactions/
```

`plugins.lock` contains target-neutral resolution facts. The actual target,
Go toolchain, build flags, Runtime ABI, and target graph projection enter each
Image BuildManifest instead. Runtime State belongs to a Runtime, never to an
Image.

## Standard Workflow

```sh
ingot setup
ingot init .
ingot up -- web
```

`up` builds the nearest project recipe, binds the result to the `default`
Runtime, persists argv after `--` as that Runtime's default command, stops any
old Process, and starts the new Process in the foreground. Use `-d` for a
detached Process:

```sh
ingot up -d -- web
ingot logs -f
ingot stop
```

Named Runtimes support parallel debugging without inventing Image tags:

```sh
ingot up work -d -- web
ingot logs work -f
ingot stop work
```

Runtime bindings always contain the concrete Image and Artifact digests. The
name `default` is only the implicit Runtime selected when a lifecycle command
omits its optional Runtime argument; it is not a mutable Image reference.
If `up` cannot stop the old Process, it returns an error and leaves the newly
built Image and desired Runtime binding in place with `restart_required`; it
does not roll the binding back behind a still-running Process.

## Configure The Browser Agent

After `ingot up -d -- web`, open
[http://127.0.0.1:7316/](http://127.0.0.1:7316/). A missing model configuration
is an expected initial state; the browser can start, but model requests require
a configured provider and model.

The following steps target the exact plugin `v0.1.0` modules selected by the
checked-in profiles. Their browser Operation names are under group
`configuration`; select them from the browser's Operation picker.

1. Choose `model.openai-compatible.config`. Add **one initial provider**, an
   absolute HTTP(S) base URL, an API key if required, and the provider's model
   IDs. An empty model list means no local allowlist. This released Operation
   returns `restart_required: true`; run `ingot restart default` and refresh
   the browser before continuing.
2. Choose `model.runtime.config` and set the default provider and model.
   With only one provider, provider selection may be automatic; an actual model
   ID must still be supplied for a model request. Changed defaults return
   `restart_required: true`; run `ingot restart default` again and refresh.
3. Create/select a Session, select its workspace before its binding is fixed,
   and send a message. The shell tool runs in that Session's workspace, not
   necessarily the directory where `ingot up` was invoked.

The released provider list and defaults are captured at Runtime construction;
saving configuration does not update that Process. If adding multiple providers
at once, save an explicit `default_provider` through `model.runtime.config`
**before** the first restart: this version rejects startup with multiple
providers and no default. For a named Runtime, replace `default` in the commands
with its name. No Image rebuild is needed for these configuration changes.

The shipped application's corresponding Operation is `app.backend.config` in
group `configuration`; changed application settings also require a restart.
Follow the Operation's own `restart_required` result. This configuration result
is separate from Core `runtime show`'s binding/generation `restart_required`:
Core does not inspect plugin configuration changes.

Newer plugin source on `main` uses `/model-openai-compatible config`,
`/model-runtime config`, and `/app-webui config`, and its provider/default
configuration can apply live. Those names and hot-update behavior must not be
assumed for the profile's published `v0.1.0` modules. Read plugin docs and
changelogs at the module versions in your recipe/lock.

For direct file editing, stop the Runtime first and follow the relevant
[plugin guide](https://github.com/ingot-agent/plugins/tree/main/docs), then
start it again. Defaults and secrets belong to that specific Runtime; creating
another Runtime does not copy them. Two browser Runtimes cannot listen on the
same address simultaneously; give the second one a different backend port.
The default application is for trusted local, single-user use.

## Project Commands

Recipe-oriented commands search upward from the current directory and use the
nearest `plugins.toml`. Its default lock is the adjacent `plugins.lock`. Use
`-f/--file` and `--lock` for explicit paths, or `--profile` to select a managed
Home profile. `--profile` cannot be combined with `--file` or `--lock`.

```text
ingot project resolve [-f recipe.toml] [--lock recipe.lock]
ingot project status [-f recipe.toml] [--lock recipe.lock]
ingot project show [-f recipe.toml] [--lock recipe.lock]
ingot project generate -o runtime-source [-f recipe.toml] [--lock recipe.lock] [--locked]
ingot build [runtime] [-f recipe.toml] [--lock recipe.lock] [--locked] [--tag name:tag]
ingot up [runtime] [-d] [-f recipe.toml] [--lock recipe.lock] [--locked] [--tag name:tag] [-- argv...]
```

Normal build refreshes a missing or stale lock. `--locked` requires the lock
and all locked source facts to match and never rewrites it. `--tag` additionally
moves the current host target slot after a successful build.

`project generate` applies the same lock refresh and `--locked` rules, loads
and type-checks the complete Component Graph, and writes the generated Runtime
as a standalone `package main` Go module. It does not run `go build`, execute
the Runtime validation check, create an Image, move a tag, or bind a Runtime.
The explicit output directory must be missing or empty and cannot be inside a
local replacement source tree.

The exported module contains `go.mod`, `go.sum`, generated Go files, an exact
`ingot-build-manifest.json`, and copies of local replacements under `dev/` with
relative `replace` directives. Remote modules are not vendored; their exact
versions and sums remain locked by the module files. The build manifest records
the target, Go version, tags, and compiler flags required to reproduce the
expected Image identity.

`build` always binds the resulting Image to exactly one Runtime. The Runtime
defaults to `default`, is created when absent, and is switched when it already
exists. `build` never starts or restarts a Process. Rebuilding the same Image
does not advance Runtime generation. Switching a running Runtime leaves the old
Process alive and reports `restart_required` until it is restarted.

Plugin mutations use the same project selection rules:

```text
ingot plugin ls
ingot plugin show <plugin>
ingot plugin add <module[@query]|path>
ingot plugin rm <plugin>
ingot plugin update <plugin[@query]>
ingot plugin move <plugin> --before <anchor>
ingot plugin move <plugin> --after <anchor>
```

All plugin commands accept `--file`, `--lock`, or `--profile` selectors.
A module query such as `@latest` is resolved to an exact version before it is
written to the recipe. `plugin update` without a query selects `latest`; it does
not update every plugin. `move` requires exactly one of `--before` or `--after`.
`plugin add` recognizes local paths explicitly (`./my-plugin` or an absolute
path on Unix, `.\my-plugin` or an absolute path on Windows).

A successful plugin mutation resolves and commits the recipe/lock change; it
does not rebuild or restart an existing Runtime. Run `ingot up` to use the new
composition. A module resolution can succeed while the Component Graph is
invalid; full dependency/type checks happen during `build` or `project generate`.
Build validation executes the generated native program with `--ingot-check`
using a temporary empty Runtime Home. This checks construction in check mode;
it is not a test of the production Runtime's saved configuration or provider
credentials. Plugin code runs during this check, so only build plugins you trust.

## Plugin Collections

A Collection is a reusable ordered recipe of exact released Plugin modules. It
is an input transformation only: after apply, `plugins.toml` remains the sole
desired-state authority.

```toml
collection_schema = 1
id = "github.com/example/ingot-collections/coding"
version = "v1.0.0"

[metadata]
name = "Coding Essentials"

[[plugins]]
module = "github.com/ingot-agent/plugins/tool-shell"
version = "v0.1.0"
```

```text
ingot collection inspect [--expect-digest sha256:...] <path-or-https-url>
ingot collection plan [-f ...] [--lock ...] [--expect-digest sha256:...] [--accept-order] <source>
ingot collection apply [-f ...] [--lock ...] [--expect-digest sha256:...] [--accept-order] <source>
```

`inspect` does not require an initialized Home. `plan` classifies additions,
satisfied Plugins, version/source conflicts, and order conflicts without
resolving modules. A conflicting plan is valid JSON with `applicable: false`.

Collection order is a strict subsequence constraint, not a contiguous block.
Existing Plugin order is never changed by default. `--accept-order` explicitly
authorizes the deterministic minimum-reversal reorder shown by the plan; it
does not authorize version changes or replacement of Local Path sources.

`apply` replans under the project lock, performs a complete resolve preflight,
and atomically commits `plugins.toml` and `plugins.lock`. Fetch, digest, parse,
conflict, or resolve failure leaves both files unchanged. The current implementation does not keep a
Collection receipt and does not provide Collection remove or update commands.

## Image Commands

```text
ingot image ls
ingot image show <ref>
ingot image verify <ref-or-digest>
ingot image tag <ref-or-digest> <name>:<tag>
ingot image untag <name>:<tag>
ingot image pin|unpin <ref-or-digest>
ingot image rm <digest>
ingot image export <ref-or-digest> [--target os/arch] --output file.ingot-image
ingot image import file.ingot-image [--no-tag]
```

References are `sha256:<64 lowercase hex>`, `name:tag`, or
`name:tag@goos/goarch`. Names and tags are lowercase ASCII. No default
`latest` tag is inserted.

A tag is a mutable multi-target pointer. Runtimes and Processes store concrete
Image and Artifact digests, so moving a tag never changes an existing binding.
Bundles are deterministic ZIP files containing exactly one target variant.
Import validates paths, entry counts, size limits, Build Input identity, target,
and executable digest before committing bytes. Imported native code is not
trusted merely because its checksum is valid.

## Runtime Commands

```text
ingot run <name> <image> [-d] [-- <default-argv>]
ingot runtime create <name> <image> [-- <default-argv>]
ingot runtime ls
ingot runtime show [name]
ingot runtime switch <name> <ref>
ingot runtime rollback [name]
ingot runtime command set [name] -- <argv...>
ingot runtime command clear [name]
ingot runtime rm <name> [--purge]
ingot start [name] [--foreground] [-- <temporary-argv>]
ingot restart [name]
ingot logs [name] [--process <id>] [-f]
```

`switch` updates desired and rollback bindings atomically but does not restart
a live Process. `runtime show` reports `restart_required` when the live
Process differs from the desired Image or Runtime generation. `rollback`
swaps desired and rollback bindings and never copies or interprets State.

Optional Runtime names on lifecycle and inspection commands default to
`default`. `run` remains available for creating a Runtime from an already-built
Image reference; `up` is the normal build-and-restart workflow.

Each Runtime has an isolated Runtime Home. Generated Images acquire
`run/writer.lock` before constructing any Plugin, so standalone and managed
launches enforce the same single-writer contract.

`start` runs detached by default; `start --foreground` attaches to the terminal.
Its argv after `--` applies only to this launch. `run` creates a new Runtime and
runs in the foreground unless `-d` is supplied; its argv becomes that Runtime's
default. `restart` uses the persisted default argv and starts in the background.
`up` without `--` preserves an existing Runtime's default argv. Use
`runtime command set` or `clear` to change that default without rebuilding.

`runtime rm` requires a stopped Runtime and refuses nonempty `state/` or `logs/`
unless `--purge` is supplied. `--purge` permanently deletes that Runtime's state
and logs; it does not delete its Image. Image rollback restores code bindings,
not plugin data: it is not a state backup or a schema downgrade mechanism.

## Process Commands

```text
ingot ps
ingot ps -a
ingot stop [runtime] [--timeout 10s]
ingot stop --process <process-id> [--timeout 10s]
```

Foreground CLI and detached `ingot supervise` processes own Process records,
exit observations, logs, and a token-authenticated IPv4 loopback control
endpoint. `running` means the OS child was spawned; it is not application
readiness. Stop requests graceful termination and does not fall back to a
PID-only force kill.

Runtime states are `stopped`, `starting`, `running`, `stopping`, `unresponsive`,
`orphaned`, `external`, and `failed`. An external standalone writer has no known
actual Image, so switch, mutation, and GC operations fail closed where required.

## Garbage Collection

```sh
ingot gc [--keep-recent N]
```

`--keep-recent` defaults to `3`. GC preserves all tag variants, pins, Runtime desired and rollback Images, live
Process actual Images, and the requested recent unreferenced Images. Missing or
corrupt roots, or any external Runtime writer, abort the sweep without deletion.

## Output And Exit Codes

Commands print concise human-readable output by default. Pass global `--json`
for stable machine-readable output. Foreground `run`, foreground `up`,
`start --foreground`, and raw `logs` reject `--json` because stdout belongs to
the Runtime or log stream.

Usage errors return `2`; domain, I/O, and verification failures return `1`;
foreground runs propagate the Runtime exit code.

## Shell Completion

Cobra generates completion scripts for Bash, Zsh, Fish, and PowerShell:

```sh
ingot completion bash
ingot completion zsh
ingot completion fish
ingot completion powershell
```

Dynamic completion suggests existing Runtime names, Image references, plugins,
and profiles. Completion is read-only: it does not initialize Home, recover
transactions, resolve modules, build Images, or access the network.

## Troubleshooting And Documentation Scope

| Symptom | Check |
| --- | --- |
| `ingot` is not found | Confirm the install directory is on `PATH`; reopen the shell if needed. |
| No project recipe | Run `ingot init .`, select `--file`, or select a managed `--profile`. |
| Module download/toolchain failure | Check `go version`, module availability, credentials, and `GOPROXY`. The Builder disables automatic toolchain downloads. |
| Browser does not open | Read `ingot logs`, check `ingot ps -a`, and verify the listen address is free. A spawned Process is not application readiness. |
| Provider/model unavailable | Configure both provider access and the default model in browser operations; inspect plugin-specific errors. |
| Changed code does not appear | Build the project with `ingot up`; `start` uses the Runtime's existing Image binding. |
| Runtime reports `restart_required` | Inspect `ingot runtime show`, then restart the intended Runtime. |
| Lock is stale with `--locked` | Review the source/recipe change and run `ingot project resolve` or an ordinary build, then review the new lock. |

This guide describes the implemented Core CLI. Plugin configuration, HTTP APIs,
frontend development, and implementation design records are maintained in the
[plugins documentation](https://github.com/ingot-agent/plugins/tree/main/docs).
See the [Core documentation index](./README.md) for the current references and
historical design records; a historical proposal is not a supported API.
