@echo off
cd /d "%~dp0"
echo Monitoring headset events for 90 seconds.
echo Meanwhile: press the mic mute button a few times, turn the volume wheel, pause/play a video.
bsctl.exe monitor 90
echo.
pause
