@echo off
rem Checks before a release: gofmt, go vet and go test for Windows, go vet and
rem build for Linux (platform-free code must stay portable), staticcheck for both.
setlocal
cd /d "%~dp0.."
set "BAD="
for /f "delims=" %%f in ('gofmt -l cmd internal') do (
  echo gofmt: %%f
  set "BAD=1"
)
if defined BAD goto :fail
echo === windows: go vet
go vet ./...
if errorlevel 1 goto :fail
echo === windows: go test
go test ./...
if errorlevel 1 goto :fail
echo === staticcheck v0.8.2
set "GOBIN=%TEMP%\bsctl-tools"
go install honnef.co/go/tools/cmd/staticcheck@v0.8.2
if errorlevel 1 goto :fail
"%GOBIN%\staticcheck.exe" ./...
if errorlevel 1 goto :fail
set GOOS=linux
echo === linux: go vet, go build, staticcheck
go vet ./...
if errorlevel 1 goto :fail
go build ./...
if errorlevel 1 goto :fail
"%GOBIN%\staticcheck.exe" ./...
if errorlevel 1 goto :fail
echo All checks passed.
exit /b 0
:fail
echo Checks failed.
exit /b 1
