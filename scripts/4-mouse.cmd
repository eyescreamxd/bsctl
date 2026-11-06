@echo off
cd /d "%~dp0"
echo Close Razer Synapse first (tray icon - Exit), then press any key.
echo This check only READS battery and firmware from the mouse and the dock.
pause >nul
bsctl.exe mouse
echo.
echo Done. Details are in bsctl.log
pause
