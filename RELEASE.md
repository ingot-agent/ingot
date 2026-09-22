# Releasing Core

This is the maintainer procedure for publishing `github.com/ingot-agent/ingot`.
It describes the checked-in workflows, not an assertion that a candidate or a
historical tag has passed every step. User upgrade and recovery instructions
are in [docs/UPGRADING.md](docs/UPGRADING.md).

## Release inputs and version authorities

| Input | Authority in this repository | Meaning |
| --- | --- | --- |
| Core release line | [`internal/buildinfo/buildinfo.go`](internal/buildinfo/buildinfo.go) | `CoreVersion` is `X.Y.Z-dev` in source; the workflow embeds `X.Y.Z` or `X.Y.Z-prerelease` |
| Composition protocol | [`internal/builder/resolve.go`](internal/builder/resolve.go), `DefaultIngotVersion` | Version matched by plugin manifest `ingot` ranges |
| Builder identity | Same file, `DefaultBuilderVersion` | Builder identity entering locks and Image build inputs |
| Fixed host ABI | Same file, `IngotABIModulePath` / `IngotABIVersion` | Exact ABI module version; a Go MVS upgrade away from it is rejected |
| Shipped compositions | [`internal/profiles/default.toml`](internal/profiles/default.toml), [`minimal.toml`](internal/profiles/minimal.toml) | Ordered exact released plugin modules |
| Core build toolchain | [`go.mod`](go.mod) and `actions/setup-go` in workflows | Core declares Go 1.24.2; plugin dependencies can require more for Image builds |
| Persistent formats | [FILE_FORMATS.md](docs/FILE_FORMATS.md), [UPGRADING.md](docs/UPGRADING.md) | Independent format versions and migration limitations |

At this source revision the Core development line is `0.3.2-dev`, composition
protocol is `0.3.0`, Builder identity is `0.3.1`, ABI is `v0.1.0`, and both
profiles select plugin `v0.1.0` modules. These are source facts, not a promise
that a same-numbered Core Release exists or that all those repositories are
released together. Read these files at the intended release commit; do not copy
this snapshot into a future release without rechecking it.

The SDK is an ordinary transitive Go module in generated compositions. Core's
own `go.mod` has neither the SDK nor the runtime ABI as a direct dependency;
changing it cannot update the generated ABI pin. Each plugin's `go.mod`, the
resolved graph, and the lock establish the actual SDK version. Current plugin
source documentation can describe changes newer than a profile's published
module. Inspect the exact released module source when preparing release notes.

## Prerequisites and dependency release order

Before creating a release tag:

1. Enable immutable GitHub Releases and protect creation of `v*` tags with a
   repository ruleset restricted to the authorized release process/maintainers.
   The workflow checks tag contents and ancestry but cannot configure these
   repository policies. Verify Actions permissions allow the publish job's
   `contents`, `id-token`, `attestations`, and `artifact-metadata` writes.
   Enable GitHub private vulnerability reporting for each published repository
   and verify that its private report form is reachable. As recorded in
   [SECURITY.md](SECURITY.md), that channel was disabled at the 2026-09-22 audit;
   publishing this guide does not enable it or establish an alternative private
   contact channel.
2. Merge the reviewed candidate through a pull request to `main`. Both Core
   tests and the released-profile smoke job must pass at the candidate's actual
   code. Review changes after a merge/rebase, not only an earlier PR revision.
