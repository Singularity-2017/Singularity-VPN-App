# Offline staging only. The committed pin, never the source directory, is trusted.
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$Source,
    [Parameter(Mandatory=$true)][string]$Destination
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$pin_path = Join-Path $root 'internal/xraybundle/pins.json'
$pin = Get-Content -LiteralPath $pin_path -Raw | ConvertFrom-Json

# Check everything before modifying the destination. No config, profile, script,
# private pilot record or unlisted binary is copied.
foreach ($file in $pin.files.PSObject.Properties) {
    $path = Join-Path $Source $file.Name
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Xray staging: missing $($file.Name)" }
    if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -ne $file.Value) {
        throw "Xray staging: hash mismatch for $($file.Name)"
    }
}
$destination_path = [IO.Path]::GetFullPath($Destination)
$root_path = [IO.Path]::GetFullPath($root).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
if (-not $destination_path.StartsWith($root_path, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Xray staging: destination must be inside this worktree'
}
if ((Split-Path -Leaf $destination_path) -ne 'xray') { throw 'Xray staging: destination must be an xray subdirectory' }
$source_path = [IO.Path]::GetFullPath($Source).TrimEnd('\', '/')
if ($source_path.Equals($destination_path.TrimEnd('\', '/'), [StringComparison]::OrdinalIgnoreCase) -or
    $source_path.StartsWith($destination_path.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Xray staging: source must be outside destination'
}
$ancestor = $destination_path
while ($ancestor -and $ancestor.Length -ge $root_path.TrimEnd('\', '/').Length) {
    if ((Test-Path -LiteralPath $ancestor) -and ((Get-Item -LiteralPath $ancestor).Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw 'Xray staging: destination and its parents cannot be reparse points'
    }
    $ancestor = Split-Path -Parent $ancestor
}
if (Test-Path -LiteralPath $destination_path) {
    Remove-Item -LiteralPath $destination_path -Recurse -Force
}
New-Item -ItemType Directory -Path $destination_path -Force | Out-Null
foreach ($file in $pin.files.PSObject.Properties) {
    $target = Join-Path $destination_path $file.Name
    Copy-Item -LiteralPath (Join-Path $Source $file.Name) -Destination $target
    if ((Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant() -ne $file.Value) {
        throw "Xray staging: copied hash mismatch for $($file.Name)"
    }
}
Copy-Item -LiteralPath $pin_path -Destination (Join-Path $destination_path 'manifest.json')
Copy-Item -LiteralPath (Join-Path $root 'scripts/xray-NOTICE.txt') -Destination (Join-Path $destination_path 'NOTICE.txt')
if (-not $env:NO_COLOR -and -not [Console]::IsOutputRedirected) {
    Write-Host "Xray $($pin.version): verified offline payload staged" -ForegroundColor Green
} else { Write-Host "Xray $($pin.version): verified offline payload staged" }
