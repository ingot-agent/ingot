# install.ps1 -- build and install the ingot CLI and its official plugin set.
#
# The ingot binary embeds no plugin sources: the official plugins are
# distributed as directory trees next to the binary (this repository keeps
# them under plugins/) and `ingot init` locates them during installation.
# This script installs both the binary and the plugin tree:
#
#   <Prefix>\bin\ingot.exe
#   <Prefix>\share\ingot\plugins\<plugin>\...
#
# Usage:
#   .\scripts\install.ps1                          # -> $env:LocalAppData\ingot
#   .\scripts\install.ps1 -Prefix D:\ingot         # explicit prefix
param(
    [string]$Prefix = (Join-Path $env:LOCALAPPDATA 'ingot'),
    [string]$DestDir = ''
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
    Write-Host 'Next steps:'
    Write-Host '  1. Run: ingot init'
    Write-Host '  2. Edit your ingot home config.toml (model provider settings).'
    Write-Host '  3. Run: ingot apply'
    Write-Host '  4. Run: ingot chat'
} finally {
    Remove-Item -Recurse -Force $staging -ErrorAction SilentlyContinue
}
