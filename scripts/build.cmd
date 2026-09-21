@echo off
rem Double-clickable wrapper around build.ps1, so a build does not need a
rem terminal that is already in the right place. %~dp0 is this file's own
rem directory: without it, ./build.ps1 would only be found when the caller
rem happened to be standing in scripts/. Arguments are passed through, so
rem `build.cmd -Target ./cmd/svpn-probe` works.
powershell -NoProfile -File "%~dp0build.ps1" %*
set build_exit=%errorlevel%
pause
exit /b %build_exit%
