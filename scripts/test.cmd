@echo off
rem Double-clickable wrapper around test.ps1 for running the automated test suite.
rem %~dp0 ensures the script is found regardless of the caller's working directory.
rem Arguments are passed through (e.g. `test.cmd -Build` or `test.cmd -E2E`).
rem The console window pauses on exit so results can be read before closing.

powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0test.ps1" %*
set test_exit=%errorlevel%

echo.
if "%test_exit%"=="0" (
    echo [SUITE PASSED]
) else (
    echo [SUITE FAILED with code %test_exit%]
)

pause
exit /b %test_exit%
