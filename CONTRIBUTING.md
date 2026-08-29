# Contributing to ingot

[**English**](./CONTRIBUTING.md) · [**中文**](./docs/CONTRIBUTING.zh.md)

Thank you for helping improve ingot. Contributions of code, tests,
documentation, bug reports, and design feedback are welcome.

## Before you start

- Search existing issues and pull requests before opening a new one.
- Small fixes and documentation improvements can go directly to a pull
  request.
- Discuss large features, architecture changes, new file-format behavior, and
  breaking changes with the maintainers before investing in an implementation.
- Do not report security vulnerabilities in a public issue. Use the
  repository's private security reporting channel, or contact a maintainer
  privately if that channel is unavailable.

## Project principles

Every contribution must preserve the core properties of ingot:

- **Plugins are equal.** Core behavior must never depend on a particular plugin
  name, module path, package path, or official-plugin status.
- **Composition happens at build time.** The Builder resolves and validates a
  static graph and generates ordinary Go wiring; the runtime does not discover
  or dynamically load plugins.
- **Builds are deterministic and traceable.** Ordering, canonical inputs,
  locked sources, image identity, and artifact verification must remain stable.
- **Images are immutable.** Activation, rollback, recovery, and garbage
  collection must preserve image integrity.

If a feature needs new Builder behavior, express it as a general manifest rule,
typed contract, or mechanism available to every plugin. Do not add a special
case for the plugin that first needs it.

## Development workflow

### 1. Create a branch

Never commit or push directly to `main`. Start from an up-to-date `main` and use
a focused branch:

```sh
git switch main
git pull --ff-only
git switch -c feat/short-description
```

Common prefixes include `feat/`, `fix/`, `docs/`, `test/`, and `refactor/`.

### 2. Make a focused change

- Keep a pull request limited to one coherent purpose.
- Follow existing package boundaries and local code style.
- Add or update tests for changed behavior, including failure cases.
- Update user documentation, examples, and design documents affected by the
  change.
- Do not include secrets, local configuration, IDE metadata, build artifacts,
  or unrelated formatting changes.

Format every changed Go file with `gofmt`:

```sh
gofmt -w path/to/changed_file.go
```

### 3. Run the complete test suite

Targeted tests are useful during development, but every module must pass with
the race detector before a commit is considered ready. Run the same module
discovery used by CI from the repository root:

```bash
while IFS= read -r -d '' mod_file; do
  module_dir="$(dirname "$mod_file")"
  (
    cd "$module_dir"
    GOWORK=off go test -race ./...
  )
done < <(find . -type f -name go.mod -not -path '*/vendor/*' -print0 | sort -z)

git diff --check
```

Running only `go test ./...` at the repository root is not sufficient because
the plugins are independent nested Go modules. If the complete suite cannot
run or does not pass, document the blocker clearly and do not present the
change as ready to merge.

### 4. Commit clearly

Use concise, imperative commit subjects following the style already used in
the repository:

```text
feat(builder): support a general component rule
fix(app-cli): preserve cancellation errors
docs: clarify plugin composition
```

Keep commits reviewable and avoid mixing unrelated changes. Do not rewrite
shared branch history without coordinating with other contributors.

### 5. Open a pull request

Open the pull request against `main` and include:

- what changed and why;
- relevant issue links;
- the tests and validation performed;
- user-visible or compatibility impact;
- any known limitations or follow-up work.

All required CI checks must pass. Address review comments with additional
commits when practical; avoid force-pushing after review has started unless it
has been coordinated with the reviewers.

## Code and test expectations

- Write idiomatic Go and keep exported identifiers accurately documented.
- Preserve `context.Context` cancellation and deadlines across blocking calls.
- Wrap errors so callers can continue to use `errors.Is` and `errors.As`.
- Keep component construction and cleanup deterministic; cleanup runs in
  reverse creation order.
- Protect strict parsing and validation behavior with positive and negative
  tests.
- Prefer tests that exercise public behavior. Core plugin tests should use
  arbitrary or synthetic identities rather than relying on official plugins.
- When behavior depends on order or concurrency, assert that behavior
  explicitly and run the race detector.

## Contributing a plugin

An official plugin under `plugins/` is an independent Go module with its own
`go.mod`, `ingot.plugin.toml`, component implementation, and tests. A new plugin
must satisfy the same manifest and component rules as any third-party plugin.

Adding a plugin to an official profile is a separate product decision. Profile
membership may select the plugin, but it must not grant different Builder or
runtime behavior.

## Documentation

Keep commands and examples executable and consistent with current behavior.
When editing a document that has both English and Chinese versions, update both
unless the pull request explicitly explains why only one version changes.

## License

By submitting a contribution, you agree that it may be distributed under the
repository's [MIT License](./LICENSE).
