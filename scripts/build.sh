#!/usr/bin/env bash
# Build both executables for Windows and Linux with one uniform naming scheme:
#   dist/kcptun-<role>-<os>-<arch>[.exe]
set -euo pipefail
cd "$(dirname "$0")/.."

GO="${GO:-go}"
VERSION="${VERSION:-$(sed -n 's/^var Version = "\([^"]*\)".*/\1/p' internal/config/config.go | head -1)}"
OUT="${OUT:-dist}"
mkdir -p "$OUT"

LDFLAGS="-s -w -X kcptun-proxy/internal/config.Version=${VERSION}"

build_one() {
  local os="$1" arch="$2" suffix="$3" role name
  for role in server client; do
    name="kcptun-${role}-${os}-${arch}${suffix}"
    echo "build -> ${OUT}/${name}"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
      "$GO" build -trimpath -ldflags "$LDFLAGS" -o "${OUT}/${name}" "./cmd/kcptun-${role}"
    if [ -z "$suffix" ]; then
      chmod +x "${OUT}/${name}"
    fi
  done
}

build_one windows amd64 ".exe"
build_one linux amd64 ""

echo "built ${VERSION} (4 artifacts):"
ls -1 "$OUT"/kcptun-*
