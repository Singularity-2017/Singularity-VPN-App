# VERSION owns the base number. Git describes provenance, never the base.
function Get-BuildVersion {
    param([string]$Root, [string]$RequestedVersion = '', [switch]$Release)
    $file = Join-Path $Root 'VERSION'
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw 'VERSION is missing' }
    $base = [IO.File]::ReadAllText($file).Trim()
    if ($base -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
        throw 'VERSION must contain X.Y.Z without leading zeroes'
    }
    $canonical = 'v' + $base
    if ($RequestedVersion -and $RequestedVersion -cne $canonical -and $RequestedVersion -cne $base) {
        throw "requested version differs from VERSION ($canonical); edit VERSION first"
    }
    $commit = ''; $dirty = $false; $tagged = $false
    if (Test-Path -LiteralPath (Join-Path $Root '.git')) {
        $commit = & git -C $Root rev-parse --short HEAD 2>$null
        if ($LASTEXITCODE -ne 0) { throw 'cannot read the build commit' }
        $status = @(& git -C $Root -c core.quotepath=false status --porcelain --untracked-files=all)
        if ($LASTEXITCODE -ne 0) { throw 'cannot determine whether the tree is clean' }
        foreach ($line in $status) {
            # Agent-local files are deliberately never committed. Only untracked
            # copies are excluded; a tracked change must still mark the tree.
            if ($line.StartsWith('?? ')) {
                $path = $line.Substring(3)
                if ($path -ceq 'AGENTS.md' -or $path -ceq '.ignore' -or $path.StartsWith('.kilo/')) { continue }
            }
            $dirty = $true
        }
        $head = & git -C $Root rev-parse HEAD
        $tags = @(& git -C $Root tag --points-at $head)
        if ($LASTEXITCODE -ne 0) { throw 'cannot read the build tags' }
        $tagged = $tags -ccontains $canonical
    }
    if ($Release -and ($dirty -or -not $tagged)) { throw "release $canonical requires a clean tree and its exact tag at HEAD" }
    $display = $canonical
    if ($dirty) { $display += '-dirty' }
    elseif (-not $tagged) {
        $display += '-dev'
        if ($commit) { $display += '.' + $commit }
    }
    return [pscustomobject]@{ Base = $canonical; Version = $display; Commit = $commit; Dirty = $dirty; Tagged = $tagged }
}
