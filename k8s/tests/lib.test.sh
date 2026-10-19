#!/usr/bin/env bash
# Tests for the pure helpers in lib.sh.
# Usage: bash k8s/tests/lib.test.sh   (exit 0 = all passed)
set -euo pipefail

# shellcheck source=k8s/tests/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
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

check "the first Ready Ollama provider's endpoint" "http://ollama.ollama-cpu:11434" \
  "$(printf '%s\n' 'ollama false http://ollama.ollama-rig0:11434' 'vllm true ' \
       'ollama true http://ollama.ollama-cpu:11434' 'ollama true http://ollama.other:11434' \
     | first_ready_ollama_endpoint)"
check "nothing when no Ollama provider is Ready" "" \
  "$(printf '%s\n' 'ollama false http://ollama.ollama-rig0:11434' 'vllm true ' | first_ready_ollama_endpoint)"
check "nothing for a Ready Ollama provider with no endpoint" "" \
  "$(printf '%s\n' 'ollama true ' | first_ready_ollama_endpoint)"
check "nothing from no providers" "" "$(printf '' | first_ready_ollama_endpoint)"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
