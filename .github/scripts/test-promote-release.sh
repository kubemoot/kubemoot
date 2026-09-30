#!/usr/bin/env bash
# Tests for promote-release.sh: a throwaway repository with an operator chart, release
# candidate tags, and a bare origin; crane and `helm push` are stubbed, `helm package`
# is the real one.
# Usage: bash .github/scripts/test-promote-release.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
command -v helm >/dev/null || { echo "helm is required"; exit 1; }
real_helm="$(command -v helm)"

failures=0
check() {
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; failures=$((failures + 1)); fi
}

root="$(mktemp -d)"
trap 'rm -rf "$root"' EXIT
export LOG="${root}/calls.log" GHCR_STATE="${root}/ghcr"
mkdir -p "${root}/bin" "$GHCR_STATE"
touch "$LOG"

# crane stub: Harbor digests are derived from the reference; a release-registry
# reference exists once copied (or planted by a test).
cat > "${root}/bin/crane" <<'EOF'
#!/usr/bin/env bash
echo "crane $*" >> "$LOG"
[ "$1" = "--insecure" ] && shift
args=("$@"); [ "${args[1]:-}" = "--insecure" ] && args=("${args[0]}" "${args[@]:2}")
case "${args[0]}" in
  auth) exit 0 ;;
  digest)
    ref="${args[1]}"
    case "$ref" in
      ghcr.test/*) f="$GHCR_STATE/$(echo "$ref" | tr '/:' '__')"; [ -f "$f" ] && cat "$f" && exit 0; exit 1 ;;
      *) echo "sha256:$(echo "${ref%:*}" | sha256sum | cut -c1-16)" ;;
    esac ;;
  copy)
    dst="${args[2]}"; src="${args[1]}"
    echo "${src#*@}" > "$GHCR_STATE/$(echo "$dst" | tr '/:' '__')" ;;
  tag) exit 0 ;;
esac
EOF
cat > "${root}/bin/helm" <<EOF
#!/usr/bin/env bash
case "\$1" in
  push|registry) echo "helm \$*" >> "\$LOG"; exit 0 ;;
esac
exec "${real_helm}" "\$@"
EOF
chmod +x "${root}/bin/crane" "${root}/bin/helm"
export PATH="${root}/bin:${PATH}"

# Fixture repository.
git init -q --bare "${root}/origin.git"
git clone -q "${root}/origin.git" "${root}/repo" 2>/dev/null
cd "${root}/repo"
git config user.email test@example.com
git config user.name test
git checkout -q -b main
chart=operator/chart/kubemoot-operator
dash=dashboard/charts/kubemoot-dashboard
mkdir -p "$chart" "$dash"
write_chart() {
  printf 'apiVersion: v2\nname: kubemoot-operator\nversion: %s\nappVersion: "%s"\n' "$1" "$2" > "$chart/Chart.yaml"
  cat > "$chart/values.yaml" <<EOF
verify:
  image: test-runner:0.46.2
liaison:
  image: crew-liaison:$4
config:
  images:
    agentRuntime: agent-runtime:$3
    mcpGateway: mcp-gateway:0.326.22
EOF
}
printf 'apiVersion: v2\nname: kubemoot-dashboard\nversion: 0.50.0\nappVersion: 0.50.0\n' > "$dash/Chart.yaml"
write_chart 0.92.581 0.343.40 0.342.31 0.346.31
git add -A; git commit -q -m "chore: init"
git tag -a v0.343.40 -m f; git tag -a agent-runtime-v0.342.31 -m f

git commit -q --allow-empty -m "feat(runtime): stream tokens"
git tag -a agent-runtime-v0.342.32-rc.3 -m rc
git commit -q --allow-empty -m "fix: code sandbox limit"
git tag -a code-sandbox-v0.16.32-rc.1 -m rc
git tag -a crew-liaison-v0.346.32-rc.0 -m rc
git tag -a v0.343.41-rc.2 -m rc
sed -i 's/^version:.*/version: 0.50.1-rc.0/; s/^appVersion:.*/appVersion: 0.50.1-rc.0/' "$dash/Chart.yaml"
git commit -q -am "chore: update dashboard Helm chart to v0.50.1-rc.0 [skip ci]"
git tag -a dashboard-v0.50.1-rc.0 -m rc
write_chart 0.92.582-rc.1 0.343.41-rc.2 0.342.32-rc.3 0.346.32-rc.0
git commit -q -am "chore: update pins, operator chart to v0.92.582-rc.1 [skip ci]"
git tag -a operator-chart-v0.92.582-rc.1 -m rc
candidate=$(git rev-parse HEAD)
git commit -q --allow-empty -m "fix: later work not yet built"
git tag -a artifact-access-v0.342.33-rc.0 -m rc
git push -q origin main --tags 2>/dev/null
git fetch -q origin

export REGISTRY=harbor.test RELEASE_REGISTRY=ghcr.test/kubemoot
export HARBOR_USERNAME=u HARBOR_PASSWORD=p GHCR_USERNAME=u GHCR_TOKEN=t
run_promote() { OUT_DIR="${root}/out-$1" bash "${here}/promote-release.sh" > "${root}/run-$1.log" 2>&1; }

