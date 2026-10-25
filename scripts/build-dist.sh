#!/usr/bin/env bash
# Cross-compiles every release archive into dist/ with a SHA256SUMS, the
# same layout the release workflow publishes. Used by CI for a dry run.
set -euo pipefail
cd "$(dirname "$0")/.."
rm -rf dist && mkdir -p dist
for p in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64; do
  os=${p%/*}; arch=${p#*/}; exe=""; [ "$os" = windows ] && exe=".exe"
  stage=$(mktemp -d)
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$stage/rmqa$exe" ./cmd/rmqa
  cp LICENSE NOTICE THIRD_PARTY_LICENSES.md README.md CHANGELOG.md "$stage/"
  if [ "$os" = windows ]; then
    (cd "$stage" && zip -qX "$OLDPWD/dist/rmqa_${os}_${arch}.zip" *)
  else
    tar -czf "dist/rmqa_${os}_${arch}.tar.gz" -C "$stage" .
  fi
  rm -rf "$stage"
done
(cd dist && sha256sum * > SHA256SUMS)
ls -l dist
