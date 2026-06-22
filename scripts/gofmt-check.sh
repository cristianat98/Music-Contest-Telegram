#!/usr/bin/env bash
# Fails (and lists offending files) if any given Go file is not
# gofmt-formatted. gofmt -l alone always exits 0, so pre-commit would
# silently pass without this wrapper.
set -euo pipefail

unformatted=$(gofmt -l "$@")
if [[ -n "$unformatted" ]]; then
  echo "The following files are not gofmt-formatted:"
  echo "$unformatted"
  echo "Run: gofmt -w $unformatted"
  exit 1
fi
