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
# After installation the script runs `ingot init`, collects model provider
# settings (from the INGOT_* environment variables or interactively), runs
# `ingot apply` to build the runtime image, and offers to start the web UI.
#
# Usage:
#   .\scripts\install.ps1                                   # -> $env:LocalAppData\ingot
#   .\scripts\install.ps1 -Prefix D:\ingot                  # explicit prefix
#   $env:INGOT_API_KEY='sk-...'; .\scripts\install.ps1      # non-interactive
param(
    [string]$Prefix = (Join-Path $env:LOCALAPPDATA 'ingot'),
    [string]$DestDir = '',
    [string]$Home = (Join-Path $env:USERPROFILE '.ingot'),
    [ValidateSet('default', 'minimal')]
    [string]$Profile = 'default',
    [switch]$NoConfigure,
    [switch]$NoApply,
    [switch]$NoOpen
)

$ErrorActionPreference = 'Stop'

$Root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$BinaryDir = Join-Path $Prefix 'bin'
$ShareDir = Join-Path $Prefix 'share\ingot'
$PluginDir = Join-Path $ShareDir 'plugins'

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

    $targetBin = Join-Path $DestDir $BinaryDir.TrimStart('\')
    $targetPlugin = Join-Path $DestDir $PluginDir.TrimStart('\')
    Write-Host "==> installing to $targetBin"
    New-Item -ItemType Directory -Force -Path $targetBin, $targetPlugin | Out-Null
    Copy-Item (Join-Path $staging 'ingot.exe') (Join-Path $targetBin 'ingot.exe') -Force

    Write-Host "==> installing official plugins to $targetPlugin"
    Copy-Item (Join-Path $Root 'plugins\*') $targetPlugin -Recurse -Force

    Write-Host ''
    Write-Host 'ingot installed:'
    Write-Host "  binary:  $targetBin\ingot.exe"
    Write-Host "  plugins: $targetPlugin"
    Write-Host ''

    if ($DestDir) {
        Write-Host 'Staged packaging complete (DestDir set). To prepare a usable home:'
        Write-Host "  $BinaryDir\ingot.exe --home `"$Home`" init --profile $Profile --bundle `"$PluginDir`""
        return
    }

    $Ingot = Join-Path $BinaryDir 'ingot.exe'

    # --- init -----------------------------------------------------------------
    if (Test-Path (Join-Path $Home 'plugins.toml')) {
        Write-Host "==> home $Home is already initialized; skipping init"
    } else {
        Write-Host "==> initializing ingot home $Home (profile: $Profile)"
        New-Item -ItemType Directory -Force -Path $Home | Out-Null
        & $Ingot --home $Home init --profile $Profile --bundle $PluginDir
        if ($LASTEXITCODE -ne 0) { throw 'ingot init failed' }
    }

    # --- model provider configuration -----------------------------------------
    $Config = Join-Path $Home 'config.toml'
    $Configured = $true
    if ((Test-Path $Config) -and (Select-String -Path $Config -Pattern 'api_key = ""' -Quiet)) {
        $Configured = $false
    }
    if (-not $NoConfigure -and -not $Configured) {
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
            $content = Get-Content -Raw -Path $Config
            $content = $content.Replace('name = "openai"',       "name = `"$(& $esc $ProviderName)`"")
            $content = $content.Replace('base_url = "https://api.example.com/v1"', "base_url = `"$(& $esc $BaseUrl)`"")
            $content = $content.Replace('api_key = ""',          "api_key = `"$(& $esc $ApiKey)`"")
            $content = $content.Replace('models = ["gpt-4o-mini"]', "models = [`"$(& $esc $Model)`"]")
            $content = $content.Replace('default_provider = "openai"', "default_provider = `"$(& $esc $ProviderName)`"")
            $content = $content.Replace('default_model = "gpt-4o-mini"', "default_model = `"$(& $esc $Model)`"")
            [IO.File]::WriteAllText($Config, $content)
            Write-Host "==> wrote provider $ProviderName ($Model) to $Config"
        } else {
            Write-Warning 'no API key provided; skipping configuration'
            Write-Warning "edit $Config manually, then run: $Ingot --home `"$Home`" apply"
        }
    }

    # --- apply ----------------------------------------------------------------
    if ($NoApply) {
        Write-Host "==> skipping apply (NoApply); run later: $Ingot --home `"$Home`" apply"
    } else {
        Write-Host '==> building runtime image (first build downloads modules and may take a few minutes)'
        $applyAttempts = 0
        while ($true) {
            & $Ingot --home $Home apply
            if ($LASTEXITCODE -eq 0) { break }
            $applyAttempts++
            if ($applyAttempts -ge 2) {
                throw 'apply failed twice; re-run this script after checking network access'
            }
            Write-Host '==> retrying apply'
            Start-Sleep -Seconds 2
        }
        Write-Host '==> active image ready'
    }

    # --- start ----------------------------------------------------------------
    if (-not $NoApply) {
        $Start = $true
        if ($Host.Name -eq 'ConsoleHost' -and -not [Environment]::GetEnvironmentVariable('CI')) {
            $Start = $false
            $Answer = Read-Host 'Start the web UI now? [Y/n]'
            if ($Answer -notmatch '^(n|no)$') { $Start = $true }
        }
        if ($Start) {
            $Log = Join-Path $Home 'web.log'
            Write-Host "==> starting web UI in the background (log: $Log)"
            Start-Process -FilePath $Ingot -ArgumentList "--home `"$Home`"", 'web' -RedirectStandardOutput $Log -RedirectStandardError $Log -WindowStyle Hidden
            Start-Sleep -Seconds 1
            Write-Host '    listening on http://127.0.0.1:7316/'
            if (-not $NoOpen) {
                Start-Process 'http://127.0.0.1:7316/' -ErrorAction SilentlyContinue
            }
        }
    } else {
        Write-Host ''
        Write-Host 'Agent home is ready. Next steps:'
        Write-Host "  $Ingot --home `"$Home`" apply"
        Write-Host "  $Ingot --home `"$Home`" web   # then open http://127.0.0.1:7316/"
    }
} finally {
    Remove-Item -Recurse -Force $staging -ErrorAction SilentlyContinue
}
