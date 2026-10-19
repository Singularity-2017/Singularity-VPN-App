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
    # The service, the window that drives it, and the console harness that
    # drives it without a window.
    [string[]]$Target = @("./cmd/svpn-service", "./cmd/singularityvpn", "./cmd/svpn-ctl", "./cmd/svpn-swap"),
    [string]$Out = "dist",
    # Build SingularityVPN-setup.exe: the three programs and wintun.dll
    # inside one installer. wintun.dll is taken from local/ or from the
    # output directory, where the stage 3 instructions put it.
    [switch]$Setup
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

$control_plane = ""
$update_mirror = ""
$env_file = Join-Path $root "local/app.env"
if (Test-Path $env_file) {
    foreach ($line in Get-Content $env_file) {
        if ($line -match '^\s*CONTROL_PLANE\s*=\s*(.+?)\s*$') {
            $control_plane = $Matches[1]
        }
        if ($line -match '^\s*UPDATE_MIRROR\s*=\s*(.+?)\s*$') {
            $update_mirror = $Matches[1]
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
if ($update_mirror) { $ldflags += "-X $pkg.Update_mirror=$update_mirror" }
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
# The window is a Wails application: it needs Wails' build tags, the GUI
# subsystem so no console flashes up behind it, and the name the design gives
# it. Everything else is a console program and must stay one.
$gui = "./cmd/singularityvpn"
$console = @($Target | Where-Object { $_ -ne $gui })

Push-Location $root
try {
    if ($console.Count -gt 0) {
        & go build -ldflags ($ldflags -join " ") -o $out_dir @console
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
    if ($Target -contains $gui) {
        $gui_ldflags = @($ldflags) + "-H windowsgui"
        & go build -tags desktop,production -ldflags ($gui_ldflags -join " ") -o (Join-Path $out_dir "SingularityVPN.exe") $gui
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
} finally {
    Pop-Location
}
# Authenticode signing (design section 1): present and switched off until a
# certificate exists. Set SVPN_SIGN_CERT to a .pfx (and SVPN_SIGN_PASSWORD if
# it has one) and every program -- and then the setup -- is signed before it
# is packed. Without it, SmartScreen reports an unknown publisher; releases
# are still verified by their Ed25519 manifest signature.
function Sign-Binaries([string[]]$paths) {
    if (-not $env:SVPN_SIGN_CERT) { return }
    $signtool = Get-Command signtool.exe -ErrorAction SilentlyContinue
    if (-not $signtool) { Write-Error "code signing: SVPN_SIGN_CERT is set but signtool.exe is not on PATH" }
    $sign_args = @("sign", "/fd", "SHA256", "/tr", "http://timestamp.digicert.com", "/td", "SHA256", "/f", $env:SVPN_SIGN_CERT)
    if ($env:SVPN_SIGN_PASSWORD) { $sign_args += @("/p", $env:SVPN_SIGN_PASSWORD) }
    & $signtool.Source @sign_args @paths
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}
if ($env:SVPN_SIGN_CERT) {
    Write-Host "code signing: on"
} else {
    Write-Host "code signing: no certificate configured -- skipped"
}
Sign-Binaries @(Get-ChildItem $out_dir -Filter *.exe | Where-Object { $_.Name -ne "SingularityVPN-setup.exe" } | ForEach-Object { $_.FullName })

if ($Setup) {
    $payload = Join-Path $root "cmd/svpn-setup/payload"
    New-Item -ItemType Directory -Force -Path $payload | Out-Null
    foreach ($name in @("svpn-service.exe", "SingularityVPN.exe", "svpn-ctl.exe", "svpn-swap.exe")) {
        $from = Join-Path $out_dir $name
        if (-not (Test-Path $from)) { Write-Error "setup: $name was not built; build all the default targets with -Setup" }
        Copy-Item $from (Join-Path $payload $name) -Force
    }
    $wintun = @((Join-Path $root "local/wintun.dll"), (Join-Path $out_dir "wintun.dll")) | Where-Object { Test-Path $_ } | Select-Object -First 1
    if (-not $wintun) {
        Write-Error "setup: wintun.dll not found in local/ or $Out/ -- download the signed release from https://www.wintun.net and put amd64/wintun.dll in local/"
    }
    Copy-Item $wintun (Join-Path $payload "wintun.dll") -Force
    Push-Location $root
    try {
        & go build -tags payload -ldflags ($ldflags -join " ") -o (Join-Path $out_dir "SingularityVPN-setup.exe") ./cmd/svpn-setup
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
        Sign-Binaries @((Join-Path $out_dir "SingularityVPN-setup.exe"))
    } finally {
        Pop-Location
    }
}
Write-Host "built into $Out"
