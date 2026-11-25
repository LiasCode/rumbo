#!/bin/sh
# Builds the release archives of rumbo: one per platform, each with the
# binary, the README, the changelog, and the license, and a SHA256SUMS file
# beside them. Windows' are zip files, which it opens by itself; the others
# are gzipped tarballs.
#
#   scripts/release.sh <version> [directory]
#
# The version is written into the binary, which `rumbo version` prints.
set -eu

version=${1:?usage: scripts/release.sh <version> [directory]}
out=${2:-dist}
module=$(go list -m)
platforms="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"

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
	binary=rumbo

	if [ "$os" = windows ]; then
		binary=rumbo.exe
	fi

	mkdir "$stage/$name"

	# Nothing here needs cgo, so every platform builds without it.
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X $module/internal/cli.version=$version" \
		-o "$stage/$name/$binary" ./cmd/rumbo

	for file in README.md CHANGELOG.md LICENSE; do
		if [ -f "$file" ]; then
			cp "$file" "$stage/$name/"
		fi
	done

	if [ "$os" = windows ]; then
		go run ./scripts/zipdir "$out/$name.zip" "$stage/$name"
	else
		tar -C "$stage" -czf "$out/$name.tar.gz" "$name"
	fi
done

cd "$out"

if command -v sha256sum >/dev/null; then
	sha256sum ./*.tar.gz ./*.zip >SHA256SUMS
else
	shasum -a 256 ./*.tar.gz ./*.zip >SHA256SUMS
fi
