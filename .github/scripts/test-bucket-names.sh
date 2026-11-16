#!/usr/bin/env bash
# The discussion workspace's two Object Store bucket names are literals in five components. This
# check ties them together: each component must name both buckets, spelled the same, and none may
# name a variant. It reads sources across the repository, so it runs in CI where the whole tree
# exists, not in a component's own tests (the dashboard image builds from dashboard/ alone).
# Usage: bash .github/scripts/test-bucket-names.sh   (exit 0 = all agree)
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
workspace="kubemoot_workspace"
legacy="kubemoot_discussion_artifacts"

sources=(
  dashboard/src/lib/server/nats-object-store.ts
  agent-runtime/src/main/java/ai/kubemoot/agent/nats/DiscussionArtifacts.java
  discussion-gateway/internal/api/artifacts.go
  operator/pkg/mesh/names.go
  artifact-access/internal/envcompat/envcompat.go
)

failures=0
fail() { echo "FAIL $1"; failures=$((failures + 1)); }

for src in "${sources[@]}"; do
  file="${root}/${src}"
  if [ ! -f "$file" ]; then
    fail "${src}: missing"
    continue
  fi
  for name in "$workspace" "$legacy"; do
    grep -q "[\"']${name}[\"']" "$file" || fail "${src}: does not name ${name}"
  done
  # A near miss (a renamed or mistyped bucket) is a bucket nothing else reads.
  while IFS= read -r literal; do
    case "$literal" in
      "$workspace" | "$legacy") ;;
      *) fail "${src}: names ${literal}, which is neither ${workspace} nor ${legacy}" ;;
    esac
  done < <(grep -oE "[\"']kubemoot_(workspace|discussion_artifacts)[a-z_]*[\"']" "$file" | tr -d "\"'" | sort -u)
  echo "checked ${src}"
done

if [ "$failures" -gt 0 ]; then
  echo "${failures} bucket-name disagreement(s)"
  exit 1
fi
echo "all components agree on ${workspace} and ${legacy}"
