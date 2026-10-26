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
#
# When UPDATE_MIRROR in local/app.env is a GitHub repository's
# https://github.com/<owner>/<repo>/releases/latest/download, it also goes
# into the signed mirror list, and publishing the GitHub release is one of
# the printed steps.

[CmdletBinding()]
param(
    [string]$Version = "",
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
. (Join-Path $PSScriptRoot 'version.ps1')
if (-not $Key) { $Key = Join-Path $root "local/release-keys/primary.key" }

$color = -not $env:NO_COLOR -and -not [Console]::IsOutputRedirected
function say([string]$text, [string]$fg) {
    if ($color -and $fg) { Write-Host $text -ForegroundColor $fg } else { Write-Host $text }
}
function fail([string]$text) { say $text "Red"; exit 1 }
function command([string]$text) { say "    $text" "DarkGray" }

$semver = '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
try { $version_info = Get-BuildVersion -Root $root -RequestedVersion $Version } catch { fail $_.Exception.Message }
$Version = $version_info.Base
if ($Minimum -and $Minimum -notmatch $semver) { fail "minimum must look like v1.2.3, got '$Minimum'" }
if (-not (Test-Path $Key)) { fail "no release key at $Key" }

Push-Location $root
try {
    if ($version_info.Dirty) { fail "the tree has uncommitted changes; commit or stash them first" }
    $out = Join-Path $root "local/release-$Version"
    if (Test-Path $out) { fail "$out already exists; a signed release is not overwritten" }

    $head = & git rev-parse HEAD
    $tagged = & git rev-parse -q --verify "refs/tags/$Version^{commit}"
    if ($tagged -and $tagged -ne $head) { fail "$Version already tags another commit" }
    if (-not $tagged) {
        & git tag -a $Version -m $Version
        if ($LASTEXITCODE -ne 0) { fail "tagging $Version failed" }
        say "tagged $Version" "Green"
    }

    & (Join-Path $PSScriptRoot "build.ps1") -Setup -Version $Version -Release
    if ($LASTEXITCODE -ne 0) { fail "the build failed" }

    $out = Join-Path $root "local/release-$Version"
    if (Test-Path $out) { fail "$out already exists; a signed release is not overwritten" }
    $sign = @("sign", "--key", $Key, "--setup", (Join-Path $root "dist/SingularityVPN-setup.exe"), "--version", $Version, "--out", $out)
    if ($Minimum) { $sign += @("--minimum", $Minimum) }
    if ($Urgent) { $sign += "--urgent" }
    $github = ""
    $env_file = Join-Path $root "local/app.env"
    if (Test-Path $env_file) {
        foreach ($line in Get-Content $env_file) {
            if ($line -match '^\s*UPDATE_MIRROR\s*=\s*(https://github\.com/([^/\s]+/[^/\s]+)/releases/latest/download)/?\s*$') {
                $github = $Matches[2]
                if ($Mirror -notcontains $Matches[1]) { $Mirror += $Matches[1] }
            } elseif ($line -match '^\s*UPDATE_MIRROR\s*=.*github\.com') {
                say "UPDATE_MIRROR names GitHub but not as https://github.com/<owner>/<repo>/releases/latest/download; it is not signed into the mirror list" "Yellow"
            }
        }
    }
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
if ($github) {
    # The tag goes first: a release made for a tag GitHub does not have is
    # tagged at whatever the default branch points to.
    $setup_name = (Get-ChildItem -Path $out -Filter "SingularityVPN-setup-*.exe" | Select-Object -First 1).Name
    if (-not $setup_name) { fail "no SingularityVPN-setup-*.exe in $out to publish on GitHub" }
    say "  3. publish it on GitHub, $github (installations fetch the setup there first when GitHub answers):"
    command "git push github $Version"
    command "gh release create $Version `"$out/manifest.json`" `"$out/manifest.json.sig`" `"$out/$setup_name`" --repo $github --title $Version --notes $Version --verify-tag"
}
