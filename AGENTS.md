# rumbo: agent guide

rumbo is a general agent harness. Before changing anything, read the docs
for the area you touch; they hold the design and the rules a change must
not break:

- [docs/architecture.md](docs/architecture.md): the parts, how they fit,
  and "Rules the design depends on". Read it first.
- [docs/use-cases.md](docs/use-cases.md): what the host, providers, front
  ends, and extensions must, can, and must not do, including what the
  harness deliberately is not.
- [docs/glossary.md](docs/glossary.md): the terms.
- [docs/plugins.md](docs/plugins.md): plugins, manifests, replacing a tool.
- [docs/protocol.md](docs/protocol.md),
  [docs/frontend-protocol.md](docs/frontend-protocol.md),
  [docs/extension-protocol.md](docs/extension-protocol.md): the public
  protocols.
- [evals/README.md](evals/README.md): the evaluation set.

## Commands

```sh
gofmt -l .          # must print nothing
go vet ./...
GOOS=darwin go vet ./...    # it builds for every system it runs on
GOOS=windows go vet ./...
go test ./...
go build -o bin/rumbo ./cmd/rumbo
```

`make check` runs them all.

## Releasing

The commits since the last tag decide the version, so their Conventional
Commits types matter: a breaking change (`!` or a `BREAKING CHANGE:`
footer) raises the major number, or the minor one before 1.0; `feat` the
minor; `fix` and `perf` the patch; other types release nothing.

1. `make next` says which version that is, and the commits that decide it.
2. `make release` runs the checks, puts `## X.Y.Z (YYYY-MM-DD)` under
   `## Unreleased` in CHANGELOG.md, commits it as `chore: release X.Y.Z`,
   and tags the commit `vX.Y.Z` (`scripts/bump`). It refuses uncommitted
   changes and an empty `Unreleased` section.
3. `git push --atomic origin main vX.Y.Z`. The release workflow runs the
   checks, builds the archives with `scripts/release.sh`, and publishes them
   with that section of the changelog as their notes. It refuses a tree
   without a `LICENSE`, or a changelog without that section.

1.0, or any version the commits do not give, is still released by hand:
the same changelog heading, commit, and tag.

`scripts/release.sh X.Y.Z <directory>` builds the same archives locally.
