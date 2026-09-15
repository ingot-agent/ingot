# Install an official ingot core binary from GitHub Releases.
param(
    [string]$Prefix = (Join-Path $env:LOCALAPPDATA 'ingot'),
    [string]$BinaryDir = '',
    [string]$DestDir = '',
    [string]$Version = '',
    [switch]$Force
)

$ErrorActionPreference = 'Stop'
$ReleaseBase = 'https://github.com/ingot-agent/ingot/releases'
$StagedTarget = $null
if (-not $BinaryDir) {
    $BinaryDir = Join-Path $Prefix 'bin'
}

function Join-StagedInstallPath {
    param([string]$StagingRoot, [string]$InstallPath)
    if (-not $StagingRoot) { return $InstallPath }
    if (-not [IO.Path]::IsPathRooted($InstallPath)) { return Join-Path $StagingRoot $InstallPath }
    $root = [IO.Path]::GetPathRoot($InstallPath)
    $relative = $InstallPath.Substring($root.Length).TrimStart('\', '/')
    if (-not $relative) { return $StagingRoot }
    return Join-Path $StagingRoot $relative
}

function Add-UserPathEntry {
    param([string]$PathEntry)
    $normalized = [IO.Path]::GetFullPath($PathEntry).TrimEnd('\', '/')
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $entries = if ($userPath) { @($userPath -split ';' | Where-Object { $_ }) } else { @() }
    $present = $entries | Where-Object { $_.Trim().TrimEnd('\', '/').Equals($normalized, [StringComparison]::OrdinalIgnoreCase) }
    if (-not $present) {
        $updated = if ($userPath) { "$userPath;$normalized" } else { $normalized }
        [Environment]::SetEnvironmentVariable('Path', $updated, 'User')
    }
    $processEntries = @($env:Path -split ';' | Where-Object { $_ })
    $processPresent = $processEntries | Where-Object { $_.Trim().TrimEnd('\', '/').Equals($normalized, [StringComparison]::OrdinalIgnoreCase) }
    if (-not $processPresent) {
        $env:Path = if ($env:Path) { "$env:Path;$normalized" } else { $normalized }
    }
    return [bool]$present
}

function Get-ReleaseFile {
    param([string]$Uri, [string]$Destination)
    Invoke-WebRequest -UseBasicParsing -Uri $Uri -OutFile $Destination
}

function Get-PublishedChecksum {
    param([string]$ChecksumsPath, [string]$Name)
    foreach ($line in Get-Content $ChecksumsPath) {
        if ($line -match '^([0-9a-f]{64})\s+\*?(.+)$' -and $Matches[2] -eq $Name) {
            return $Matches[1]
        }
    }
    throw "install.ps1: no checksum published for $Name"
}

function Assert-Checksum {
    param([string]$Path, [string]$Name, [string]$ChecksumsPath)
    $expected = Get-PublishedChecksum $ChecksumsPath $Name
    $actual = (Get-FileHash -Algorithm SHA256 -Path $Path).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
        throw "install.ps1: SHA-256 mismatch for $Name"
    }
}

function Convert-SemVer {
    param([string]$Value)
    $normalized = if ($Value.StartsWith('v', [StringComparison]::Ordinal)) { $Value.Substring(1) } else { $Value }
    if ($normalized -notmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$') {
        throw "install.ps1: invalid semantic version $Value"
    }
    $major = $Matches[1]
    $minor = $Matches[2]
    $patch = $Matches[3]
    $pre = if ($Matches[4]) { @($Matches[4] -split '\.') } else { @() }
    foreach ($identifier in $pre) {
        if ($identifier -match '^[0-9]+$' -and $identifier.Length -gt 1 -and $identifier.StartsWith('0')) {
            throw "install.ps1: invalid semantic version $Value"
        }
    }
    return [pscustomobject]@{
        Major = $major
        Minor = $minor
        Patch = $patch
        Pre = $pre
        Normalized = $normalized
    }
}

function Compare-NumericIdentifier {
    param([string]$Left, [string]$Right)
    if ($Left.Length -lt $Right.Length) { return -1 }
    if ($Left.Length -gt $Right.Length) { return 1 }
    return [Math]::Sign([string]::CompareOrdinal($Left, $Right))
}

function Compare-SemVer {
    param([string]$Left, [string]$Right)
    $leftVersion = Convert-SemVer $Left
    $rightVersion = Convert-SemVer $Right
    foreach ($field in @('Major', 'Minor', 'Patch')) {
        $comparison = Compare-NumericIdentifier $leftVersion.$field $rightVersion.$field
        if ($comparison -ne 0) { return $comparison }
    }
    if ($leftVersion.Pre.Count -eq 0 -and $rightVersion.Pre.Count -eq 0) { return 0 }
    if ($leftVersion.Pre.Count -eq 0) { return 1 }
    if ($rightVersion.Pre.Count -eq 0) { return -1 }
    $count = [Math]::Max($leftVersion.Pre.Count, $rightVersion.Pre.Count)
    for ($index = 0; $index -lt $count; $index++) {
        if ($index -ge $leftVersion.Pre.Count) { return -1 }
        if ($index -ge $rightVersion.Pre.Count) { return 1 }
        $leftPart = $leftVersion.Pre[$index]
        $rightPart = $rightVersion.Pre[$index]
        if ($leftPart -ceq $rightPart) { continue }
        $leftNumeric = [regex]::IsMatch($leftPart, '^[0-9]+$')
        $rightNumeric = [regex]::IsMatch($rightPart, '^[0-9]+$')
        if ($leftNumeric -and $rightNumeric) { return (Compare-NumericIdentifier $leftPart $rightPart) }
        if ($leftNumeric) { return -1 }
        if ($rightNumeric) { return 1 }
        return [Math]::Sign([string]::CompareOrdinal($leftPart, $rightPart))
    }
    return 0
}

function Convert-ToReleaseTag {
    param([string]$Value)
    $parsed = Convert-SemVer $Value
    return "v$($parsed.Normalized)"
}

function Expand-CoreArchive {
    param([string]$ArchivePath, [string]$Destination)
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [IO.Compression.ZipFile]::OpenRead($ArchivePath)
    try {
        $entries = @{}
        foreach ($entry in $archive.Entries) {
            $modeType = (($entry.ExternalAttributes -shr 16) -band 0xF000)
            if (($entry.FullName -ne 'ingot.exe' -and $entry.FullName -ne 'LICENSE') -or
                $entry.Name -ne $entry.FullName -or $entries.ContainsKey($entry.FullName) -or
                $modeType -ne 0x8000) {
                throw "install.ps1: unsafe release archive entry $($entry.FullName)"
            }
            if ($entry.Length -le 0 -or
                ($entry.FullName -eq 'ingot.exe' -and $entry.Length -gt 201326592) -or
                ($entry.FullName -eq 'LICENSE' -and $entry.Length -gt 1048576)) {
                throw "install.ps1: invalid release archive entry $($entry.FullName)"
            }
            $entries[$entry.FullName] = $entry
        }
        if ($entries.Count -ne 2 -or -not $entries.ContainsKey('ingot.exe') -or -not $entries.ContainsKey('LICENSE')) {
            throw 'install.ps1: release archive must contain exactly ingot.exe and LICENSE'
        }
        New-Item -ItemType Directory -Path $Destination | Out-Null
        $candidate = Join-Path $Destination 'ingot.exe'
        [IO.Compression.ZipFileExtensions]::ExtractToFile($entries['ingot.exe'], $candidate)
        [IO.Compression.ZipFileExtensions]::ExtractToFile($entries['LICENSE'], (Join-Path $Destination 'LICENSE'))
        return $candidate
    } finally {
        $archive.Dispose()
    }
}

if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq [System.Runtime.InteropServices.Architecture]::X64) {
    $Architecture = 'amd64'
} elseif ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq [System.Runtime.InteropServices.Architecture]::Arm64) {
    $Architecture = 'arm64'
} else {
    throw "install.ps1: unsupported architecture $([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture)"
}

$Staging = Join-Path ([IO.Path]::GetTempPath()) ("ingot-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $Staging | Out-Null
try {
    if ($Version) {
        $Tag = Convert-ToReleaseTag $Version
    } else {
        $Hint = Join-Path $Staging 'version-hint'
        Get-ReleaseFile "$ReleaseBase/latest/download/VERSION" $Hint
        $VersionHint = ([IO.File]::ReadAllText($Hint)).Trim()
        $Tag = Convert-ToReleaseTag $VersionHint
    }
    $ExactBase = "$ReleaseBase/download/$Tag"
    $VersionPath = Join-Path $Staging 'VERSION'
    $ChecksumsPath = Join-Path $Staging 'checksums.txt'
    Get-ReleaseFile "$ExactBase/VERSION" $VersionPath
    $PublishedTag = ([IO.File]::ReadAllText($VersionPath)).Trim()
    if ((Convert-ToReleaseTag $PublishedTag) -ne $PublishedTag) { throw "install.ps1: invalid published release version $PublishedTag" }
    if ($PublishedTag -ne $Tag) { throw "install.ps1: release VERSION is $PublishedTag, expected $Tag" }
    Get-ReleaseFile "$ExactBase/checksums.txt" $ChecksumsPath
    Assert-Checksum $VersionPath 'VERSION' $ChecksumsPath

    $Asset = "ingot-$Tag-windows-$Architecture.zip"
    $ArchivePath = Join-Path $Staging $Asset
    Get-ReleaseFile "$ExactBase/$Asset" $ArchivePath
    Assert-Checksum $ArchivePath $Asset $ChecksumsPath
    $Extracted = Join-Path $Staging 'extract'
    $Candidate = Expand-CoreArchive $ArchivePath $Extracted
    if (-not (Test-Path -LiteralPath $Candidate -PathType Leaf)) { throw 'install.ps1: release archive does not contain ingot.exe' }
    $ReleaseVersion = (Convert-SemVer $Tag).Normalized
    $CandidateVersion = (& $Candidate --version | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $CandidateVersion -ne "ingot $ReleaseVersion") {
        throw "install.ps1: candidate reports $CandidateVersion, expected ingot $ReleaseVersion"
    }

    $TargetDirectory = Join-StagedInstallPath $DestDir $BinaryDir
    $Target = Join-Path $TargetDirectory 'ingot.exe'
    if (Test-Path -LiteralPath $Target -PathType Leaf) {
        $PreviousHome = $env:INGOT_HOME
        try {
            $env:INGOT_HOME = Join-Path $Staging 'legacy-home'
            $CurrentOutput = (& $Target --version 2>$null | Out-String).Trim()
        } catch {
            $CurrentOutput = ''
        } finally {
            if ($null -eq $PreviousHome) { Remove-Item Env:INGOT_HOME -ErrorAction SilentlyContinue } else { $env:INGOT_HOME = $PreviousHome }
        }
        if ($CurrentOutput -match '^ingot (.+)$') {
            $CurrentVersion = $Matches[1]
            try {
                $Comparison = Compare-SemVer $ReleaseVersion $CurrentVersion
                if ($Comparison -eq 0 -and -not $Force) {
                    Write-Host "ingot $ReleaseVersion is already installed at $Target"
                    return
                }
                if ($Comparison -lt 0 -and -not $Force) {
                    throw "install.ps1: refusing to downgrade ingot $CurrentVersion to $ReleaseVersion without -Force"
                }
            } catch {
                if ($_.Exception.Message -notlike 'install.ps1: invalid semantic version*') { throw }
            }
        }
    }

    New-Item -ItemType Directory -Force -Path $TargetDirectory | Out-Null
    $StagedTarget = "$Target.tmp.$PID"
    Copy-Item -LiteralPath $Candidate -Destination $StagedTarget -Force
    if (Test-Path -LiteralPath $Target -PathType Leaf) {
        [IO.File]::Replace($StagedTarget, $Target, $null, $true)
    } else {
        [IO.File]::Move($StagedTarget, $Target)
    }
    $StagedTarget = $null

    if (-not $DestDir) {
        if (Add-UserPathEntry $BinaryDir) {
            Write-Host "==> $BinaryDir is already in the current user's PATH"
        } else {
            Write-Host "==> added $BinaryDir to the current user's PATH"
        }
    }
    Write-Host "ingot $ReleaseVersion installed to $Target"
} finally {
    if ($StagedTarget) { Remove-Item -LiteralPath $StagedTarget -Force -ErrorAction SilentlyContinue }
    Remove-Item -Recurse -Force $Staging -ErrorAction SilentlyContinue
}
