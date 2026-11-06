@echo off
cd /d "%~dp0"
echo Closing the old tray...
taskkill /IM BlackSharkTray.exe >nul 2>&1
timeout /t 2 /nobreak >nul
taskkill /IM BlackSharkTray.exe /F >nul 2>&1
timeout /t 1 /nobreak >nul
if exist BlackSharkTray.new.exe (
  move /Y BlackSharkTray.new.exe BlackSharkTray.exe >nul
  if errorlevel 1 (
    echo Could not replace BlackSharkTray.exe. Close the tray and run update.cmd again.
    pause
    exit /b 1
  )
)
start "" "%~dp0BlackSharkTray.exe"
echo New tray started - look at the tray icons.
timeout /t 4 >nul
