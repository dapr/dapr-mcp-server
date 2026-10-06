#!/usr/bin/env bash
# Fail if any tracked Go files are not gofmt-clean.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

unformatted=$(gofmt -l . 2>/dev/null | grep -Ev '^(\.venv/|test/\.venv/)' || true)
if [ -n "$unformatted" ]; then
  echo "unformatted Go files:" >&2
  echo "$unformatted" >&2
  echo "fix with: gofmt -w <file>" >&2
  exit 1
fi
