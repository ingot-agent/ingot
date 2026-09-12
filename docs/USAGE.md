# ingot M2 Usage Guide

> English version · [中文版](./USAGE.zh.md)

ingot separates project recipes, immutable Images, persistent Runtimes, and
individual Processes. There is no global active image and unknown commands are
never dispatched implicitly.

## Install And Initialize

Requires Go 1.24 or newer.

```sh
go build -o ingot ./cmd/ingot
./ingot init
```

`init` initializes schema v2 in `~/.ingot`, materializes the official plugin
bundle there, and maintains the selected official recipe under
`~/.ingot/profiles/`. It never writes to the current directory and does not
create a Runtime.

Create a project-owned recipe only with an explicit directory:

```sh
ingot project init . [--profile default|minimal] [--force]
```

Use another managed Home with a global option before the command:

```sh
ingot --home /path/to/home init
```

An old or non-empty incompatible Home is rejected. M2 does not migrate the
pre-release `current`, top-level `state`, or old Image manifest layouts.

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
  bundled-plugins/
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

The Docker-style convenience command creates and starts in one step:

```sh
ingot run --name work --detach acme/coding-agent:1.0.0 -- web
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
