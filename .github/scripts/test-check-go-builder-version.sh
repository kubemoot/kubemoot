#!/usr/bin/env bash
# Tests for check-go-builder-version.sh against small fake repositories, plus the real one.
# Usage: bash .github/scripts/test-check-go-builder-version.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="${here}/check-go-builder-version.sh"

failures=0
check() {
  local name="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    echo "ok   ${name}"
  else
    echo "FAIL ${name}: want [${want}] got [${got}]"
    failures=$((failures + 1))
  fi
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# module DIR GOMOD_LINES FROM_LINE: a module directory with a go.mod and a Dockerfile.
module() {
  mkdir -p "${work}/$1"
  printf 'module example.com/%s\n\n%b\n' "$1" "$2" >"${work}/$1/go.mod"
  printf '%s\nWORKDIR /build\n' "$3" >"${work}/$1/Dockerfile"
}

# run: prints the script's exit status; its output goes to run.log.
run() { bash "$script" "${work}" >"${work}/run.log" 2>&1 && echo 0 || echo $?; }

reset() { rm -rf "${work:?}"/*; }

module a 'go 1.27.2' 'FROM docker.io/library/golang:1.27.2-alpine3.23@sha256:abc AS builder'
check "a builder equal to the go directive passes" 0 "$(run)"
check "the pass names both versions" 1 "$(grep -c 'needs Go 1.27.2; .*builds on 1.27.2' "${work}/run.log")"

reset
module a 'go 1.27.2' 'FROM golang:1.27.1-alpine AS builder'
check "a builder older than the go directive fails" 1 "$(run)"
check "the failure names the needed and the builder version" 1 "$(grep -c 'needs Go 1.27.2 but the builder image is golang:1.27.1' "${work}/run.log")"

reset
module a 'go 1.27.2' 'FROM golang:1.28.0'
check "a newer builder passes" 0 "$(run)"

reset
module a 'go 1.27.2' 'FROM golang:1.27-alpine'
check "a minor-only builder covers every patch" 0 "$(run)"

reset
module a 'go 1.28.0' 'FROM golang:1.27-alpine'
check "a minor-only builder older in the minor fails" 1 "$(run)"

reset
module a 'go 1.27\n' 'FROM golang:1.27.1'
check "a go directive without a patch passes on a patch builder" 0 "$(run)"

reset
module a 'go 1.9.2' 'FROM golang:1.10.0'
check "versions compare as numbers, not text" 0 "$(run)"

reset
module a 'go 1.27.1\n\ntoolchain go1.27.3' 'FROM golang:1.27.2'
check "a toolchain line newer than the builder fails" 1 "$(run)"

reset
# shellcheck disable=SC2016
module a 'go 1.27.2' 'FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.2-alpine AS builder'
check "a platform flag and a registry prefix are read" 0 "$(run)"

reset
module a 'go 1.27.2' 'FROM golang:1.27.2 AS one\nFROM golang:1.26.0 AS two'
printf 'FROM golang:1.27.2 AS one\nFROM golang:1.26.0 AS two\n' >"${work}/a/Dockerfile"
check "every golang stage is checked" 1 "$(run)"

reset
module a 'go 1.27.2' 'FROM golang:1.27.2'
module b 'go 1.28.0' 'FROM golang:1.27.2'
check "one module behind fails the run" 1 "$(run)"
check "the passing module is still reported" 1 "$(grep -c 'a/go.mod needs Go 1.27.2' "${work}/run.log")"

reset
module a 'go 1.27.2' 'FROM golang:1.27.2'
module c 'go 1.99.0' 'FROM docker.io/library/python:3.14-slim'
check "a Dockerfile without a golang stage is ignored" 0 "$(run)"

reset
mkdir -p "${work}/a"
printf 'FROM golang:1.27.2\n' >"${work}/a/Dockerfile"
check "a golang Dockerfile without go.mod fails" 1 "$(run)"
check "the failure names the missing go.mod" 1 "$(grep -c 'does not exist' "${work}/run.log")"

reset
# shellcheck disable=SC2016
module a 'go 1.27.2' 'FROM golang:${GO_VERSION}'
check "a tag that is not a literal version fails" 1 "$(run)"

reset
module a 'no directive here' 'FROM golang:1.27.2'
check "a go.mod without a go directive fails" 1 "$(run)"

reset
module a 'go 1.27.2' 'FROM python:3'
check "no golang Dockerfile at all fails, so a moved check is noticed" 1 "$(run)"

check "a missing root fails" 1 "$(bash "$script" "${work}/nope" >/dev/null 2>&1 && echo 0 || echo $?)"

check "the real repository passes" 0 "$(bash "$script" "${here}/../.." >/dev/null 2>&1 && echo 0 || echo $?)"

if [ "$failures" -ne 0 ]; then
  echo "${failures} check(s) failed"
  exit 1
fi
echo "all checks passed"
