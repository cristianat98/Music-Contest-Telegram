# Music-Contest-Telegram

## Development setup

This repo uses [pre-commit](https://pre-commit.com) for local checks
(gofmt, go vet, go build, go test -short, golangci-lint) and enforces
[Conventional Commits](https://www.conventionalcommits.org/) on commit
messages, which drive automatic semver tagging on merge to `master`.

```bash
pip install pre-commit
pre-commit install
pre-commit install --hook-type commit-msg
```

Commit messages must start with a type, e.g. `feat:`, `fix:`, `docs:`,
`refactor:`, `chore:`. A `feat:` commit bumps the minor version, `fix:`
bumps patch, and a `BREAKING CHANGE:` footer (or `!` after the type) bumps
major.