3. Release required ABI/SDK contract changes in their own repositories first.
   Coordinate ABI changes with the Core pin, generated host implementations,
   graph checks, and affected plugin dependencies. Use the
   [ABI release procedure](https://github.com/ingot-agent/ingot-abi/blob/main/RELEASE.md)
   and [SDK migration notes](https://github.com/ingot-agent/sdk/blob/main/docs/MIGRATIONS.md).
4. Release affected plugins following the
   [plugins release procedure](https://github.com/ingot-agent/plugins/blob/main/RELEASE.md).
   Plugin modules use subdirectory-prefixed tags such as
   `tool-shell/v0.1.0`; selecting `github.com/ingot-agent/plugins/tool-shell@v0.1.0`
   is distinct from a root repository tag. Confirm generated frontend assets
   are included by the plugin release process where applicable.
5. Update the Core profile files to those actual module versions. Verify the
   modules can be fetched from a clean cache with workspace overrides disabled.
   Local `replace` directives or a monorepo `go.work` are not release evidence.
6. Document changed commands, persistent formats, plugin state compatibility,
   upgrade prerequisites, rollback limitations, platform support, and known
   issues. Include the exact Core, profile module, ABI, and resolved SDK
   relationship in release notes when it changes. Do not imply an Image
   rollback restores database state.

Use read-only remote checks to confirm a proposed version is actually available
and the Core tag is unused. The following versions illustrate syntax; substitute
the candidate's real version set:

```sh
git ls-remote --tags https://github.com/ingot-agent/ingot.git refs/tags/v0.3.2
git ls-remote --tags https://github.com/ingot-agent/ingot-abi.git refs/tags/v0.1.0
git ls-remote --tags https://github.com/ingot-agent/plugins.git refs/tags/tool-shell/v0.1.0
GOWORK=off GOTOOLCHAIN=local go mod download -json github.com/ingot-agent/ingot-abi@v0.1.0
GOWORK=off GOTOOLCHAIN=local go mod download -json github.com/ingot-agent/plugins/tool-shell@v0.1.0
```

A tag's presence is not a compatibility test. The full composition smoke below
must still succeed. If the public proxy has not indexed a newly published
module, wait or diagnose proxy/module availability; do not publish Core with an
unfetchable default composition or replace it with a local source path.

## Pre-tag verification against released modules

Run the complete race-enabled suite for every Go module as required by
[AGENT.md](AGENT.md), then `git diff --check`. The current repository contains
one Core Go module:

```sh
GOWORK=off go test -race ./...
git diff --check
```

The race detector requires a supported host and C compiler. Passing a suite
without `-race` does not satisfy the release requirement. A Windows-specific
failure must be investigated or explicitly scoped; Linux CI passing does not
establish Windows behavior.

Next run the equivalent of the PR workflow's fresh-Home default profile build,
and also validate `minimal`. This Bash example writes only to a newly created
temporary directory; retain it as evidence until review completes:

```bash
set -euo pipefail
release_check=$(mktemp -d)
export GOWORK=off GOTOOLCHAIN=local
go build -trimpath -o "$release_check/ingot" ./cmd/ingot
"$release_check/ingot" version --json > "$release_check/core-version.json"
go version > "$release_check/go-version.txt"
cd "$release_check"
for profile in default minimal; do
  profile_home="$release_check/home-$profile"
  project_dir="$release_check/project-$profile"
  "$release_check/ingot" --home "$profile_home" setup --profile "$profile"
  "$release_check/ingot" --home "$profile_home" init "$project_dir" --profile "$profile"
  "$release_check/ingot" --home "$profile_home" project resolve \
    --file "$project_dir/plugins.toml" --lock "$project_dir/plugins.lock"
  "$release_check/ingot" --home "$profile_home" build smoke \
    --file "$project_dir/plugins.toml" --lock "$project_dir/plugins.lock" \
    --locked --tag "local/ingot:release-$profile"
  "$release_check/ingot" --home "$profile_home" runtime show smoke --json \
    > "$release_check/runtime-$profile.json"
  "$release_check/ingot" --home "$profile_home" image verify "local/ingot:release-$profile"
done
```

Save the exact recipe/lock, `version --json`, toolchain identity, and test output.
The temporary directory must be outside a tree with an ancestor `go.work`.
The current Builder scans the CLI working directory's ancestors for local
versionless `replace` entries itself, even when Go subprocesses receive
`GOWORK=off`. Changing only `--home` or `--file` does not disable this developer
override. The `cd` above is intentional; inspect the resulting locks and reject
any local `[[replacements]]` for this released-module check. Inspect the locks'
selected SDK/ABI and plugin versions. Build checks execute
native plugin constructors in check mode with empty temporary state. They do
not prove HTTP application readiness, provider credentials, state migration,
model behavior, or tool safety. Use an isolated workspace and test account for
application smoke; verify browser startup and shutdown, configuration, one
representative request, persistence across restart, and recovery from the
pre-upgrade state backup for affected plugins. Record any behavior that cannot
be exercised without external credentials.

The Core release matrix builds six binaries, but a native Image build runs on
its build host; Core cross-compilation is not evidence that every selected
plugin works on all six targets. Verify each platform you promise in the release
notes, including dependencies and installed tool requirements.

## Tag and workflow behavior

After the reviewed commit is on `main`, fetch and verify the exact commit and
unused tag. `vX.Y.Z` and canonical SemVer prereleases such as `vX.Y.Z-rc.1` are
accepted. Build metadata (`+...`) and numeric prerelease identifiers with
leading zeros are rejected. Source `CoreVersion` must be `X.Y.Z-dev` even when
tagging `vX.Y.Z-rc.1`.

Tag creation and pushing are the actual publication trigger. Maintainers should
run these only when intentionally publishing an approved candidate; replace
the example version and commit with reviewed values:

```sh
git fetch origin main --tags
git status --short
git merge-base --is-ancestor <reviewed-commit> origin/main
git tag -a vX.Y.Z <reviewed-commit> -m "Release vX.Y.Z"
git push origin refs/tags/vX.Y.Z
```

[`release.yml`](.github/workflows/release.yml) then performs:

| Job | Actual checks and outputs |
| --- | --- |
| `validate` | Tag syntax, source development line, tag commit ancestry on `origin/main`, full Core race suite |
| `build` | Linux/macOS/Windows × amd64/arm64, `CGO_ENABLED=0`, trimmed paths, official version/revision/clean metadata embedded with linker flags |
| `package` | Deterministic archives via `internal/releasetool`, checksums, release manifest, Unix and PowerShell installers |
| `smoke` | Linux, macOS and Windows hosted runner: verify native archive checksum, extract, and run `--version` and `version --json` with expected version/revision/official/target |
| `publish` | Attest every asset, then create a GitHub Release with generated notes; stable releases become latest, prereleases use `--prerelease --latest=false` |

The workflow does **not** create a draft awaiting manual publication. Pushing a
valid tag can publish automatically. It does **not** run the released-profile
build job from [`test.yml`](.github/workflows/test.yml), exercise all six
executables natively, install through the scripts, or perform an application
readiness check. Those gaps are why pre-tag and post-publish checks are listed
explicitly here.

## Assets and installer validation

Expect six platform archives named
`ingot-vVERSION-OS-ARCH.tar.gz` (Unix) or `.zip` (Windows), each containing the
Core executable and `LICENSE`. Also expect `VERSION`, `release-manifest.json`,
`checksums.txt`, `install.sh`, and `install.ps1`. The source commit timestamp
controls archive timestamps. `PackCoreRelease` requires an empty/nonexistent
output directory and every platform binary; missing files fail packaging.

For a local packaging rehearsal, build all six platform binaries using the
workflow's exact flags, then run the packager from the repository root:

```sh
GOWORK=off go run ./internal/releasetool \
  --version X.Y.Z --commit <full-40-character-commit> \
  --source-date <commit-unix-timestamp> \
  --input <platform-binary-directory> --output <new-release-directory>
```

A locally stamped `official=true` is not release provenance. Verify distributed
assets against their SHA-256 entries and GitHub attestations; use the manifest
to verify the source revision. For an actual downloaded asset:

```sh
gh attestation verify ingot-vVERSION-linux-amd64.tar.gz \
  --repo ingot-agent/ingot \
  --signer-workflow ingot-agent/ingot/.github/workflows/release.yml
```

After publication, validate the release-page assets and test both installers
using an explicit version and isolated destination. For example, after
downloading the published `install.sh`/`install.ps1`, replace `vX.Y.Z` with that
release:

```bash
install_check=$(mktemp -d)
sh ./install.sh --version vX.Y.Z --bindir "$install_check/bin"
"$install_check/bin/ingot" version --json
"$install_check/bin/ingot" update --check --version vX.Y.Z
```

```powershell
$installCheck = Join-Path ([IO.Path]::GetTempPath()) ('ingot-install-' + [guid]::NewGuid())
& ./install.ps1 -Version vX.Y.Z -BinaryDir 'bin' -DestDir $installCheck
$installedBinary = Join-Path $installCheck 'bin/ingot.exe'
& $installedBinary version --json
if ($LASTEXITCODE -ne 0) { throw 'Installed Core identity check failed' }
& $installedBinary update --check --version vX.Y.Z
if ($LASTEXITCODE -ne 0) { throw 'Release update check failed' }
```

The Windows `-DestDir` option stages the executable without changing user PATH.
Verify this path against the script if installer layout changes. Repeat on the
platforms being supported, then run a fresh released-profile build with the
installed binary. Inspect that `version --json` reports the intended commit,
`official: true`, `modified: false`, and correct target. Confirm a stable release
is selected by `update --check` and a prerelease is available only by explicit
version selection. The updater validates manifest/archive/identity; it does not
verify the GitHub attestation itself.

## Failure handling and completion

| Failure | Response |
| --- | --- |
| Invalid tag/source line or commit outside main | Stop publication; correct source via review and select an unused release version. Do not force-move a public tag. |
| Released module missing or ABI/MVS incompatibility | Publish/fix the dependency, update the profile/pin as appropriate, and rerun the clean-cache composition checks. |
| Tests, platform build, package or smoke fails | Keep the run evidence. Fix code through a PR; do not manually upload unverified replacement binaries. |
| Transient infrastructure failure before any Release exists | Confirm commit, tag and inputs are unchanged; rerun the failed workflow after diagnosis. |
| Attestation or publish failure | Inspect the run and Release page first. Determine whether an immutable Release already exists before retrying. |
| Release exists with wrong behavior/assets/metadata | Preserve published identities and explain the issue. Publish a corrected version; never retag or replace immutable assets. |
| Upgrade exposes state incompatibility | Document the affected versions and restore using a pre-upgrade backup; Image rollback alone cannot reverse a migration. |

Before announcing completion, verify downloads, provenance, installer results,
profile build evidence, application smoke scope, upgrade/rollback notes, and
the security-reporting entry point in [SECURITY.md](SECURITY.md). Review the
generated GitHub release notes for completeness; generated commit summaries do
not explain compatibility on their own. In a following reviewed change, advance
the development line when beginning the next release cycle.

## Verification snapshot: 2026-09-22

This records documentation verification on Windows/amd64 with Go 1.26.3 and a
source-built `0.3.2-dev` Core (`official: false`). It is not a release approval,
an all-platform result, or a claim that the minimum supported Go version was
tested. The working tree contained documentation edits.

- Both checked-in released profiles, `default` and `minimal`, resolved, built
  with `--locked`, and passed `image verify` in separate fresh Homes. CLI runs
  were outside the development workspace. Both locks contained no local
  replacements and selected released ABI `v0.1.0`, SDK `v0.2.9`, and the
  profiles' exact plugin `v0.1.0` modules.
- The PowerShell cold-backup/recovery workflow was exercised with a native
  released-profile Image: create/start an isolated Runtime, verify `/api/state`
  responds, stop registered writers, copy the Home, export/import the Image,
  create a fresh Runtime, restore `state/`, start and verify the recovered
  application's `/api/state`, then stop both Runtimes. An unused loopback port
  and temporary directories were used. No user credentials, model requests,
  populated Session migrations, external workspaces, or cross-machine recovery
  were tested. The Bash commands were reviewed against the implementation but
  were not executed on Linux/macOS in this check.
- `go test -race` passed for `internal/cli`, `internal/managedruntime`,
  `internal/process`, and `internal/release`. The same run failed in
  `internal/coreupdate` with `sync ...: Access is denied` on temporary staged
  archive/candidate files. The five failing tests were
  `TestExtractZipExecutable`,
  `TestUpdateDownloadsExactAssetAndReplacesBinary`,
  `TestExactDowngradeRequiresForce`,
  `TestCandidateIdentityMismatchDoesNotReplaceBinary`, and
  `TestReplacementFailureCleansCandidateAndLeavesTarget`.
- All four PowerShell code blocks in this guide and `docs/UPGRADING.md` passed
  PowerShell parser validation, and `git diff --check` passed. Real installer
  replacement/publication, all-module race tests, and release attestations were
  not exercised by this documentation task.

The updater failures remain a release-validation blocker for Windows until
fixed or otherwise resolved with evidence. GitHub private vulnerability
reporting also needs activation as documented in [SECURITY.md](SECURITY.md).
Follow the full procedure above at the eventual release commit.
