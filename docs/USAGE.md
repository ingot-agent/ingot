# ingot M2 Usage Guide

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
force option:

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

To build the core from source instead, Go 1.24 or newer is required:

```sh
GOWORK=off go build -o ingot ./cmd/ingot
```

After either installation method, initialize Home explicitly:

```sh
ingot init
```

`init` initializes schema v2 in `INGOT_HOME`, or `~/.ingot` when that variable
is unset, and maintains the selected Official Profile recipe under `profiles/`.
The recipe pins released plugin modules to exact versions. It never writes to
the current directory and does not create a Runtime.

Create a project-owned recipe only with an explicit directory:

```sh
ingot project init . [--profile default|minimal] [--force]
```

Use another managed Home with a global option before the command. This option
overrides `INGOT_HOME`:

```sh
ingot --home /path/to/home init
```

An old or non-empty incompatible Home is rejected. M2 does not migrate the
pre-release `current`, top-level `state`, or old Image manifest layouts.

## Core Version And Updates

Core update checks are always explicit. These commands are the only update
operations that contact GitHub, and they inherit the process HTTP(S) proxy
configuration:

```text
ingot --version
ingot version
ingot update --check
ingot update
ingot update --version v0.3.1
ingot update --version v0.3.1 --force
```

`--version` prints a short human-readable core version. `version` emits JSON
with the core version and provenance, build protocol version, and Builder
version; these identities evolve independently.

Without `--version`, `update` resolves the latest stable Release and never
selects a prerelease. An exact version may select a prerelease. Downgrades and
same-version reinstalls require `--force`; `--check` never changes the binary
and cannot be combined with `--force`. Version and update commands reject
`--home` because they do not read or write Home.

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
ingot init
ingot project init .
ingot build --tag acme/coding-agent:1.0.0
ingot runtime create work --image acme/coding-agent:1.0.0 -- web
ingot runtime start work
ingot runtime logs work --follow
ingot stop work
```

The Docker-style convenience command creates and runs in the foreground by
default. Pass `-d` (or `--detach`) to start it in the background:

```sh
ingot run --name work -d acme/coding-agent:1.0.0 -- web
```

## Project Commands

All recipe-oriented commands default to `./plugins.toml` and its adjacent
`plugins.lock`. They never search parent directories or fall back to Home.
The Ingot-managed official recipes under Home are selected explicitly with
`--use`, as the installers do.

```text
ingot resolve [--use recipe.toml] [--lock recipe.lock]
ingot build [--use recipe.toml] [--lock recipe.lock] [--locked] [--tag name:tag]
ingot status [--use recipe.toml] [--lock recipe.lock]
ingot inspect [--use recipe.toml] [--lock recipe.lock] [plugin]
```

Normal build refreshes a missing or stale lock. `--locked` requires the lock
and all locked source facts to match and never rewrites it. `--tag` moves only
the current host target slot after a successful build.

Plugin mutations use the same project selection rules:

```text
ingot plugin list|inspect ... [--use ...] [--lock ...]
ingot plugin add module@version [--use ...] [--lock ...]
ingot plugin add --path ../plugin [--use ...] [--lock ...]
ingot plugin remove|update|reorder ... [--use ...] [--lock ...]
```

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
ingot collection plan [--use ...] [--lock ...] [--expect-digest sha256:...] [--accept-order] <source>
ingot collection apply [--use ...] [--lock ...] [--expect-digest sha256:...] [--accept-order] <source>
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
conflict, or resolve failure leaves both files unchanged. M4 does not keep a
Collection receipt and does not provide Collection remove or update commands.

## Image Commands

```text
ingot image list
ingot image inspect <ref>
ingot image verify <ref-or-digest>
ingot image tag <ref-or-digest> <name>:<tag>
ingot image untag <name>:<tag>
ingot image pin|unpin <ref-or-digest>
ingot image remove <digest>
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
ingot runtime create <name> --image <ref> [-- <default-argv>]
ingot runtime list
ingot runtime inspect <name>
ingot runtime switch <name> <ref>
ingot runtime rollback <name>
ingot runtime command set <name> -- <argv...>
ingot runtime command clear <name>
ingot runtime run <name> [-- <temporary-argv>]
ingot runtime start <name> [--timeout 30s] [-- <temporary-argv>]
ingot runtime restart <name> [--timeout 30s]
ingot runtime logs <name> [--process <id>] [--follow]
ingot runtime delete <name> [--purge]
```

`switch` updates desired and rollback bindings atomically but does not restart
a live Process. `runtime inspect` reports `restart_required` when the live
Process differs from the desired Image or Runtime generation. `rollback`
swaps desired and rollback bindings and never copies or interprets State.

Each Runtime has an isolated Runtime Home. Generated Images acquire
`run/writer.lock` before constructing any Plugin, so standalone and managed
launches enforce the same single-writer contract.

## Process Commands

```text
ingot ps
ingot stop <runtime> [--timeout 10s]
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

GC preserves all tag variants, pins, Runtime desired and rollback Images, live
Process actual Images, and the requested recent unreferenced Images. Missing or
corrupt roots, or any external Runtime writer, abort the sweep without deletion.

## Output And Exit Codes

Commands emit stable JSON objects except foreground Runtime stdio and raw log
streaming. Usage errors return `2`; domain, I/O, and verification failures
return `1`; foreground runs propagate the Runtime exit code.
