# Upgrading, backup, and recovery

This guide describes the current schema-v2 Managed Home and manifest-v3 Images.
Read the release notes for both the Core and every changed plugin before an
upgrade. Plugin state compatibility is owned by each plugin. The Core does not
migrate plugin databases or reverse plugin state changes.

For first installation and command syntax, see [USAGE.md](USAGE.md). For
release coordination, see [RELEASE.md](../RELEASE.md).

## What an upgrade changes

| Operation | What changes | What needs a separate action |
| --- | --- | --- |
| `ingot update` | The Core executable being invoked | Existing Images, project recipes, plugin state, and running Processes |
| `ingot setup --profile default` | The selected managed profile recipe from the installed Core; missing default builder configuration | Project-owned recipes and existing Runtime bindings |
| `ingot plugin update MODULE@VERSION` | The selected project recipe and resolution lock after successful resolution | Building an Image and restarting the Runtime |
| `ingot build NAME` | An immutable Image and NAME's desired binding; previous desired binding becomes the rollback binding when it changes | A live Process continues using its actual Image until restarted |
| `ingot up NAME` | Build, bind, and restart NAME | State remains in the same Runtime Home |
| `ingot runtime rollback NAME` | Desired and rollback Image bindings are swapped | Restarting a live Process, restoring state, and restoring the project recipe |

The Core executable and its generated runtime executables are different
programs. Updating the Core does not patch code already compiled into an Image.
Moving a tag also does not update Runtimes previously created from that tag.

`setup` may replace local edits to `profiles/<name>.toml` with the profile
shipped by the installed Core. `setup --force` also replaces `builder.toml` with
distribution defaults. Do not use `--force` as a general upgrade command. Keep
custom compositions in project-owned `plugins.toml` files.

## Record the deployment before changing it

Choose the actual Managed Home explicitly. `--home` overrides `INGOT_HOME`; the
default when neither is set is `~/.ingot`. Do this for every Managed Home you
operate, including homes used by scheduled jobs or other user accounts.

Record these facts in a private backup location:

- `ingot version --json`, OS/architecture, and the path of the Core executable;
- `ingot runtime ls --json`, including desired/rollback bindings and persisted
  `default_argv`, plus `ingot ps -a --json`;
- the project `plugins.toml`, `plugins.lock`, local plugin source trees, and
  external build inputs needed to reconstruct the same composition;
- the external workspace paths, services, credentials, and environment used by
  the application. A Session's workspace binding is not a workspace backup.

Relative local-plugin paths are resolved from the recipe's directory. Preserve
that directory relationship when copying a project, or deliberately update its
paths and resolve a new lock. A lock containing local source identities does
not contain a recoverable copy of those source files.

The JSON outputs, logs, and state may contain private information. Keep the
backup under the same access restrictions as the original deployment. Do not
attach it wholesale to a public issue.

## Stop all writers and take a cold backup

Pause jobs/services that can start or mutate this Home. Wait for builds,
resolves, imports, plugin mutations, and other CLI writers to finish. Stop every
Runtime, not just `default`. A successful `stop` alone does not establish that
an independently launched runtime executable has exited: a standalone writer
can appear as `external`, and an orphan can outlive its supervisor. Resolve
`external`, `orphaned`, `unresponsive`, `starting`, `running`, and `stopping`
states before copying. Stop standalone executables through the terminal or
service that launched them, and confirm their processes have exited.

These examples stop all registered Runtimes and refuse to copy while the
registry still reports an active/uncertain state. They assume the selected
Home is initialized. Linux/macOS uses Bash and `jq`; PowerShell checks native
command exit codes explicitly. Replace the example paths with your deployment
paths. Choose a new backup directory outside the Managed Home and workspaces.

```bash
set -euo pipefail
ingot_home="${INGOT_HOME:-$HOME/.ingot}"
backup_dir="$HOME/ingot-backup-$(date +%Y%m%d-%H%M%S)"
umask 077
mkdir "$backup_dir"
ingot version --json > "$backup_dir/core-version.json"
ingot --home "$ingot_home" runtime ls --json > "$backup_dir/runtimes-before.json"
ingot --home "$ingot_home" ps -a --json > "$backup_dir/processes-before.json"
jq -r '.[].name' "$backup_dir/runtimes-before.json" > "$backup_dir/runtime-names.txt"
while IFS= read -r runtime_name; do
  ingot --home "$ingot_home" stop "$runtime_name" --timeout 30s
done < "$backup_dir/runtime-names.txt"
ingot --home "$ingot_home" runtime ls --json > "$backup_dir/runtimes-stopped.json"
jq -e 'all(.[]; (.state == "stopped" or .state == "failed") and .process == null)' \
  "$backup_dir/runtimes-stopped.json" > /dev/null
cp -a "$ingot_home" "$backup_dir/managed-home"
# Also copy every project recipe/lock and local plugin source tree, and back up
# external workspaces/services using their normal backup procedures.
```

