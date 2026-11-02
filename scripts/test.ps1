# Runs the full automated test suite for Singularity VPN.
#
# Usage:
#   powershell -NoProfile -File scripts/test.ps1
#   powershell -NoProfile -File scripts/test.ps1 -Build      # also verifies compilation of all targets
#   powershell -NoProfile -File scripts/test.ps1 -E2E        # also runs live network verification (requires Administrator)
#   powershell -NoProfile -File scripts/test.ps1 -NoBeep    # disable completion sound

[CmdletBinding()]
param(
    [switch]$Build,
    [switch]$E2E,
    [switch]$NoBeep
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

# ANSI terminal setup & styling
$use_color = (-not $env:NO_COLOR -and -not [Console]::IsOutputRedirected)
if ($use_color -and $PSVersionTable.PSVersion.Major -ge 5 -and [Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
    try {
        $kernel32 = Add-Type -MemberDefinition @"
[DllImport("kernel32.dll", SetLastError = true)]
public static extern IntPtr GetStdHandle(int nStdHandle);
[DllImport("kernel32.dll", SetLastError = true)]
public static extern bool GetConsoleMode(IntPtr hConsoleHandle, out uint lpMode);
[DllImport("kernel32.dll", SetLastError = true)]
public static extern bool SetConsoleMode(IntPtr hConsoleHandle, uint dwMode);
"@ -Name "ConsoleMode" -Namespace "Win32" -PassThru
        $stdout = $kernel32::GetStdHandle(-11)
        $mode = 0
        if ($kernel32::GetConsoleMode($stdout, [ref]$mode)) {
            $kernel32::SetConsoleMode($stdout, $mode -bor 0x0004) | Out-Null
        }
    } catch {}
}

$esc = [char]27
$c_reset = if ($use_color) { "$esc[0m" } else { "" }
$c_green = if ($use_color) { "$esc[32m" } else { "" }
$c_red = if ($use_color) { "$esc[31m" } else { "" }
$c_yellow = if ($use_color) { "$esc[33m" } else { "" }
$c_gray = if ($use_color) { "$esc[90m" } else { "" }
$c_bold = if ($use_color) { "$esc[1m" } else { "" }

function Log-Command([string]$cmd) {
    Write-Host "$c_gray> $cmd$c_reset"
}

function Log-Status([string]$status, [string]$color, [string]$name, [string]$detail = "") {
    $tag = "$color[$status]$c_reset"
    if ($detail) {
        Write-Host "  $tag $name $c_gray($detail)$c_reset"
    } else {
        Write-Host "  $tag $name"
    }
}

$suite_results = @()
$suite_start = [System.Diagnostics.Stopwatch]::StartNew()

function Run-Step([string]$name, [string]$command_display, [scriptblock]$action) {
    $step_num = $script:suite_results.Count + 1
    Write-Host ""
    Write-Host "$c_bold=== Step ${step_num}: $name$c_reset"
    Log-Command $command_display

    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    $step_failed = $false
    $err_msg = ""

    try {
        & $action
        if ($LASTEXITCODE -ne 0 -and $LASTEXITCODE -ne $null) {
            $step_failed = $true
            $err_msg = "exit code $LASTEXITCODE"
        }
    } catch {
        $step_failed = $true
        $err_msg = $_.Exception.Message
    }

    $sw.Stop()
    $duration = [string]::Format("{0:0.0}s", $sw.Elapsed.TotalSeconds)

    if ($step_failed) {
        Log-Status "FAIL" $c_red $name "$duration - $err_msg"
        $script:suite_results += [PSCustomObject]@{
            Name     = $name
            Passed   = $false
            Duration = $duration
            Error    = $err_msg
        }
    } else {
        Log-Status "PASS" $c_green $name $duration
        $script:suite_results += [PSCustomObject]@{
            Name     = $name
            Passed   = $true
            Duration = $duration
            Error    = ""
        }
    }
}

Push-Location $root
try {
    # 1. Whitespace & diff sanity
    Run-Step "Git diff whitespace check" "git diff --check" {
        $out = & git diff --check
        if ($LASTEXITCODE -ne 0) { throw $out }
    }

    # 2. Go formatting
    Run-Step "Go code formatting" "gofmt -l cmd internal" {
        $unformatted = & gofmt -l cmd internal
        if ($unformatted) {
            throw "Unformatted files found:`n$unformatted"
        }
    }

    # 3. Version derivation rules
    Run-Step "Version derivation tests" "powershell -NoProfile -File scripts/version.test.ps1" {
        & powershell -NoProfile -File (Join-Path $PSScriptRoot "version.test.ps1")
        if ($LASTEXITCODE -ne 0) { throw "version tests failed" }
    }

    # 4. Frontend unit tests
    Run-Step "Frontend unit tests (Node)" "node --test cmd/singularityvpn/frontend_test/*.test.cjs" {
        & node --test cmd/singularityvpn/frontend_test/*.test.cjs
        if ($LASTEXITCODE -ne 0) { throw "frontend unit tests failed" }
    }

    # 5. Go vet
    Run-Step "Go vet static analysis" "go vet ./..." {
        & go vet ./...
        if ($LASTEXITCODE -ne 0) { throw "go vet detected issues" }
    }

    # 6. Go unit & integration test suite
    Run-Step "Go test suite (all packages)" "go test ./..." {
        & go test ./...
        if ($LASTEXITCODE -ne 0) { throw "go test suite failed" }
    }

    # 7. Build verification (optional)
    if ($Build) {
        Run-Step "Build verification (all binaries)" "powershell -NoProfile -File scripts/build.ps1 -Setup" {
            & powershell -NoProfile -File (Join-Path $PSScriptRoot "build.ps1") -Setup
            if ($LASTEXITCODE -ne 0) { throw "build failed" }
        }
    }

    # 8. Live E2E network verification (optional)
    if ($E2E) {
        Run-Step "Live network E2E verification" "svpn-service / svpn-ctl probe" {
            $elevated = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
                [Security.Principal.WindowsBuiltInRole]::Administrator)
            if (-not $elevated) {
                throw "E2E network verification requires an elevated Administrator console"
            }
            # Verify built ctl exists
            $ctl = Join-Path $root "dist/svpn-ctl.exe"
            if (-not (Test-Path $ctl)) {
                throw "dist/svpn-ctl.exe is missing -- run with -Build first"
            }
            & $ctl status
            if ($LASTEXITCODE -ne 0) { throw "svpn-ctl status failed" }
        }
    }

} finally {
    Pop-Location
}

$suite_start.Stop()
$total_time = [string]::Format("{0:0.0}s", $suite_start.Elapsed.TotalSeconds)

# Print Summary
Write-Host ""
Write-Host "$c_bold------------------------------------------------------------$c_reset"
Write-Host "$c_bold Test Suite Summary ($total_time)$c_reset"
Write-Host "$c_bold------------------------------------------------------------$c_reset"

$failed_count = 0
foreach ($res in $suite_results) {
    if ($res.Passed) {
        Write-Host "  $c_green[PASS]$c_reset $($res.Name) $c_gray($($res.Duration))$c_reset"
    } else {
        $failed_count++
        Write-Host "  $c_red[FAIL]$c_reset $($res.Name) $c_gray($($res.Duration))$c_reset"
        if ($res.Error) {
            Write-Host "         $c_red$($res.Error)$c_reset"
        }
    }
}

Write-Host "$c_bold------------------------------------------------------------$c_reset"

# Sound notification
if (-not $NoBeep) {
    try {
        if ($failed_count -eq 0) {
            [Console]::Beep(880, 150)
            [Console]::Beep(1174, 250)
        } else {
            [Console]::Beep(440, 250)
            [Console]::Beep(330, 350)
        }
    } catch {}
}

if ($failed_count -eq 0) {
    Write-Host "${c_green}${c_bold}Result: ALL $($suite_results.Count) CHECKS PASSED$c_reset"
    exit 0
} else {
    Write-Host "${c_red}${c_bold}Result: $failed_count OF $($suite_results.Count) CHECKS FAILED$c_reset"
    exit 1
}