# 1. Dry run of the latest candidate: plans, packages, publishes nothing.
DRY_RUN=true RC_TAG=latest run_promote dry && status=0 || status=$?
check "dry run succeeds" 0 "$status"
out="$(cat "${root}/run-dry.log")"
check "plans the operator image" 1 "$(grep -c 'image kubemoot-operator: 0.343.41-rc.2 .* -> 0.343.41' <<<"$out")"
check "plans a pinned component" 1 "$(grep -c 'image agent-runtime: 0.342.32-rc.3 .* -> 0.342.32' <<<"$out")"
check "plans the liaison pin" 1 "$(grep -c 'image crew-liaison: 0.346.32-rc.0' <<<"$out")"
check "plans a standalone image" 1 "$(grep -c 'image code-sandbox: 0.16.32-rc.1' <<<"$out")"
check "plans the standalone dashboard" 1 "$(grep -c 'image dashboard: 0.50.1-rc.0' <<<"$out")"
check "skips a standalone candidate newer than the chart candidate" 0 "$(grep -c 'image artifact-access' <<<"$out" || true)"
check "leaves a final pin alone" 0 "$(grep -c 'image mcp-gateway' <<<"$out" || true)"
check "dry run copies nothing" 0 "$(grep -c '^crane copy' "$LOG" || true)"
check "dry run pushes no chart" 0 "$(grep -c '^helm push' "$LOG" || true)"
check "dry run creates no tag" "" "$(git tag -l 'operator-chart-v0.92.582')"
tgz="${root}/out-dry/kubemoot-operator-0.92.582.tgz"
check "packages the final operator chart" 1 "$([ -f "$tgz" ] && echo 1 || echo 0)"
values="$(tar -xzOf "$tgz" kubemoot-operator/values.yaml)"
check "final chart pins the final runtime" 1 "$(grep -c 'agent-runtime:0.342.32$' <<<"$values")"
check "final chart pins no candidate" 0 "$(grep -c -- '-rc\.' <<<"$values" || true)"
check "final chart appVersion" 1 "$(tar -xzOf "$tgz" kubemoot-operator/Chart.yaml | grep -cE '^appVersion: "?0.343.41"?$')"
check "packages the final dashboard chart" 1 "$([ -f "${root}/out-dry/kubemoot-dashboard-0.50.1.tgz" ] && echo 1 || echo 0)"
check "notes list the feature" 1 "$(grep -c '^- feat(runtime): stream tokens' "${root}/out-dry/notes.md")"
check "notes stop at the candidate" 0 "$(grep -c 'later work' "${root}/out-dry/notes.md" || true)"

# 2. Unexpected inputs.
DRY_RUN=true RC_TAG=v9.9.9-rc.1 run_promote badtag && status=0 || status=$?
check "refuses an unknown tag" 1 "$status"
DRY_RUN=true RC_TAG=v0.343.40 run_promote final && status=0 || status=$?
check "refuses a final tag as input" 1 "$status"
echo "sha256:someotherdigest" > "${GHCR_STATE}/ghcr.test_kubemoot_agent-runtime_0.342.32"
DRY_RUN=true RC_TAG=latest run_promote clash && status=0 || status=$?
check "refuses a published version with another digest" 1 "$status"
check "names the clash" 1 "$(grep -c 'exists with digest' "${root}/run-clash.log")"
rm "${GHCR_STATE}/ghcr.test_kubemoot_agent-runtime_0.342.32"

# 3. The real promotion from an explicit candidate tag.
: > "$LOG"
DRY_RUN=false RC_TAG=operator-chart-v0.92.582-rc.1 run_promote real && status=0 || status=$?
check "promotion succeeds" 0 "$status"
[ "$status" -eq 0 ] || sed 's/^/    /' "${root}/run-real.log"
git fetch -q origin --tags
check "chart final tag on the candidate commit" "$candidate" "$(git rev-list -n 1 operator-chart-v0.92.582 2>/dev/null)"
check "component final tag on its candidate commit" "$(git rev-list -n 1 agent-runtime-v0.342.32-rc.3)" "$(git rev-list -n 1 agent-runtime-v0.342.32 2>/dev/null)"
check "operator final tag" "$(git rev-list -n 1 v0.343.41-rc.2)" "$(git rev-list -n 1 v0.343.41 2>/dev/null)"
check "notes of the real run start at the previous release" 1 "$(grep -c '^## Changes since v0.343.40' "${root}/out-real/notes.md")"
check "notes of the real run list the feature" 1 "$(grep -c '^- feat(runtime): stream tokens' "${root}/out-real/notes.md")"
check "names the GitHub Release" "operator-chart-v0.92.582|Kubemoot 0.92.582|notes.md" "$(tr '\t' '|' < "${root}/out-real/releases.tsv")"
check "leaves no scratch worktree" 1 "$(git worktree list | wc -l | tr -d ' ')"
check "copies five images" 5 "$(grep -c '^crane copy' "$LOG")"
check "copies by digest" 5 "$(grep '^crane copy' "$LOG" | grep -c '@sha256:')"
check "pushes two charts to both registries" 4 "$(grep -c '^helm push' "$LOG")"

# 4. A second promotion of the same candidate is refused.
DRY_RUN=false RC_TAG=latest run_promote again && status=0 || status=$?
check "refuses a candidate already released" 1 "$status"
check "says it is released" 1 "$(grep -c 'already released' "${root}/run-again.log")"

if [ "$failures" -ne 0 ]; then echo "${failures} test(s) failed"; exit 1; fi
echo "all promote-release tests passed"
