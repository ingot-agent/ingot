# install.ps1 -- install ingot and its official plugin set, then prepare a
# ready-to-use agent in one command.
#
# The ingot binary embeds no plugin sources: the official plugins are
# distributed as directory trees next to the binary (this repository keeps
# them under plugins/) and `ingot init` locates them during installation.
# This script installs both the binary and the plugin tree:
#
#   <Prefix>\bin\ingot.exe
#   <Prefix>\share\ingot\plugins\<plugin>\...
#
# After installation the script initializes a new home or refreshes the
# official bundle in an existing home, collects model provider settings (from
# the INGOT_* environment variables or interactively), builds a named image,
# creates the `default` Runtime, and offers to start the web UI.
#
# Usage:
#   .\scripts\install.ps1                                   # -> $env:LocalAppData\ingot
#   .\scripts\install.ps1 -Prefix D:\ingot                  # explicit prefix
#   $env:INGOT_API_KEY='sk-...'; .\scripts\install.ps1      # non-interactive
param(
    [string]$Prefix = (Join-Path $env:LOCALAPPDATA 'ingot'),
    [string]$DestDir = '',
    [Alias('Home')]
    [string]$HomeDir = (Join-Path $env:USERPROFILE '.ingot'),
    [ValidateSet('default', 'minimal')]
    [string]$Profile = 'default',
    [switch]$NoConfigure,
    [switch]$NoApply
)

$ErrorActionPreference = 'Stop'

$Root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$BinaryDir = Join-Path $Prefix 'bin'
$ShareDir = Join-Path $Prefix 'share\ingot'
$PluginDir = Join-Path $ShareDir 'plugins'

