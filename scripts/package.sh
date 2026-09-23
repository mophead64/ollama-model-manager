#!/usr/bin/env bash
# Cross-compiles the standalone binaries and packages each with the README and
# LICENSE: .tar.gz for Linux and macOS, .zip for Windows, plus SHA256SUMS.
# Used by the release workflow; runs anywhere with Go, tar, zip and a
# sha256sum or shasum.
#
#   scripts/package.sh <version> [out-dir]     e.g. scripts/package.sh v2026.09.23 dist/release
#
# The SQLite driver is pure Go, so CGO is off and every target builds from any
# machine.
set -euo pipefail

version="${1:?usage: scripts/package.sh <version> [out-dir]}"
out="${2:-dist/release}"
name=ollama-model-manager
targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)

cd "$(dirname "$0")/.."
rm -rf "$out"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

for target in "${targets[@]}"; do
  os="${target%/*}" arch="${target#*/}"
  exe="$name"
  [ "$os" = windows ] && exe="$name.exe"
  pkg="$name-$version-$os-$arch"
  dir="$stage/$pkg"
  mkdir -p "$dir"

  echo "Building $pkg"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags="-s -w -X github.com/mophead64/ollama-model-manager/internal/version.Version=$version" \
    -o "$dir/$exe" ./cmd/ollama-model-manager
  cp README.md LICENSE "$dir/"

  if [ "$os" = windows ]; then
    (cd "$stage" && zip -qr "$out/$pkg.zip" "$pkg")
  else
    tar -C "$stage" -czf "$out/$pkg.tar.gz" "$pkg"
  fi
done

cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum -- *.tar.gz *.zip > SHA256SUMS
else
  shasum -a 256 -- *.tar.gz *.zip > SHA256SUMS
fi
echo "Packages in $out:"
ls -1 "$out"
