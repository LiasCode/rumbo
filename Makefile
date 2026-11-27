# The checks of AGENTS.md, and the release. See AGENTS.md, "Releasing".
.PHONY: check next release

# The checks every change runs.
check:
	test -z "$$(gofmt -l . | tee /dev/stderr)"
	go vet ./...
	GOOS=darwin go vet ./...
	GOOS=windows go vet ./...
	go test ./...
	go build -o bin/rumbo ./cmd/rumbo

# Says which version the next release would be, and the commits that decide it.
next:
	go run ./scripts/bump -n

# Runs the checks, then commits the changelog dated with the next version and
# tags that commit. Pushing the tag publishes the release.
release: check
	go run ./scripts/bump