```powershell
$ErrorActionPreference = 'Stop'
$ingotHome = if ($env:INGOT_HOME) { $env:INGOT_HOME } else { Join-Path $HOME '.ingot' }
$backupDir = Join-Path $HOME ('ingot-backup-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
New-Item -ItemType Directory -Path $backupDir | Out-Null
function Invoke-IngotChecked {
    & ingot @args
    if ($LASTEXITCODE -ne 0) { throw "ingot failed with exit code $LASTEXITCODE" }
}
Invoke-IngotChecked version --json | Set-Content (Join-Path $backupDir 'core-version.json')
$before = Invoke-IngotChecked --home $ingotHome runtime ls --json | Out-String
$before | Set-Content (Join-Path $backupDir 'runtimes-before.json')
Invoke-IngotChecked --home $ingotHome ps -a --json |
    Set-Content (Join-Path $backupDir 'processes-before.json')
foreach ($entry in ($before | ConvertFrom-Json)) {
    Invoke-IngotChecked --home $ingotHome stop $entry.name --timeout 30s
}
$stopped = Invoke-IngotChecked --home $ingotHome runtime ls --json | Out-String
$stopped | Set-Content (Join-Path $backupDir 'runtimes-stopped.json')
foreach ($entry in ($stopped | ConvertFrom-Json)) {
    if ($entry.state -notin @('stopped', 'failed') -or $null -ne $entry.process) {
        throw "Runtime $($entry.name) still needs attention: $($entry.state)"
    }
}
Copy-Item -LiteralPath $ingotHome -Destination (Join-Path $backupDir 'managed-home') -Recurse
# Also back up project recipes/locks, local plugin sources, and external workspaces.
```

PowerShell `Copy-Item` is a file-content copy, not an NTFS ACL backup. Use your
normal backup system if you need to preserve owner/ACL information; set the
backup and recovery destination permissions appropriately before using secrets.
Likewise, verify your backup tool's behavior for symbolic links and mounted
directories. An OS lock is a property of a live file handle, not a lock-file
backup: never delete a live `writer.lock` to bypass exclusivity.

What the full Managed Home copy contains:

| Path | Recovery significance |
| --- | --- |
| `home.json`, `builder.toml`, `profiles/` | Home format and build/profile configuration |
| `images/`, including `catalog.json` | Immutable executable/manifest pairs plus tags and pins |
| `runtimes/<name>/runtime.json` | Desired/rollback Image bindings, generation, and default argv |
| `runtimes/<name>/state/` | Plugin configuration, sessions, assets, databases, and other persisted data |
| `runtimes/<name>/logs/` | Historical logs, useful for diagnosing failures |
| `runtimes/<name>/run/` | Process identities, control endpoint/token, locks, and exit observations; keep as evidence, do not transplant into a new Runtime |
| `cache/`, `.transactions/`, lock files | Build cache and recovery metadata; a full snapshot preserves evidence, but is not a portable deployment bundle |

Copy complete plugin state directories together. In particular, do not copy
only a SQLite `.db` file while ignoring its companion files or related asset
stores. All relevant writers must be stopped first. External storage selected
by a plugin needs its own consistent backup.

## Preserve deployable Images separately

Export the known-good Image before upgrading, and preferably also the rollback
Image. Get their concrete IDs from `runtime show`; use an ID rather than a
mutable tag when recording a recovery point. The following commands use the
`default` Runtime after it has been stopped, and continue the variables from the
corresponding backup example:

```bash
old_image=$(ingot --home "$ingot_home" runtime show default --json | jq -r '.desired_image.image_id')
ingot --home "$ingot_home" image verify "$old_image"
ingot --home "$ingot_home" image export "$old_image" --output "$backup_dir/default.ingot-image"
```

