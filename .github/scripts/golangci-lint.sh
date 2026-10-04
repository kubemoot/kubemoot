#!/usr/bin/env bash
# Runs golangci-lint on the Go module in the current directory with the shared
# .golangci.yml at the repository root. The linter version is the one pin in
# operator/Makefile (GOLANGCI_LINT_VERSION), so every module and `make lint` agree.
set -euo pipefail

repo="$(git rev-parse --show-toplevel)"
version="$(sed -n 's/^GOLANGCI_LINT_VERSION ?= //p' "${repo}/operator/Makefile")"
if [ -z "${version}" ]; then
  echo "::error::GOLANGCI_LINT_VERSION not found in operator/Makefile" >&2
  exit 1
fi

go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${version}"
"$(go env GOPATH)/bin/golangci-lint" run ./...
