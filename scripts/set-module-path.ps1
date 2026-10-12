<#
.SYNOPSIS
    Renames the Go module path and rewrites every reference to it.

.DESCRIPTION
    Updates go.mod (and tray/go.mod when present), rewrites import paths in
    all .go files under cmd, internal, pkg, examples, tools and tray, and
    rewrites the module path inside the -X ldflags strings of scripts/*.sh
    and scripts/*.ps1. Run once before the first public push.

.PARAMETER Module
    New module path, for example github.com/acme/bubbles.

.EXAMPLE
    scripts\set-module-path.ps1 -Module github.com/acme/bubbles
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Module
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $repo

if ($Module -notmatch '^[A-Za-z0-9][A-Za-z0-9._~\-]*(/[A-Za-z0-9._~\-]+)+$') {
    throw "Invalid module path '$Module'. Expected a form like github.com/acme/bubbles."
}

$goMod = Join-Path $repo 'go.mod'
$oldModule = $null
foreach ($line in [System.IO.File]::ReadAllLines($goMod)) {
    if ($line -match '^module\s+(\S+)\s*$') {
        $oldModule = $Matches[1]
        break
    }
}
if (-not $oldModule) { throw "Could not find a module directive in $goMod." }

if ($oldModule -eq $Module) {
    Write-Output "Module path is already '$Module'; nothing to do."
    exit 0
}

$utf8 = New-Object System.Text.UTF8Encoding($false)

function Update-FileText {
    param([string]$Path, [string]$From, [string]$To)
    $text = [System.IO.File]::ReadAllText($Path)
    $updated = $text.Replace($From, $To)
    if ($updated -ne $text) {
        [System.IO.File]::WriteAllText($Path, $updated, $utf8)
        return $true
    }
    return $false
}

# go.mod through the toolchain so formatting stays canonical.
& go mod edit -module $Module
if ($LASTEXITCODE -ne 0) { throw 'go mod edit -module failed.' }

$trayMod = Join-Path $repo 'tray\go.mod'
if (Test-Path -LiteralPath $trayMod) {
    & go -C tray mod edit -module "$Module/tray"
    if ($LASTEXITCODE -ne 0) { throw 'go mod edit -module failed for tray/.' }
    # The tray requires the server module through a local replace; point both
    # at the new path so the rewritten imports resolve.
    & go -C tray mod edit -dropreplace $oldModule -droprequire $oldModule
    if ($LASTEXITCODE -ne 0) { throw 'go mod edit (drop old module) failed for tray/.' }
    & go -C tray mod edit -require "$Module@v0.0.0-00010101000000-000000000000" -replace "$Module=../"
    if ($LASTEXITCODE -ne 0) { throw 'go mod edit (require new module) failed for tray/.' }
}

$changed = 0
$goDirs = 'cmd', 'internal', 'pkg', 'examples', 'tools', 'tray'
foreach ($dir in $goDirs) {
    $root = Join-Path $repo $dir
    if (-not (Test-Path -LiteralPath $root)) { continue }
    Get-ChildItem -LiteralPath $root -Recurse -File -Filter '*.go' | ForEach-Object {
        # Import paths are always quoted in Go source, so match the opening quote
        # to avoid touching unrelated prose that merely contains the path.
        if (Update-FileText -Path $_.FullName -From ('"' + $oldModule) -To ('"' + $Module)) {
            $changed++
        }
    }
}

# The tray frontend imports its Wails bindings by a directory named after the
# Go package path (frontend/bindings/<module>/tray).
$trayFrontend = Join-Path $repo 'tray\frontend\src'
if (Test-Path -LiteralPath $trayFrontend) {
    Get-ChildItem -LiteralPath $trayFrontend -Recurse -File -Include '*.ts', '*.js' | ForEach-Object {
        if (Update-FileText -Path $_.FullName -From ('bindings/' + $oldModule) -To ('bindings/' + $Module)) {
            $changed++
        }
    }
}

Get-ChildItem -LiteralPath (Join-Path $repo 'scripts') -File |
    Where-Object { ($_.Extension -in '.sh', '.ps1') -and ($_.Name -ne 'set-module-path.ps1') } |
    ForEach-Object {
        if (Update-FileText -Path $_.FullName -From $oldModule -To $Module) {
            $changed++
        }
    }

Write-Output "Module path changed from '$oldModule' to '$Module'. Files rewritten: $changed."
Write-Output 'Next: review the diff, run go build ./... and go test ./...'