```powershell
$runtime = Invoke-IngotChecked --home $ingotHome runtime show default --json | Out-String | ConvertFrom-Json
$oldImage = $runtime.desired_image.image_id
Invoke-IngotChecked --home $ingotHome image verify $oldImage
Invoke-IngotChecked --home $ingotHome image export $oldImage --output (Join-Path $backupDir 'default.ingot-image')
```

Repeat for every required Image. A bundle contains one target variant and its
build provenance, not Runtime state, credentials, workspace files, Runtime
definitions, or a build cache. It can be imported without Go. Execution still
requires the matching OS/architecture. An ImageID is the build-input identity;
the Artifact digest verifies the executable bytes. Neither is a publisher
signature or a permission sandbox.

## Upgrade the Core and composition deliberately

Check the published release and select an exact version for a repeatable
rollout. In this example `v0.3.1` illustrates syntax, not a recommendation to
upgrade/downgrade to that particular version:

```sh
ingot update --check
ingot update --version v0.3.1
ingot version --json
```

The updater verifies the archive and candidate build identity before replacing
the Core. It needs write access next to the invoked executable. Source builds
and package-managed installations should normally be upgraded through the same
build/package mechanism that installed them. `--force --version VERSION`
explicitly permits a same-version reinstall or downgrade; it does not establish
that the older binary can read newer state. A Core-only update need not stop
Runtimes, but the cold backup above is necessary before changing their state.

For a project-owned recipe, select compatible exact plugin versions from their
release notes. From the project directory:

```sh
# Replace both the module and version with the intended released dependency.
ingot plugin update github.com/ingot-agent/plugins/tool-shell@v0.1.0
git diff -- plugins.toml plugins.lock
ingot project show
ingot build default --locked
ingot runtime show default
ingot start default
ingot logs default
```

These commands assume `default` was stopped for backup and that the intended
Home is selected by `INGOT_HOME` or explicit `--home`. A non-Git project should
compare recipe and lock against its backup. Coordinated updates involving
several interdependent modules may require editing the recipe together and
running `ingot project resolve` once. Review the resulting graph before build.
Plugin mutations do resolution checks; full graph/type checks occur during
build or `project generate`.

For a managed profile, run `setup --profile NAME` with the new Core, inspect its
new exact module selections, then `build default --profile NAME` and start the
stopped Runtime. To adopt a new profile in an existing customized project,
generate it into a separate directory with `init`, compare it with the existing
recipe, and apply the intended changes. Do not overwrite the project with
`init --force` merely to obtain new defaults.

`--locked` refuses to refresh an invalid/stale lock. If an ABI or lock-format
change requires a new resolution, preserve the old recipe and lock, resolve
with the new Core, and review the replacement. Do not hand-edit ABI sums or
format versions to make validation pass.

Before production startup, use a separate Home/Runtime with a **copy** of state
to test migrations and startup where practical. Keep its workspaces isolated
and account for external services/ports: a copied API key or workspace path can
still point at production. Build-time `--ingot-check` only constructs the graph
against temporary empty state; it does not test saved configuration, service
availability, credentials, or a state migration. `running` means the child was
spawned; check application readiness and a representative workflow separately.

## Roll back code only when state remains compatible

After confirming that the old plugin versions can read the current state:

```sh
ingot stop default --timeout 30s
ingot runtime rollback default
ingot runtime show default
ingot start default
```

This swaps the desired/rollback bindings and leaves data untouched. The rollback
slot is one previous desired binding, not an unlimited history. Pin/export
additional recovery Images if you need to retain them. `gc` preserves desired
and rollback Images, tags, pins, and live Process Images, but does not create
backups.

If newer code has changed state incompatibly, use the pre-upgrade state backup
with the old Image in a fresh Home as below. Also restore/review the project
recipe and lock before another `up`; otherwise it will rebuild the newer
composition again. There is no `runtime rollback --state` option.

## Recover in a fresh Managed Home

The most explicit recovery path is to initialize a new Home, import a verified
known-good Image, create a new stopped Runtime, and copy only the backed-up
`state/` into that empty Runtime. Keep the original Home and backup intact until
the recovered application has been verified. Use a Core that supports the
exported Image format and the plugin versions that wrote the backup.

The examples restore `default` on the same platform, using the backup directory
and ImageID captured above. They select `web` as the known default argv for the
browser profile. For another deployment, use its saved `default_argv` instead.
Choose a recovery path that does not already exist and is outside the source
and backup trees. The original application's writers must remain stopped.

