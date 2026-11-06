@echo off
rem Publishes this folder as a PRIVATE GitHub repo "bsctl". Needs git and gh (gh auth login).
rem Later updates: git add -A, git commit -m "...", git push
setlocal
cd /d "%~dp0"

where git >nul 2>&1
if errorlevel 1 (echo git not found: https://git-scm.com/download/win& goto :end)
where gh >nul 2>&1
if errorlevel 1 (echo gh not found: https://cli.github.com& goto :end)
gh auth status >nul 2>&1
if errorlevel 1 (echo gh is not logged in, run: gh auth login& goto :end)

if exist ".git" goto :repo_ok
git init -b main
if errorlevel 1 goto :end
:repo_ok

rem commit identity for this repo only, when git has none configured
git config user.email >nul 2>&1
if not errorlevel 1 goto :identity_ok
for /f "delims=" %%i in ('gh api user --jq .login') do set "GHLOGIN=%%i"
for /f "delims=" %%i in ('gh api user --jq .id') do set "GHID=%%i"
git config user.name "%GHLOGIN%"
git config user.email "%GHID%+%GHLOGIN%@users.noreply.github.com"
:identity_ok

git add -A
if errorlevel 1 goto :end
git diff --cached --quiet
if not errorlevel 1 goto :committed
set "VER="
for /f "tokens=4" %%v in ('findstr /c:"const version" version.go') do set "VER=%%~v"
set "MSG=%TEMP%\bsctl-commit-msg.txt"
> "%MSG%" echo bsctl %VER%
>> "%MSG%" echo.
>> "%MSG%" echo Co-Authored-By: Claude Opus 5.5 ^<noreply@anthropic.com^>
git commit -q -F "%MSG%"
if errorlevel 1 goto :end
del "%MSG%" >nul 2>&1
:committed

git remote get-url origin >nul 2>&1
if not errorlevel 1 goto :push
gh repo create bsctl --private --source . --remote origin --push
if errorlevel 1 goto :end
goto :done
:push
git push -u origin main
if errorlevel 1 goto :end
:done
echo.
gh repo view --json url --jq .url
:end
echo.
pause
