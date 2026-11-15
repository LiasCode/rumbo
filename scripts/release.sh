#!/bin/sh
# Builds the release archives of rumbo: one per platform, each with the
# binary, the README, the changelog, and the license, and a SHA256SUMS file
# beside them.
#
#   scripts/release.sh <version> [directory]
#
# The version is written into the binary, which `rumbo version` prints.
set -eu

version=${1:?usage: scripts/release.sh <version> [directory]}
out=${2:-dist}
module=$(go list -m)
platforms="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

if [ -e "$out" ] && [ -n "$(ls -A "$out")" ]; then
	echo "release: $out is not empty; remove it or name another directory" >&2
	exit 1
fi

mkdir -p "$out"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

for platform in $platforms; do
	os=${platform%/*}
	arch=${platform#*/}
	name=rumbo-$version-$os-$arch

	mkdir "$stage/$name"

	# SQLite is pure Go here, so every platform builds without cgo.
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X $module/internal/cli.version=$version" \
		-o "$stage/$name/rumbo" ./cmd/rumbo

	for file in README.md CHANGELOG.md LICENSE; do
		if [ -f "$file" ]; then
			cp "$file" "$stage/$name/"
		fi
	done

	tar -C "$stage" -czf "$out/$name.tar.gz" "$name"
done

cd "$out"

if command -v sha256sum >/dev/null; then
	sha256sum ./*.tar.gz >SHA256SUMS
else
	shasum -a 256 ./*.tar.gz >SHA256SUMS
fi
