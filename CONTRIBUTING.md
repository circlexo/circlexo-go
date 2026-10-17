# Contributing to circlexo-go

Thanks for wanting to help. This guide covers how to get a local setup running, the conventions
the codebase follows, and how a change gets merged.

## Ways to contribute

- Report a bug or request a feature through the [issue forms](https://github.com/circlexo/circlexo-go/issues/new/choose).
- Pick up an issue labelled [`good first issue`](https://github.com/circlexo/circlexo-go/labels/good%20first%20issue)
  or [`help wanted`](https://github.com/circlexo/circlexo-go/labels/help%20wanted). Comment on it first so nobody
  duplicates the work.
- Improve the docs. Typos and unclear sentences are worth a PR.

Security problems are **not** issues. Follow [SECURITY.md](SECURITY.md) instead.

## Local setup

Requirements: Go (version in `go.mod`). No services are needed: the tests run against the in-memory hub in `circlexotest`.

```bash
git clone https://github.com/circlexo/circlexo-go.git && cd circlexo-go
go test ./...
```

`TestLive` runs against a real hub only when `CIRCLEXO_LIVE_ORG` (and the `CIRCLEXO_*` config) is set.

## Before you open a pull request

```bash
gofmt -l .        # must print nothing
go vet ./...
go test -race ./...
```

CI runs the same. Public API changes need a test against `circlexotest` and a README update.

## Conventions

- **Small, focused PRs.** One concern per PR; refactors separate from behaviour changes.
- **Branch names:** `feat/…`, `fix/…`, `docs/…`, `chore/…` off `main`.
- **Commit messages:** [Conventional Commits](https://www.conventionalcommits.org/) style
  (`feat: …`, `fix: …`, `docs: …`), imperative mood, explain *why* in the body.
- **Tests:** a bug fix comes with a test that fails without it; a feature comes with tests for its
  main path and its permission checks.
- **No secrets** in code, tests, fixtures or screenshots. Configuration goes in `.env`
  (documented in `.env.example`).
- **AI-assisted contributions are welcome.** Agents should read [AGENTS.md](AGENTS.md). You are
  responsible for every line you submit: review it, run it, and test it as if you wrote it.

## Developer Certificate of Origin (DCO)

Every commit must be signed off, certifying the [Developer Certificate of Origin](https://developercertificate.org/):
you wrote the change, or otherwise have the right to submit it under the project's license.

```bash
git commit -s -m "fix: describe the change"
```

This adds a `Signed-off-by: Your Name <you@example.com>` trailer that matches your Git identity.
Forgot? `git commit --amend -s` for the last commit, or `git rebase --signoff main` for a branch.

## License

circlexo-go is licensed under the [GNU AGPL-3.0](LICENSE). By contributing you agree your contribution
is licensed under the same terms.

## Releases

Record user-visible changes under `[Unreleased]` in [CHANGELOG.md](CHANGELOG.md).
Before releasing, run the checks above and review the changes since the latest
tag. Move the unreleased entries into a dated `X.Y.Z` section, update the compare
links, and commit the changelog on `main`.

A maintainer then tags that tested commit with an annotated `vX.Y.Z` tag and
pushes the tag. For example, after choosing `0.3.0` as the next version:

```bash
git tag -a v0.3.0 -m "Release v0.3.0"
git push origin v0.3.0
```

Use the chosen version in place of the example. Go consumers can then pin it
with `go get github.com/circlexo/circlexo-go@v0.3.0`. Release tagging is a
maintainer action; a contribution PR should only add its changelog entry.

## Community

- **Questions and ideas:** [GitHub Discussions](https://github.com/circlexo/circlexo-go/discussions)
- **Bugs and feature requests:** [Issues](https://github.com/circlexo/circlexo-go/issues/new/choose)

Everyone taking part is expected to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
