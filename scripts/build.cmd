@echo off
rem Builds bsctl.exe and BlackSharkTray.new.exe into dist\ and copies the plugins,
rem the scripts and README.txt next to them. Run dist\update.cmd to replace the tray.
setlocal
cd /d "%~dp0.."
set CGO_ENABLED=0
if not exist dist mkdir dist
rem bsctl.json next to the exe keeps this build portable (state in dist, no updates)
if not exist dist\bsctl.json echo {}> dist\bsctl.json
go build -trimpath -ldflags "-s -w" -o dist\bsctl.exe ./cmd/bsctl
if errorlevel 1 goto :fail
go build -trimpath -ldflags "-s -w -H=windowsgui -X main.defaultMode=tray" -o dist\BlackSharkTray.new.exe ./cmd/bsctl
if errorlevel 1 goto :fail
xcopy /D /I /Y /Q third_party\rnnoise dist\rnnoise >nul
if errorlevel 1 goto :fail
xcopy /D /I /Y /Q third_party\dfn dist\dfn >nul
if errorlevel 1 goto :fail
for %%f in (update.cmd 1-probe.cmd 2-apply.cmd 3-monitor.cmd 4-mouse.cmd 5-dock-test.cmd) do (
  copy /Y scripts\%%f dist\ >nul
  if errorlevel 1 goto :fail
)
copy /Y README.txt dist\ >nul
if errorlevel 1 goto :fail
echo Built dist\bsctl.exe and dist\BlackSharkTray.new.exe. Run dist\update.cmd to replace the running tray.
pause
exit /b 0
:fail
echo Build failed.
pause
exit /b 1
