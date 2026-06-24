# Music-Contest-Telegram

## Development setup

This repo uses [pre-commit](https://pre-commit.com) for local checks
(gofmt, go vet, go build, golangci-lint).

```bash
pip install pre-commit
pre-commit install
```

[Conventional Commits](https://www.conventionalcommits.org/) are still
used as the commit message convention, driving automatic semver tagging
on merge to `master`: a `feat:` commit bumps the minor version, `fix:`
bumps patch, and a `BREAKING CHANGE:` footer (or `!` after the type) bumps
major. This is no longer enforced locally by a pre-commit hook.

Each new tag triggers [GoReleaser](https://goreleaser.com) to publish a
GitHub Release with `linux/amd64` and `linux/arm64` binaries attached (see
`.goreleaser.yaml`).
