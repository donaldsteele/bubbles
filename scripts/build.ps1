<#
.SYNOPSIS
    Builds the web UI, then cross-compiles the server for six targets into dist\.

.DESCRIPTION
    Mirrors scripts/build.sh. Each binary is zipped with README.md,
    HOW-TO-OPEN.txt and LICENSE (using tools/distzip, so no external zip tool is
    needed), and dist\SHA256SUMS lists the artifacts. Server builds always use
    CGO_ENABLED=0.

.PARAMETER SkipZip
    Build the binaries only; do not create zip archives or SHA256SUMS of zips.

.PARAMETER SkipWeb
    Reuse an existing internal\webui\dist instead of running npm ci and the web build.

.PARAMETER Version
    Version string to stamp. Defaults to $env:BUBBLES_VERSION, then
    `git describe --tags --always --dirty`, then "dev".

.EXAMPLE
    scripts\build.ps1 -SkipZip
#>
[CmdletBinding()]
param(
    [switch]$SkipZip,
    [switch]$SkipWeb,
    [string]$Version
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $repo

$Module = 'github.com/donaldsteele/bubbles'
$Targets = @(
    'windows/amd64', 'windows/arm64',
    'darwin/amd64', 'darwin/arm64',
    'linux/amd64', 'linux/arm64'
)

function Invoke-Checked {
    param([string]$What, [scriptblock]$Command)
    & $Command
    if ($LASTEXITCODE -ne 0) { throw "$What failed with exit code $LASTEXITCODE." }
}

function Get-GitValue {
    param([string[]]$GitArgs, [string]$Fallback)
    $saved = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $value = (& git @GitArgs 2>$null | Select-Object -First 1)
        if ($LASTEXITCODE -ne 0 -or -not $value) { return $Fallback }
        return ([string]$value).Trim()
    }
    finally {
        $ErrorActionPreference = $saved
    }
}

if (-not $Version) {
    $Version = if ($env:BUBBLES_VERSION) { $env:BUBBLES_VERSION } else { Get-GitValue @('describe', '--tags', '--always', '--dirty') 'dev' }
}
$commit = Get-GitValue @('rev-parse', '--short', 'HEAD') 'unknown'
$date = (Get-Date).ToUniversalTime().ToString('yyyy-MM-dd\THH:mm:ss\Z')
$ldflags = "-s -w -X $Module/internal/buildinfo.Version=$Version -X $Module/internal/buildinfo.Commit=$commit -X $Module/internal/buildinfo.Date=$date"

if (-not $SkipWeb) {
    Write-Output '==> web: npm ci + build'
    Invoke-Checked 'npm ci' { npm --prefix web ci }
    Invoke-Checked 'web build' { npm --prefix web run build }
}
elseif (-not (Test-Path -LiteralPath (Join-Path $repo 'internal\webui\dist\index.html'))) {
    throw '-SkipWeb needs a built internal\webui\dist (index.html missing).'
}

Write-Output "==> version: $Version"
$dist = Join-Path $repo 'dist'
if (Test-Path -LiteralPath $dist) { Remove-Item -LiteralPath $dist -Recurse -Force }
$stageRoot = Join-Path $dist '.stage'
New-Item -ItemType Directory -Path $stageRoot -Force | Out-Null

$artifacts = @()
try {
    foreach ($target in $Targets) {
        $goos, $goarch = $target.Split('/')
        $ext = if ($goos -eq 'windows') { '.exe' } else { '' }
        $name = "bubbles-$Version-$goos-$goarch"
        $binary = Join-Path $dist "$name$ext"

        Write-Output "==> build $name"
        $env:CGO_ENABLED = '0'
        $env:GOOS = $goos
        $env:GOARCH = $goarch
        try {
            Invoke-Checked "go build $target" { go build -trimpath -ldflags $ldflags -o $binary ./cmd/bubbles }
        }
        finally {
            Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
        }
        if ($SkipZip) { $artifacts += $binary }

        if (-not $SkipZip) {
            $stage = Join-Path $stageRoot $name
            New-Item -ItemType Directory -Path $stage -Force | Out-Null
            $stagedBinary = Join-Path $stage "bubbles$ext"
            Copy-Item -LiteralPath $binary -Destination $stagedBinary
            $zip = Join-Path $dist "$name.zip"
            $files = @(
                $stagedBinary,
                (Join-Path $repo 'README.md'),
                (Join-Path $repo 'HOW-TO-OPEN.txt'),
                (Join-Path $repo 'LICENSE')
            )
            Invoke-Checked "zip $name" { go run ./tools/distzip -out $zip @files }
            $artifacts += $zip
        }
    }
}
finally {
    if (Test-Path -LiteralPath $stageRoot) { Remove-Item -LiteralPath $stageRoot -Recurse -Force }
}

Write-Output '==> dist\SHA256SUMS'
# Computed with .NET directly: Get-FileHash is not available in every PowerShell 5.1 host.
$sha256 = [System.Security.Cryptography.SHA256]::Create()
try {
    $lines = foreach ($artifact in $artifacts) {
        $stream = [System.IO.File]::OpenRead($artifact)
        try {
            $hash = ([BitConverter]::ToString($sha256.ComputeHash($stream)) -replace '-', '').ToLowerInvariant()
        }
        finally {
            $stream.Dispose()
        }
        "$hash  $(Split-Path -Leaf $artifact)"
    }
}
finally {
    $sha256.Dispose()
}
[System.IO.File]::WriteAllText((Join-Path $dist 'SHA256SUMS'), (($lines -join "`n") + "`n"), (New-Object System.Text.UTF8Encoding($false)))

$count = (Get-ChildItem -LiteralPath $dist -File).Count
Write-Output "==> done: $count files in dist\"
