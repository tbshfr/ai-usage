#!/usr/bin/env bash
# Cross-compile release binaries into dist/ and generate checksums.
#
# Usage: scripts/release.sh [VERSION]
#   VERSION defaults to `git describe --tags --always --dirty` (or "dev").
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
DIST=dist
LDFLAGS="-s -w -X main.version=${VERSION}"

rm -rf "${DIST}"
mkdir -p "${DIST}"

build() {
    local goos="$1" goarch="$2" name="$3"
    echo "building ${name}"
    CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
        go build -trimpath -ldflags="${LDFLAGS}" -o "${DIST}/${name}" ./cmd/ai-usage
}

build linux  amd64 ai-usage-linux-amd64
# build linux  arm64 ai-usage-linux-arm64
# build darwin amd64 ai-usage-darwin-amd64
# build darwin arm64 ai-usage-darwin-arm64
# build windows amd64 ai-usage-windows-amd64.exe

sha256sum "${DIST}"/* > "${DIST}/checksums.txt"
echo
cat "${DIST}/checksums.txt"
echo
echo "version: ${VERSION}"
echo "artifacts in ${DIST}/"
