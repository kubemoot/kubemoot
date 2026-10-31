#!/usr/bin/env bash
# Runs the Open Memory Protocol Initiative's Python loader/validator on an
# export of crew memory. Not part of CI: it fetches the OMP repository.
#
#   scripts/omp-validate.sh                 # validate a generated fixture export
#   scripts/omp-validate.sh /path/to/export # validate an export you already wrote
#
# The OMP code is untrusted input: it is pinned to one commit, read from a temp
# directory, and run in isolated mode (python -I) from a different directory.
set -euo pipefail

OMP_REPO="https://github.com/The-AI-Disclosures-Project/Open-Memory-Protocol.git"
OMP_COMMIT="e18997919677e72c690fb33f7226faabdd776936"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

export_dir="${1:-}"
if [ -z "$export_dir" ]; then
  export_dir="$work/export"
  (cd "$root/operator" && OMP_FIXTURE_DIR="$export_dir" go test ./internal/crewmemory/omp -run TestWriteFixtureForValidator -count=1)
fi

mkdir "$work/omp"
git -C "$work/omp" init -q
git -C "$work/omp" fetch -q --depth 1 "$OMP_REPO" "$OMP_COMMIT"
git -C "$work/omp" checkout -q FETCH_HEAD

mkdir "$work/runner"
cat > "$work/runner/check.py" <<'PY'
import sys
from pathlib import Path

sys.path.insert(0, sys.argv[1])
from open_memory_protocol import load_memory
from open_memory_protocol.loader import build_harness_context

memory = load_memory(Path(sys.argv[2]))
ctx = build_harness_context(memory)
print(f"valid: {len(memory.core_files)} core file(s), {len(memory.external_files)} deferred file(s)")
for path in ctx.deferred_index:
    ctx.read_deferred(path)
print(f"read {len(ctx.deferred_index)} deferred file(s) on demand")
PY

python3 -I "$work/runner/check.py" "$work/omp/prototypes/python-loader-validator" "$export_dir"
