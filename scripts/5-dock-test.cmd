@echo off
cd /d "%~dp0"
echo Watch the dock light: green, yellow, orange, red, breathing green, rainbow - 3 seconds each.
bsctl.exe dock test
echo.
pause
