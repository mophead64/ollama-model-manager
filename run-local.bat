@echo off
rem Builds the app into .\bin and runs it natively on Windows. On Linux or
rem macOS, use run-local.sh instead.
rem
rem   run-local.bat                  build and start the server
rem   run-local.bat reset-password   build and run a subcommand instead
rem
rem Settings come from the environment, with local-friendly defaults:
rem   PORT         (8080)
rem   OLLAMA_HOST  (http://localhost:11434)
rem   DB_PATH      (.\data\omm.db, so the login persists between runs)
rem   MODELS_DIR   (auto-detected: %USERPROFILE%\.ollama\models)
setlocal
cd /d "%~dp0"

where go >nul 2>nul
if errorlevel 1 (
  echo Go isn't installed or isn't on the PATH: see https://go.dev/doc/install 1>&2
  exit /b 1
)

set "VERSION=dev"
for /f "delims=" %%v in ('git describe --tags --always --dirty 2^>nul') do set "VERSION=%%v"
set "VERSION=%VERSION%-local"
set "BIN=bin\ollama-model-manager.exe"

echo Building %BIN% (%VERSION%)...
if not exist bin mkdir bin
go build -trimpath -ldflags="-X github.com/mophead64/ollama-model-manager/internal/version.Version=%VERSION%" -o "%BIN%" .\cmd\ollama-model-manager
if errorlevel 1 exit /b 1

if not defined PORT set "PORT=8080"
if not defined OLLAMA_HOST set "OLLAMA_HOST=http://localhost:11434"
if not defined DB_PATH set "DB_PATH=%CD%\data\omm.db"

if "%~1"=="" (
  echo Starting on http://localhost:%PORT% ^(Ollama at %OLLAMA_HOST%, db %DB_PATH%^)
  where nvidia-smi >nul 2>nul
  if errorlevel 1 (
    echo GPU stats on Windows need an NVIDIA GPU with nvidia-smi on the PATH; CPU and memory work regardless.
  ) else (
    echo NVIDIA GPU stats come from nvidia-smi.
  )
)
"%BIN%" %*
exit /b %ERRORLEVEL%
