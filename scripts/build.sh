#!/usr/bin/env bash
# Builds the web UI, then cross-compiles the server for six targets into dist/.
# Each binary is zipped with README.md, HOW-TO-OPEN.txt, LICENSE and NOTICE, and
# dist/SHA256SUMS lists the artifacts.
#
# Usage: scripts/build.sh [--skip-zip] [--skip-web]
#   BUBBLES_VERSION overrides the version (default: git describe --tags --always --dirty, else "dev").
set -euo pipefail

SKIP_ZIP=0
SKIP_WEB=0
for arg in "$@"; do
  case "$arg" in
    --skip-zip) SKIP_ZIP=1 ;;
    --skip-web) SKIP_WEB=1 ;;
    -h|--help)
      echo "usage: scripts/build.sh [--skip-zip] [--skip-web]"
      echo "  --skip-web  reuse an existing internal/webui/dist (release workflow)"
      exit 0
      ;;
    *)
      echo "build.sh: unknown argument: $arg" >&2
      exit 2
      ;;
  esac
done

cd "$(dirname "$0")/.."

MODULE=github.com/donaldsteele/bubbles
TARGETS="windows/amd64 windows/arm64 darwin/amd64 darwin/arm64 linux/amd64 linux/arm64"

VERSION="${BUBBLES_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-s -w -X $MODULE/internal/buildinfo.Version=$VERSION -X $MODULE/internal/buildinfo.Commit=$COMMIT -X $MODULE/internal/buildinfo.Date=$DATE"

if [ "$SKIP_WEB" -eq 0 ]; then
  echo "==> web: npm ci + build"
  npm --prefix web ci
  npm --prefix web run build
else
  if [ ! -f internal/webui/dist/index.html ]; then
    echo "build.sh: --skip-web needs a built internal/webui/dist (index.html missing)" >&2
    exit 1
  fi
  echo "==> web: skipped (--skip-web)"
fi

echo "==> version: $VERSION"
rm -rf dist
mkdir -p dist/.stage

for target in $TARGETS; do
  goos=${target%/*}
  goarch=${target#*/}
  ext=""
  if [ "$goos" = windows ]; then
    ext=.exe
  fi
  name="bubbles-${VERSION}-${goos}-${goarch}"
  binary="dist/${name}${ext}"

  echo "==> build $name"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$binary" ./cmd/bubbles

  if [ "$SKIP_ZIP" -eq 0 ]; then
    stage="dist/.stage/$name"
    mkdir -p "$stage"
    cp "$binary" "$stage/bubbles${ext}"
    cp README.md HOW-TO-OPEN.txt LICENSE NOTICE "$stage/"
    go run ./tools/distzip -out "dist/${name}.zip" \
      "$stage/bubbles${ext}" "$stage/README.md" "$stage/HOW-TO-OPEN.txt" "$stage/LICENSE" "$stage/NOTICE"
  fi
done

rm -rf dist/.stage

if [ "$SKIP_ZIP" -eq 0 ]; then
  artifacts=(dist/*.zip)
else
  artifacts=(dist/bubbles-*)
fi

echo "==> dist/SHA256SUMS"
(
  cd dist
  names=()
  for f in "${artifacts[@]}"; do
    names+=("$(basename "$f")")
  done
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${names[@]}" > SHA256SUMS
  else
    shasum -a 256 "${names[@]}" > SHA256SUMS
  fi
)

echo "==> done: $(ls dist | wc -l | tr -d ' ') entries in dist/"
