# Makes a release: tags the clean tree, builds the setup from that tag,
# signs it and verifies the signature. What leaves this machine -- pushing
# the tag, copying the release onto the control plane -- is printed, not
# done: both are the owner's to run.
#
#     scripts/release.ps1 -Version v0.1.0
#     scripts/release.ps1 -Version v0.1.1 -Minimum v0.1.0 -Urgent
#
# The private key is read from local/release-keys/primary.key and never
# printed.

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Version,
    # Installations older than this apply the update without waiting for
    # the tunnel to be idle.
    [string]$Minimum = "",
    # Apply even while a tunnel is up.
    [switch]$Urgent,
    # Base URLs of further places the release is published, https only.
    [string[]]$Mirror = @(),
    [string]$Key = ""
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
if (-not $Key) { $Key = Join-Path $root "local/release-keys/primary.key" }

$color = -not $env:NO_COLOR -and -not [Console]::IsOutputRedirected
function say([string]$text, [string]$fg) {
    if ($color -and $fg) { Write-Host $text -ForegroundColor $fg } else { Write-Host $text }
}
function fail([string]$text) { say $text "Red"; exit 1 }
function command([string]$text) { say "    $text" "DarkGray" }

$semver = '^v\d+\.\d+\.\d+$'
if ($Version -notmatch $semver) { fail "version must look like v1.2.3, got '$Version'" }
if ($Minimum -and $Minimum -notmatch $semver) { fail "minimum must look like v1.2.3, got '$Minimum'" }
if (-not (Test-Path $Key)) { fail "no release key at $Key" }

Push-Location $root
try {
    # A dirty tree builds as vX.Y.Z-dirty, which is not a release and never
    # updates itself. Untracked files do not make it dirty.
    $dirty = & git status --porcelain --untracked-files=no
    if ($dirty) { fail "the tree has uncommitted changes; commit or stash them first" }

    $head = & git rev-parse HEAD
    $tagged = & git rev-parse -q --verify "refs/tags/$Version^{commit}"
    if ($tagged -and $tagged -ne $head) { fail "$Version already tags another commit" }
    if (-not $tagged) {
        & git tag -a $Version -m $Version
        if ($LASTEXITCODE -ne 0) { fail "tagging $Version failed" }
        say "tagged $Version" "Green"
    }

    $described = & git describe --tags --dirty
    if ($described -ne $Version) { fail "the build would be versioned '$described', not $Version" }

    & (Join-Path $PSScriptRoot "build.ps1") -Setup
    if ($LASTEXITCODE -ne 0) { fail "the build failed" }

    $out = Join-Path $root "local/release-$Version"
    if (Test-Path $out) { fail "$out already exists; a signed release is not overwritten" }
    $sign = @("sign", "--key", $Key, "--setup", (Join-Path $root "dist/SingularityVPN-setup.exe"), "--version", $Version, "--out", $out)
    if ($Minimum) { $sign += @("--minimum", $Minimum) }
    if ($Urgent) { $sign += "--urgent" }
    foreach ($m in $Mirror) { $sign += @("--mirror", $m) }
    & go run ./cmd/svpn-release @sign
    if ($LASTEXITCODE -ne 0) { fail "signing failed" }
    & go run ./cmd/svpn-release verify $out
    if ($LASTEXITCODE -ne 0) { fail "the signed release does not verify" }
} finally {
    Pop-Location
}

say "release $Version is in local/release-$Version" "Green"
say "Still to do, by hand:"
say "  1. publish the tag:"
command "git push origin $Version"
say "  2. publish it on the control plane, from its repository:"
command "deploy app-release $out"
