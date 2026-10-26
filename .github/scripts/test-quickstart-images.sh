#!/usr/bin/env bash
# Tests for quickstart-images.sh.
# Usage: bash .github/scripts/test-quickstart-images.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=/dev/null
source "${here}/quickstart-images.sh"
failures=0
check() {
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; failures=$((failures + 1)); fi
}
status() { if "$@" >/dev/null 2>&1; then echo 0; else echo 1; fi; }

d=$(printf 'a%.0s' {1..64})
manifests="image: ghcr.io/kubemoot/kubemoot-operator:0.94.1-rc.3
image: ghcr.io/kubemoot/agent-runtime:0.94.1-rc.3
image: ghcr.io/kubemoot/agent-runtime:0.94.1-rc.3
image: \"ghcr.io/kubemoot/discussion-gateway:0.94.1-rc.3\"
image: ghcr.io/kubemoot/code-sandbox:0.94.1-rc.3
url: http://dashboard.kubemoot.svc/dashboard
image: ghcr.io/kubemoot/dashboard:0.94.1-rc.1"

check "distinct component images, standalone images and paths left out" \
  "ghcr.io/kubemoot/agent-runtime:0.94.1-rc.3 ghcr.io/kubemoot/dashboard:0.94.1-rc.1 ghcr.io/kubemoot/discussion-gateway:0.94.1-rc.3 ghcr.io/kubemoot/kubemoot-operator:0.94.1-rc.3" \
  "$(quickstart_images <<<"$manifests" | paste -sd' ')"
check "a digest-pinned image is found" "reg.example/kubemoot/agent-runtime@sha256:${d}" \
  "$(quickstart_images <<<"image: reg.example/kubemoot/agent-runtime@sha256:${d}")"
check "a tag with a digest is found whole" "reg.example/kubemoot/agent-runtime:1.0.0-rc.1@sha256:${d}" \
  "$(quickstart_images <<<"image: reg.example/kubemoot/agent-runtime:1.0.0-rc.1@sha256:${d}")"
check "a short digest is not an image" "" "$(quickstart_images <<<"image: reg.example/kubemoot/agent-runtime@sha256:abc")"
check "the parts of a tagged reference" "agent-runtime|0.94.1-rc.3|" \
  "$(r=ghcr.io/kubemoot/agent-runtime:0.94.1-rc.3; echo "$(image_name "$r")|$(image_tag "$r")|$(image_digest "$r")")"
check "the parts of a digest reference" "agent-runtime||sha256:${d}" \
  "$(r=reg.example/kubemoot/agent-runtime@sha256:${d}; echo "$(image_name "$r")|$(image_tag "$r")|$(image_digest "$r")")"
check "the parts of a tag and digest reference" "agent-runtime|1.0.0|sha256:${d}" \
  "$(r=reg.example:5000/kubemoot/agent-runtime:1.0.0@sha256:${d}; echo "$(image_name "$r")|$(image_tag "$r")|$(image_digest "$r")")"

check "core present passes" "0" "$(quickstart_images <<<"$manifests" | status quickstart_require_core)"
check "a missing core component fails" "1" \
  "$(quickstart_images <<<"$(grep -v agent-runtime <<<"$manifests")" | status quickstart_require_core)"
check "no images at all fails" "1" "$(quickstart_images <<<"kind: ConfigMap" | status quickstart_require_core)"
check "no images at all says so" "ERROR: the chart names no Kubemoot component image" \
  "$(quickstart_images <<<"kind: ConfigMap" | quickstart_require_core 2>&1 || true)"
check "the CLI prints the list" "4" "$(bash "${here}/quickstart-images.sh" <<<"$manifests" | wc -l | tr -d ' ')"
check "the CLI fails on no images" "1" "$(bash "${here}/quickstart-images.sh" <<<"nothing" >/dev/null 2>&1 && echo 0 || echo 1)"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
