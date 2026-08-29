# Repository Instructions

## Project model

ingot composes Go plugins into a statically wired Component Graph and builds
that graph as an immutable native Runtime Image. Build time owns plugin
discovery, contract validation, dependency resolution, ordering, code
generation, compilation, and image verification. Runtime code instantiates the
already resolved graph and manages its lifecycle.

Keep the core boundaries clear:

- A **Plugin** is the unit of distribution, versioning, configuration, and user
  operations.
- A **Component** is a construction and lifecycle node in the graph.
- A **Capability** is a typed contract exchanged between components.

Do not move plugin-specific implementations into the Builder or runtime core.

## Plugin equality is non-negotiable

All plugins are equal. Official, bundled, default, local, and third-party
plugins must pass through the same discovery, validation, graph resolution,
code generation, configuration, and lifecycle paths.

- Never branch on a plugin name, module path, component name, package path, or
  official-plugin membership to change core behavior.
- Profiles may select a set of plugins, but selected plugins receive no special
  treatment.
- New behavior must be expressed as a general manifest rule, typed contract,
  or Builder feature available to every plugin.
- Tests for core behavior should use arbitrary or synthetic plugin identities
  so they do not accidentally encode privileged plugins.

## Change discipline

- Treat current code and tests as the source of truth. Design documents may
  describe future work; do not present unimplemented designs as current
  behavior.
- Preserve deterministic resolution, generated wiring without reflection,
  immutable images, startup validation, atomic activation, and rollback safety.
- Keep file formats strict and update affected documentation and examples when
  their behavior changes.
- Keep changes scoped. Do not modify unrelated files or discard existing work.

## Git workflow

- Never edit, commit, or push directly on `main`. Create or switch to a focused
  working branch before changing tracked files, and submit changes through a
  pull request.
- Inspect the working tree before switching branches and preserve all existing
  uncommitted work.
- Do not create commits, push branches, force-push, rewrite shared history, or
  open pull requests unless the user explicitly requests that action.
- Keep each branch and pull request focused on one coherent change.

## Required validation

Before every commit, run the complete race-enabled test suite for every Go
module in the repository, even when the change appears isolated. Use the same
module discovery and workspace-independent execution as CI:

```bash
while IFS= read -r -d '' mod_file; do
  module_dir="$(dirname "$mod_file")"
  (
    cd "$module_dir"
    GOWORK=off go test -race ./...
  )
done < <(find . -type f -name go.mod -not -path '*/vendor/*' -print0 | sort -z)
```

Targeted tests are useful while iterating, but they do not replace the complete
suite. Also run `git diff --check` before committing. If the required validation
cannot run or does not pass, report the blocker explicitly and do not describe
the change as ready to commit.
