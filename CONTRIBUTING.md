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
- Do not report vulnerability details in a public issue. Follow
  [SECURITY.md](./SECURITY.md) for the current reporting channel and its
  availability; do not assume a private channel has already been enabled.

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

## Repository ownership and prerequisites

| Change | Repository |
| --- | --- |
| CLI, Builder, graph resolution, file formats, Images, Runtime/Process management, shipped profile selection | [ingot](https://github.com/ingot-agent/ingot) (this repository) |
| Official plugin implementations, configuration, HTTP/UI behavior, frontend assets, and plugin design records | [plugins](https://github.com/ingot-agent/plugins) |
| Replaceable agent/domain capability contracts | [sdk](https://github.com/ingot-agent/sdk) |
| Fixed Component constructor and host ABI contracts | [ingot-abi](https://github.com/ingot-agent/ingot-abi) |

Use Go 1.24.2 or newer as declared by [`go.mod`](./go.mod), Git, and a C compiler
for race-enabled tests. Core builds do not require Node or a checkout of the
plugins repository. Released-profile smoke tests need network/module-cache
access to the exact plugin versions shipped in `internal/profiles/`.

```sh
git clone https://github.com/ingot-agent/ingot.git
cd ingot
GOWORK=off go build -o ingot ./cmd/ingot
./ingot --help
```

Read [AGENT.md](./AGENT.md) for repository change discipline and the
[documentation index](./docs/README.md) for current references. Historical design
records explain decisions and may include superseded or unimplemented behavior;
verify contracts against code and tests before implementing a proposal.

Use the [current architecture/source map](./docs/ARCHITECTURE.md) to locate
implementation boundaries. Maintainers should follow [RELEASE.md](./RELEASE.md)
for dependency coordination, release checks, and publication. Changes affecting
persisted state must update [upgrade and recovery instructions](./docs/UPGRADING.md).

## Development workflow

### 1. Create a branch

Never commit or push directly to `main`. Start from an up-to-date `main` and use
a focused branch. Inspect `git status` and preserve existing uncommitted work
before switching branches:

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

Targeted tests are useful during development. Before every commit, follow
[AGENT.md](./AGENT.md): run the complete race-enabled suite for every Go module
in this repository with workspace resolution disabled. The repository currently
contains the Core module; CI runs `GOWORK=off go test -race ./...` for it and a
separate released-profile resolve/build smoke job. The all-module form is:

```bash
while IFS= read -r -d '' mod_file; do
  module_dir="$(dirname "$mod_file")"
  (
    cd "$module_dir"
    GOWORK=off go test -race ./...
  ) || exit 1
done < <(find . -type f -name go.mod -not -path '*/vendor/*' -print0 | sort -z)
git diff --check
```

For the current single-module checkout, PowerShell users can run:

```powershell
$env:GOWORK = 'off'
go test -race ./...
if ($LASTEXITCODE -ne 0) { throw 'Core tests failed' }
git diff --check
```

The race detector needs a supported platform and a C compiler. Run with a
compatible compiler/CGO configuration or use the Linux CI environment; passing
without `-race` does not satisfy this requirement. Profile changes must also
pass the released-profile smoke workflow in
[`.github/workflows/test.yml`](./.github/workflows/test.yml).

If the complete suite cannot run or does not pass, document the blocker clearly
and do not present the change as ready to merge.

### 4. Commit clearly

Use concise, imperative commit subjects following the style already used in
the repository:

```text
feat(builder): support a general component rule
fix(cli): preserve runtime command arguments
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

Official plugins live in the standalone
[`ingot-agent/plugins`](https://github.com/ingot-agent/plugins) repository. Each
plugin is an independent Go module with its own `go.mod`, `ingot.plugin.toml`,
component implementation, and tests, and must satisfy the same manifest and
component rules as any third-party plugin.

Start with the [plugin development documentation](https://github.com/ingot-agent/plugins/tree/main/docs).
Keep plugin setup, state schema, Operations, and frontend instructions alongside
the plugin implementation. A Component constructor is
`New(context.Context, Dependencies) (Exports, ingotabi.Cleanup, error)`;
configuration is owned by the plugin through its Runtime state scope, not
injected through a Builder-owned `Config` parameter.

Adding a plugin to an official profile is a separate product decision. Profile
membership may select the plugin, but it must not grant different Builder or
runtime behavior.

## Documentation

Keep commands and examples executable and consistent with current behavior.
When editing a document that has both English and Chinese versions, update both
unless the pull request explicitly explains why only one version changes.

Keep Core workflows and file-format references here. Put plugin user/developer
guides in the plugins repository and link to them from Core; keep SDK and ABI
contract documentation in their respective repositories. When relocating a
record, preserve its historical status, add it to the destination index, update
all inbound and outbound links, and identify a current usage reference. Do not
turn a historical proposal into an apparent guarantee by removing its context.

Before submitting documentation, check every command against current `--help`
and command handlers, check schemas against parsers/tests, resolve local links,
and run `git diff --check`. Use placeholder credentials and clearly label
illustrative versions. Do not publish personal paths, saved model keys, Runtime
state, or local test artifacts.

## License

By submitting a contribution, you agree that it may be distributed under the
repository's [MIT License](./LICENSE).
