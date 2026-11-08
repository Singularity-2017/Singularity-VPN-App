[CmdletBinding()]
param([Parameter(Mandatory=$true)][string]$Source)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$test_dir = Join-Path $root ('dist/xray-staging-test-' + [Guid]::NewGuid().ToString('N'))
$stage = Join-Path $PSScriptRoot 'stage-xray.ps1'
try {
    $destination = Join-Path $test_dir 'xray'
    & $stage -Source $Source -Destination $destination
    Set-Content -LiteralPath (Join-Path $destination 'obsolete.dll') -Value 'stale'
    & $stage -Source $Source -Destination $destination
    if (Test-Path -LiteralPath (Join-Path $destination 'obsolete.dll')) { throw 'stale file survived staging' }
    $input_dir = Join-Path $test_dir 'input'
    New-Item -ItemType Directory -Path $input_dir | Out-Null
    Copy-Item -Path (Join-Path $destination '*') -Destination $input_dir
    Set-Content -LiteralPath (Join-Path $input_dir 'xray.exe') -Value 'untrusted'
    $refused = $false
    try { & $stage -Source $input_dir -Destination $destination } catch {
        if ($_ -notmatch 'hash mismatch for xray.exe') { throw }
        $refused = $true
    }
    if (-not $refused) { throw 'untrusted executable accepted' }
    $pin = Get-Content -LiteralPath (Join-Path $destination 'manifest.json') -Raw | ConvertFrom-Json
    if ((Get-FileHash -LiteralPath (Join-Path $destination 'xray.exe')).Hash.ToLowerInvariant() -ne $pin.files.'xray.exe') {
        throw 'failed verification modified the existing destination'
    }
    Write-Output 'PASS: offline staging, stale cleanup, tamper rejection, existing payload preserved'
} finally {
    $resolved_test_dir = [IO.Path]::GetFullPath($test_dir)
    $allowed_parent = [IO.Path]::GetFullPath((Join-Path $root 'dist')) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolved_test_dir.StartsWith($allowed_parent, [StringComparison]::OrdinalIgnoreCase)) { throw 'unsafe test cleanup path' }
    if (Test-Path -LiteralPath $resolved_test_dir) { Remove-Item -LiteralPath $resolved_test_dir -Recurse -Force }
}
