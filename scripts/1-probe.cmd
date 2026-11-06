@echo off
cd /d "%~dp0"
echo Close Razer Synapse first (tray icon - Exit), then press any key.
echo This check only READS battery/status from the headset.
pause >nul
bsctl.exe probe
echo.
echo Done. Details are in bsctl.log
pause
