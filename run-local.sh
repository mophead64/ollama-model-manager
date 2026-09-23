#!/usr/bin/env bash
# Builds the app into ./bin and runs it natively (outside Docker) on Linux or
# macOS. On Windows, use run-local.bat instead.
#
#   ./run-local.sh                  build and start the server
#   ./run-local.sh reset-password   build and run a subcommand instead
#
# Settings come from the environment, with local-friendly defaults:
#   PORT         (8080)
#   OLLAMA_HOST  (http://localhost:11434)
#   DB_PATH      (./data/omm.db, so the login persists between runs)
#   MODELS_DIR   (auto-detected: ~/.ollama/models, /usr/share/ollama/.ollama/models...)
set -euo pipefail

cd "$(dirname "$0")"

if ! command -v go >/dev/null 2>&1; then
  echo "Go isn't installed or isn't on the PATH: see https://go.dev/doc/install" >&2
  exit 1
fi

version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)-local"
bin=bin/ollama-model-manager

echo "Building ${bin} (${version})..."
mkdir -p bin
go build -trimpath \
  -ldflags="-X github.com/mophead64/ollama-model-manager/internal/version.Version=${version}" \
  -o "$bin" ./cmd/ollama-model-manager

export PORT="${PORT:-8080}"
export OLLAMA_HOST="${OLLAMA_HOST:-http://localhost:11434}"
export DB_PATH="${DB_PATH:-$PWD/data/omm.db}"

if [ $# -eq 0 ]; then
  case "$(uname -s)" in
    Darwin) gpu="Apple silicon GPU stats come from ioreg; no setup needed." ;;
    Linux)
      if command -v nvidia-smi >/dev/null 2>&1; then
        gpu="NVIDIA GPU stats come from nvidia-smi."
      else
        gpu="AMD/Intel GPUs are read from /sys. For NVIDIA GPU stats, install the driver so nvidia-smi is on the PATH."
      fi
      ;;
    *) gpu="" ;;
  esac
  echo "Starting on http://localhost:${PORT} (Ollama at ${OLLAMA_HOST}, db ${DB_PATH})"
  [ -n "$gpu" ] && echo "$gpu"
fi
exec "$bin" "$@"
