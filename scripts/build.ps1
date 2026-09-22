# Builds the application with the values that are decided by the build rather
# than by the source. The control plane address comes from local/app.env,
# which is git-ignored, so the address never reaches the repository.
#
#     CONTROL_PLANE=https://example.invalid
#
# Without that file the build still succeeds and produces a binary that
# connects nowhere, which is what a clean clone should do.

[CmdletBinding()]
param(
    # The service and the console harness that drives it: one is no use
    # without the other until the interface exists.
    [string[]]$Target = @("./cmd/svpn-service", "./cmd/svpn-ctl"),
    [string]$Out = "dist"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

$control_plane = ""
$env_file = Join-Path $root "local/app.env"
if (Test-Path $env_file) {
    foreach ($line in Get-Content $env_file) {
        if ($line -match '^\s*CONTROL_PLANE\s*=\s*(.+?)\s*$') {
            $control_plane = $Matches[1]
        }
    }
}

# A build from a source archive has no git history, and a fresh repository has
# no commit. Version and commit then stay empty and the binary says so, rather
# than claiming a version it cannot know.
$version = ""
$commit = ""
if (Test-Path (Join-Path $root ".git")) {
    # describe fails loudly when no tag exists yet, so ask first.
    if (& git -C $root tag --list | Select-Object -First 1) {
        $version = & git -C $root describe --tags --dirty
        if ($LASTEXITCODE -ne 0) { $version = "" }
    }
    $commit = & git -C $root rev-parse --short HEAD
    if ($LASTEXITCODE -ne 0) { $commit = "" }
}

$built_at = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

$pkg = "singularity-vpn-app/internal/buildinfo"
$ldflags = @()
if ($control_plane) { $ldflags += "-X $pkg.Control_plane=$control_plane" }
if ($version) { $ldflags += "-X $pkg.Version=$version" }
if ($commit) { $ldflags += "-X $pkg.Commit=$commit" }
$ldflags += "-X $pkg.Built_at=$built_at"

$out_dir = Join-Path $root $Out
New-Item -ItemType Directory -Force -Path $out_dir | Out-Null

if ($control_plane) {
    Write-Host "control plane: supplied from local/app.env"
} else {
    Write-Host "control plane: not supplied -- the binary will connect nowhere"
}

# go resolves a relative target against the current directory, not against
# this script. Everything else here already goes through $root; this did not,
# so ./cmd/svpn-service meant scripts/cmd/svpn-service to anyone who ran the
# build from anywhere but the repository root -- which is what double-clicking
# scripts/build.cmd does.
Push-Location $root
try {
    & go build -ldflags ($ldflags -join " ") -o $out_dir @Target
} finally {
    Pop-Location
}
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "built into $Out"