```bash
recovery_home="$HOME/ingot-recovery-$(date +%Y%m%d-%H%M%S)"
test ! -e "$recovery_home"
ingot --home "$recovery_home" setup
ingot --home "$recovery_home" image import "$backup_dir/default.ingot-image" --no-tag
ingot --home "$recovery_home" image verify "$old_image"
ingot --home "$recovery_home" runtime create default "$old_image" -- web
cp -a "$backup_dir/managed-home/runtimes/default/state/." \
  "$recovery_home/runtimes/default/state/"
ingot --home "$recovery_home" runtime show default
# Verify permissions, plugin configuration and external workspace/service paths.
ingot --home "$recovery_home" start default
ingot --home "$recovery_home" logs default
```

```powershell
$recoveryHome = Join-Path $HOME ('ingot-recovery-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
if (Test-Path -LiteralPath $recoveryHome) { throw 'Choose a new recovery Home' }
Invoke-IngotChecked --home $recoveryHome setup
Invoke-IngotChecked --home $recoveryHome image import (Join-Path $backupDir 'default.ingot-image') --no-tag
Invoke-IngotChecked --home $recoveryHome image verify $oldImage
Invoke-IngotChecked --home $recoveryHome runtime create default $oldImage '--' web
$savedState = Join-Path $backupDir 'managed-home/runtimes/default/state'
$newState = Join-Path $recoveryHome 'runtimes/default/state'
Get-ChildItem -LiteralPath $savedState -Force | ForEach-Object {
    Copy-Item -LiteralPath $_.FullName -Destination $newState -Recurse -Force
}
Invoke-IngotChecked --home $recoveryHome runtime show default
# Verify access controls, plugin configuration and external workspace/service paths.
Invoke-IngotChecked --home $recoveryHome start default
Invoke-IngotChecked --home $recoveryHome logs default
```

Do not copy the old `runtime.json` or `run/` into this new Runtime. The CLI has
created a fresh registry entry and its Process control records must describe
the new launch. Recreate other Runtimes individually, restore their matching
state, and recreate tags/pins as needed. Restore project inputs separately;
projects are not selected by changing `--home`.

The PowerShell example quotes `'--'` because it passes through a wrapper
function before reaching the native executable; quoting preserves the literal
Runtime-argument separator.

For the official `session.sqlite`/`app.backend` combination, saved Sessions keep
immutable absolute workspace bindings. This includes a default workspace that
may be inside the **old** Runtime Home. Copying `state/` to a fresh Home does
not rewrite those bindings or move the workspace. Preserve/restore each
original workspace at its recorded absolute path when recovering those
Sessions, or create new Sessions bound to the intended recovered workspace
using the application's supported flow. Check the plugin documentation before
relocating existing Sessions; the Core offers no generic rebind/migrate
command. Starting both original and recovered applications can access the same
workspace even though their Runtime Homes and writer locks differ.

A raw full-Home snapshot is useful for recovering the same deployment with the
same compatible Core, paths, OS, and architecture. It is not a documented
cross-machine migration format: image-directory spelling differs on Windows,
process records contain OS identities and absolute paths, plugin state may
refer to external paths, and unfinished transactions can refer to prior
locations. Prefer the explicit import/recreate path for relocation. Do not
merge a full Home snapshot into a live Home or discard transaction files to
silence an error. If only a full snapshot survives, preserve it, recover a
separate copy in a compatible environment, and export its validated Images
there before importing them into the final Home.

## Legacy or unsupported formats

The current code accepts Home schema 2, Image manifest 3, resolution lock 3,
Runtime registry 1, and image catalog 1. These are format versions, not product
release versions. The exact checks are in
[`home/schema.go`](../internal/home/schema.go),
[`image/manifest.go`](../internal/image/manifest.go),
[`builder/lock.go`](../internal/builder/lock.go), and
[`managedruntime/registry.go`](../internal/managedruntime/registry.go).

There is no automatic migration for pre-release Homes with `current`,
`current.previous`, top-level `state`, or older Image manifests. `setup --force`
does not convert an incompatible Home. Preserve the old deployment and use a
new empty Home. Rebuild an appropriate composition or obtain a compatible
export; move plugin data only using that plugin's supported migration process.
Do not rename directories or change `schema_version` fields as a substitute
for migration. If the plugin has no documented reader/migration path, keep the
old environment available and obtain a tested migration before adopting the
new state format.
