@echo off
rem Double-clickable wrapper around build.ps1, so a build does not need a
rem terminal that is already in the right place. %~dp0 is this file's own
rem directory: without it, ./build.ps1 would only be found when the caller
rem happened to be standing in scripts/. Arguments are passed through, so
rem `build.cmd -Target ./cmd/svpn-probe` works. Double-clicked, with no
rem arguments, it builds the setup: that is what a double-click is for.
rem The window waits for a key either way, so what the build said can be
rem read before it closes.
if "%~1"=="" (
    powershell -NoProfile -File "%~dp0build.ps1" -Setup
) else (
    powershell -NoProfile -File "%~dp0build.ps1" %*
)
set build_exit=%errorlevel%
if not "%build_exit%"=="0" echo build failed with code %build_exit%
pause
exit /b %build_exit%
