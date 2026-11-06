@echo off
cd /d "%~dp0"
set CGO_ENABLED=0
go build -trimpath -ldflags "-s -w" -o bsctl.exe . || goto :fail
go build -trimpath -ldflags "-s -w -H=windowsgui -X main.defaultMode=tray" -o BlackSharkTray.new.exe . || goto :fail
echo Built bsctl.exe and BlackSharkTray.new.exe. Run update.cmd to replace the running tray.
pause
exit /b 0
:fail
echo Build failed.
pause
exit /b 1