function Join-StagedInstallPath {
    param(
        [string]$StagingRoot,
        [string]$InstallPath
    )

    if ([string]::IsNullOrEmpty($StagingRoot)) {
        return $InstallPath
    }
    if (-not [IO.Path]::IsPathRooted($InstallPath)) {
        return Join-Path $StagingRoot $InstallPath
    }

    # DESTDIR is a filesystem root prepended to the install prefix. Windows
    # drive-qualified paths cannot be concatenated directly, so drop the
    # drive root before joining (D:\stage + D:\ingot\bin ->
    # D:\stage\ingot\bin).
    $root = [IO.Path]::GetPathRoot($InstallPath)
    $relative = $InstallPath.Substring($root.Length).TrimStart('\', '/')
    if ([string]::IsNullOrEmpty($relative)) {
        return $StagingRoot
    }
    return Join-Path $StagingRoot $relative
}

function Add-UserPathEntry {
    param(
        [string]$PathEntry
    )

    $normalized = [IO.Path]::GetFullPath($PathEntry).TrimEnd('\', '/')
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $userEntries = if ([string]::IsNullOrEmpty($userPath)) {
        @()
    } else {
        @($userPath -split ';' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    }
    $alreadyInUserPath = $userEntries | Where-Object {
        $_.Trim().TrimEnd('\', '/').Equals($normalized, [StringComparison]::OrdinalIgnoreCase)
    }

    if (-not $alreadyInUserPath) {
        $updatedUserPath = if ([string]::IsNullOrEmpty($userPath)) {
            $normalized
        } else {
            "$userPath;$normalized"
        }
        [Environment]::SetEnvironmentVariable('Path', $updatedUserPath, 'User')
    }

    $processEntries = @($env:Path -split ';' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    $alreadyInProcessPath = $processEntries | Where-Object {
        $_.Trim().TrimEnd('\', '/').Equals($normalized, [StringComparison]::OrdinalIgnoreCase)
    }
    if (-not $alreadyInProcessPath) {
        $env:Path = if ([string]::IsNullOrEmpty($env:Path)) {
            $normalized
        } else {
            "$env:Path;$normalized"
        }
    }

    return [bool]$alreadyInUserPath
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw 'install.ps1: go 1.24+ is required to build ingot'
}
if (-not (Test-Path (Join-Path $Root 'go.mod'))) {
    throw "install.ps1: cannot locate the ingot source tree at $Root"
}
if (-not (Test-Path (Join-Path $Root 'plugins'))) {
    throw "install.ps1: the official plugin set (plugins/) is missing from $Root"
}

$staging = Join-Path ([System.IO.Path]::GetTempPath()) ("ingot-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $staging | Out-Null
try {
    Write-Host '==> building ingot'
    Push-Location $Root
    try {
        & go build -trimpath -o (Join-Path $staging 'ingot.exe') .\cmd\ingot
        if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
    } finally {
        Pop-Location
    }

    $targetBin = Join-StagedInstallPath $DestDir $BinaryDir
    $targetPlugin = Join-StagedInstallPath $DestDir $PluginDir
    Write-Host "==> installing to $targetBin"
    New-Item -ItemType Directory -Force -Path $targetBin, $targetPlugin | Out-Null
    Copy-Item (Join-Path $staging 'ingot.exe') (Join-Path $targetBin 'ingot.exe') -Force

    Write-Host "==> installing official plugins to $targetPlugin"
    Copy-Item (Join-Path $Root 'plugins\*') $targetPlugin -Recurse -Force

    if (-not $DestDir) {
        if (Add-UserPathEntry $BinaryDir) {
            Write-Host "==> $BinaryDir is already in the current user's PATH"
        } else {
            Write-Host "==> added $BinaryDir to the current user's PATH"
        }
    }

    Write-Host ''
    Write-Host 'ingot installed:'
    Write-Host "  binary:  $targetBin\ingot.exe"
    Write-Host "  plugins: $targetPlugin"
    Write-Host ''

    if ($DestDir) {
        Write-Host 'Staged packaging complete (DestDir set). To prepare a usable home:'
        Write-Host "  $BinaryDir\ingot.exe --home `"$HomeDir`" init --profile $Profile --bundle `"$PluginDir`""
        return
    }

    $Ingot = Join-Path $BinaryDir 'ingot.exe'
    $defaultHomeDir = Join-Path $env:USERPROFILE '.ingot'
    $normalizedHomeDir = [IO.Path]::GetFullPath($HomeDir).TrimEnd('\', '/')
    $normalizedDefaultHomeDir = [IO.Path]::GetFullPath($defaultHomeDir).TrimEnd('\', '/')
    $homeArgument = if ($normalizedHomeDir.Equals($normalizedDefaultHomeDir, [StringComparison]::OrdinalIgnoreCase)) {
        ''
    } else {
        "--home `"$HomeDir`""
    }
    $ProfileRecipe = Join-Path $HomeDir "profiles/$Profile.toml"
    $ProfileLock = Join-Path $HomeDir "profiles/$Profile.lock"
    $buildCommand = if ($homeArgument) { "ingot $homeArgument build --use `"$ProfileRecipe`" --lock `"$ProfileLock`" --tag local/ingot:default" } else { "ingot build --use `"$ProfileRecipe`" --lock `"$ProfileLock`" --tag local/ingot:default" }
    $startCommand = if ($homeArgument) { "ingot $homeArgument runtime start default" } else { 'ingot runtime start default' }

    # --- init -----------------------------------------------------------------
    Write-Host "==> initializing or refreshing ingot home $HomeDir (profile: $Profile)"
    New-Item -ItemType Directory -Force -Path $HomeDir | Out-Null
    & $Ingot --home $HomeDir init --profile $Profile --bundle $PluginDir
    if ($LASTEXITCODE -ne 0) { throw 'ingot init failed' }

    $ImageRef = 'local/ingot:default'
    $RuntimeName = 'default'
    if ($NoApply) {
        Write-Host '==> skipping image build and Runtime creation (legacy NoApply switch)'
    } else {
        Write-Host '==> building runtime image (first build downloads modules and may take a few minutes)'
        $buildAttempts = 0
        while ($true) {
            & $Ingot --home $HomeDir build --use $ProfileRecipe --lock $ProfileLock --tag $ImageRef
            if ($LASTEXITCODE -eq 0) { break }
            $buildAttempts++
            if ($buildAttempts -ge 2) { throw 'build failed twice; re-run this script after checking network access' }
            Write-Host '==> retrying build'
            Start-Sleep -Seconds 2
        }
        & $Ingot --home $HomeDir runtime inspect $RuntimeName *> $null
        if ($LASTEXITCODE -eq 0) {
            & $Ingot --home $HomeDir runtime switch $RuntimeName $ImageRef
        } else {
            & $Ingot --home $HomeDir runtime create $RuntimeName --image $ImageRef -- web
        }
        if ($LASTEXITCODE -ne 0) { throw 'runtime create/switch failed' }
    }

    # --- model provider configuration -----------------------------------------
    # Plugins own their persistent configuration inside the Runtime Home; there
    # is no shared runtime config.toml. The model provider is configured by
    # writing the provider plugin's own state file before the first run.
    $ProviderDir = Join-Path $HomeDir "runtimes/$RuntimeName/state/model.openai-compatible"
    $RuntimeDir = Join-Path $HomeDir "runtimes/$RuntimeName/state/model.runtime"
    $Config = Join-Path $ProviderDir 'config.toml'
    $Defaults = Join-Path $RuntimeDir 'config.toml'
    $Configured = $false
    if ((Test-Path $Config) -and -not (Select-String -Path $Config -Pattern 'api_key = ""' -Quiet)) {
        $Configured = $true
    }
    if (-not $NoApply -and -not $NoConfigure -and -not $Configured) {
        Write-Host '==> model provider configuration'
        $ProviderName = if ($env:INGOT_PROVIDER_NAME) { $env:INGOT_PROVIDER_NAME } else { 'openai' }
        $BaseUrl = if ($env:INGOT_BASE_URL) { $env:INGOT_BASE_URL } else { 'https://api.openai.com/v1' }
        $ApiKey = if ($env:INGOT_API_KEY) { $env:INGOT_API_KEY } else { '' }
        $Model = if ($env:INGOT_MODEL) { $env:INGOT_MODEL } else { 'gpt-4o-mini' }

        if (-not $ApiKey) {
            $ApiKey = Read-Host 'API key'
        }
        if ($ApiKey) {
            # Escape for a TOML basic string: \ and " only.
            $esc = { param($s) $s.Replace('\', '\\').Replace('"', '\"') }
            New-Item -ItemType Directory -Force -Path $ProviderDir | Out-Null
            New-Item -ItemType Directory -Force -Path $RuntimeDir | Out-Null
            $providerToml = "providers = [`n  { name = `"$(& $esc $ProviderName)`", base_url = `"$(& $esc $BaseUrl)`", api_key = `"$(& $esc $ApiKey)`", models = [`"$(& $esc $Model)`"] },`n]`n"
            $defaultsToml = "default_provider = `"$(& $esc $ProviderName)`"`ndefault_model = `"$(& $esc $Model)`"`n"
            [IO.File]::WriteAllText($Config, $providerToml)
            [IO.File]::WriteAllText($Defaults, $defaultsToml)
            Write-Host "==> wrote provider $ProviderName ($Model) to $Config"
        } else {
            Write-Warning 'no API key provided; skipping configuration'
            Write-Warning "write $Config manually, then run: $Ingot --home `"$HomeDir`" runtime start $RuntimeName"
        }
    }

    Write-Host ''
    Write-Host 'Agent home is ready. Next steps:'
    if ($NoApply) {
        Write-Host "1. $buildCommand"
        Write-Host '2. Create the Runtime: ingot runtime create default --image local/ingot:default -- web'
        Write-Host "3. $startCommand"
    } else {
        Write-Host 'Start the Web UI with the following command:'
        Write-Host $startCommand
    }
} finally {
    Remove-Item -Recurse -Force $staging -ErrorAction SilentlyContinue
}
