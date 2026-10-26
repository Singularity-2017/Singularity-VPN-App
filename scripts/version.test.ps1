$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'version.ps1')
function Assert-Version($actual, $expected) {
    if ($actual -cne $expected) { throw "expected '$expected', got '$actual'" }
}
function Assert-Refused([scriptblock]$action) {
    $refused = $false
    try { & $action | Out-Null } catch { $refused = $true }
    if (-not $refused) { throw 'expected a refusal' }
}
$test_root = Join-Path ([IO.Path]::GetTempPath()) ('vpn-version-tests-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $test_root | Out-Null
try {
    [IO.File]::WriteAllText((Join-Path $test_root 'VERSION'), "0.1.3`n")
    Assert-Version (Get-BuildVersion $test_root).Version 'v0.1.3-dev'
    Assert-Refused { Get-BuildVersion $test_root -Release }
    & git -C $test_root init -q
    & git -C $test_root config user.email synthetic@example.invalid
    & git -C $test_root config user.name Synthetic
    & git -C $test_root config core.autocrlf false
    & git -C $test_root add VERSION
    & git -C $test_root commit -qm initial
    & git -C $test_root tag v0.1.0
    $info = Get-BuildVersion $test_root
    Assert-Version $info.Version ("v0.1.3-dev." + $info.Commit)
    & git -C $test_root tag v0.1.3
    Assert-Version (Get-BuildVersion $test_root -Release).Version 'v0.1.3'
    [IO.File]::WriteAllText((Join-Path $test_root 'AGENTS.md'), 'local agent instructions')
    [IO.File]::WriteAllText((Join-Path $test_root '.ignore'), 'local agent settings')
    New-Item -ItemType Directory -Path (Join-Path $test_root '.kilo') | Out-Null
    [IO.File]::WriteAllText((Join-Path $test_root '.kilo/local.json'), '{}')
    Assert-Version (Get-BuildVersion $test_root).Version 'v0.1.3'
    [IO.File]::WriteAllText((Join-Path $test_root 'new.go'), 'package example')
    Assert-Version (Get-BuildVersion $test_root).Version 'v0.1.3-dirty'
    Assert-Version (Get-BuildVersion $test_root -RequestedVersion 'v0.1.3').Version 'v0.1.3-dirty'
    Assert-Refused { Get-BuildVersion $test_root -Release }
    Assert-Refused { Get-BuildVersion $test_root -RequestedVersion 'v0.1.4' }
    Remove-Item -LiteralPath (Join-Path $test_root 'new.go')
    [IO.File]::WriteAllText((Join-Path $test_root 'VERSION'), "0.1.4`n")
    Assert-Version (Get-BuildVersion $test_root).Version 'v0.1.4-dirty'
    [IO.File]::WriteAllText((Join-Path $test_root 'VERSION'), "01.1.4`n")
    Assert-Refused { Get-BuildVersion $test_root }
    Remove-Item -LiteralPath (Join-Path $test_root 'VERSION')
    Assert-Refused { Get-BuildVersion $test_root }
    $linked = Join-Path $test_root 'linked-worktree'
    & git -C $test_root worktree add --detach -q $linked HEAD
    if ($LASTEXITCODE -ne 0) { throw 'cannot create the temporary linked worktree' }
    Assert-Version (Get-BuildVersion $linked -Release).Version 'v0.1.3'
    Write-Output 'Version tests passed: canonical base, old tags, clean release, dirty/untracked, local agent files, override refusal and source archive.'
} finally {
    $resolved = [IO.Path]::GetFullPath($test_root)
    $temp_base = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/') + [IO.Path]::DirectorySeparatorChar
    if ($resolved.StartsWith($temp_base, [StringComparison]::OrdinalIgnoreCase) -and ([IO.Path]::GetFileName($resolved)).StartsWith('vpn-version-tests-')) {
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
