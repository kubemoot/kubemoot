#!/usr/bin/env bash
# Tests for export-quickstart-candidate.sh in a throwaway repository with release candidate
# tags and a tiny operator chart. `helm pull` and crane are stubbed (a directory stands in for
# Harbor); `helm package` and `helm template` are the real ones.
# Usage: RELEASE_LIB=<release-actions>/release-lib.sh bash .github/scripts/test-export-quickstart-candidate.sh
#        (exit 0 = all passed; in CI the release-actions setup action sets RELEASE_LIB)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
command -v helm >/dev/null || { echo "helm is required"; exit 1; }
[ -f "${RELEASE_LIB:-}" ] || { echo "RELEASE_LIB must point to release-lib.sh from kubemoot/release-actions"; exit 1; }
real_helm="$(command -v helm)"

failures=0
check() {
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; failures=$((failures + 1)); fi
}

root="$(mktemp -d)"
export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=gc.auto GIT_CONFIG_VALUE_0=0
trap 'rm -rf "$root"' EXIT
export LOG="${root}/calls.log" HARBOR_STATE="${root}/harbor"
mkdir -p "${root}/bin" "$HARBOR_STATE"
touch "$LOG"

cat > "${root}/bin/crane" <<'EOF2'
#!/usr/bin/env bash
echo "crane $*" >> "$LOG"
[ "$1" = "pull" ] || exit 3
shift
args=(); while [ $# -gt 0 ]; do case "$1" in --platform) shift ;; --insecure) ;; *) args+=("$1") ;; esac; shift; done
[ -z "${CRANE_MISSING:-}" ] || case "${args[0]}" in *"${CRANE_MISSING}"*) exit 1 ;; esac
echo "image ${args[0]}" > "${args[1]}"
EOF2
cat > "${root}/bin/helm" <<EOF2
#!/usr/bin/env bash
if [ "\$1" = pull ]; then
  echo "helm \$*" >> "\$LOG"
  version=""; dest=""
  while [ \$# -gt 0 ]; do case "\$1" in --version) version="\$2"; shift ;; --destination) dest="\$2"; shift ;; esac; shift; done
  [ -f "\$HARBOR_STATE/kubemoot-operator-\${version}.tgz" ] || exit 1
  cp "\$HARBOR_STATE/kubemoot-operator-\${version}.tgz" "\$dest/"
  exit 0
fi
exec "${real_helm}" "\$@"
EOF2
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
mkdir -p "$chart/templates" quickstart
cat > "$chart/Chart.yaml" <<'EOF2'
apiVersion: v2
name: kubemoot-operator
version: 0.0.0
EOF2
cat > "$chart/values.yaml" <<'EOF2'
dashboardOn: false
liaisonOn: false
EOF2
cat > "$chart/templates/all.yaml" <<'EOF2'
apiVersion: v1
kind: ConfigMap
metadata: {name: images}
data:
  operator: ghcr.io/kubemoot/kubemoot-operator:0.94.1-rc.3
  runtime: ghcr.io/kubemoot/agent-runtime:0.94.1-rc.3
  gateway: ghcr.io/kubemoot/discussion-gateway:0.94.1-rc.3
  same-again: ghcr.io/kubemoot/agent-runtime:0.94.1-rc.3
  other: ghcr.io/kubemoot/code-sandbox:0.94.1-rc.3
{{- if .Values.dashboardOn }}
  dashboard: ghcr.io/kubemoot/dashboard:0.94.1-rc.1
{{- end }}
{{- if .Values.core_missing }}
{{- end }}
EOF2
echo 'dashboardOn: true' > quickstart/operator-values.yaml
git add -A; git commit -q -m "feat: chart"
git push -q origin main 2>/dev/null
git tag operator-chart-v0.94.1-rc.2; git tag operator-chart-v0.94.1-rc.3
git push -q origin main --tags 2>/dev/null
git fetch -q origin
"$real_helm" package "$chart" --version 0.94.1-rc.3 --destination "$HARBOR_STATE" >/dev/null

export RELEASE_LIB REGISTRY=harbor.test
run() { # RC_TAG -> exit status; output in $root/out
  : > "$LOG"; rm -rf "${root}/export"
  GITHUB_OUTPUT="${root}/gh-output" OUT_DIR="${root}/export" RC_TAG="$1" bash "${here}/export-quickstart-candidate.sh" >"${root}/out" 2>&1 && echo 0 || echo 1
}
: > "${root}/gh-output"

check "exports the latest candidate" "0" "$(run latest)"
check "the chart is pulled from Harbor as chart.tgz" "yes" "$([ -s "${root}/export/chart.tgz" ] && echo yes || echo no)"
check "the step output names the candidate tag" "rc_tag=operator-chart-v0.94.1-rc.3" "$(grep '^rc_tag=' "${root}/gh-output")"
check "the step output names the chart version" "chart_version=0.94.1-rc.3" "$(grep '^chart_version=' "${root}/gh-output")"
check "one tarball per distinct component image, none for standalone images" "4" \
  "$(wc -l < "${root}/export/images.tsv" | tr -d ' ')"
check "an image is pulled from Harbor under its own tag" "1" "$(grep -c '^crane pull --insecure --platform linux/amd64 harbor.test/kubemoot/agent-runtime:0.94.1-rc.3 ' "$LOG")"
check "an image the quickstart does not run is not pulled" "0" "$(grep -c code-sandbox "$LOG")"
check "the map names the reference the chart uses" "1" \
  "$(grep -cP '^images/\d+\.tar\tghcr.io/kubemoot/dashboard:0.94.1-rc.1$' "${root}/export/images.tsv")"
check "the exported map holds no Harbor host" "0" "$(grep -c harbor.test "${root}/export/images.tsv" "${root}/export/images.txt" | awk -F: '{s+=$2} END {print s+0}')"
check "the quickstart values of the candidate decide the images (dashboard on)" "1" "$(grep -c dashboard "${root}/export/images.txt")"

check "an exact candidate tag exports that candidate" "0" "$(run operator-chart-v0.94.1-rc.3)"
check "a tag that is not a candidate fails" "1" "$(run operator-chart-v9.9.9)"
check "an image missing from Harbor fails" "1" "$(CRANE_MISSING=discussion-gateway run latest)"
check "the failure names the missing image" "1" "$(grep -c 'cannot pull harbor.test/kubemoot/discussion-gateway:0.94.1-rc.3' "${root}/out")"

# A chart that pins agent-runtime by digest is pulled by digest and keeps its reference.
dg=$(printf 'b%.0s' {1..64})
sed -i "s|agent-runtime:0.94.1-rc.3|agent-runtime@sha256:${dg}|" "$chart/templates/all.yaml"
git commit -q -am "fix: pin the runtime by digest"; git tag operator-chart-v0.94.1-rc.4
"$real_helm" package "$chart" --version 0.94.1-rc.4 --destination "$HARBOR_STATE" >/dev/null
git push -q origin main --tags 2>/dev/null; git fetch -q origin
check "a digest-pinned chart exports" "0" "$(run latest)"
check "the digest-pinned image is pulled from Harbor by digest" "1" "$(grep -c "^crane pull --insecure --platform linux/amd64 harbor.test/kubemoot/agent-runtime@sha256:${dg} " "$LOG")"
check "the map keeps the digest reference" "1" "$(grep -c "ghcr.io/kubemoot/agent-runtime@sha256:${dg}\$" "${root}/export/images.tsv")"

# A chart that pins no agent-runtime is refused.
sed -i '/agent-runtime/d' "$chart/templates/all.yaml"
git commit -q -am "fix: drop the runtime"; git tag operator-chart-v0.94.1-rc.5
"$real_helm" package "$chart" --version 0.94.1-rc.5 --destination "$HARBOR_STATE" >/dev/null
git push -q origin main --tags 2>/dev/null; git fetch -q origin
check "a chart without a core image is refused" "1" "$(run latest)"
check "the refusal names the core image" "1" "$(grep -c 'pins no agent-runtime image' "${root}/out")"

# A candidate tag whose chart is not in Harbor fails.
git commit -q --allow-empty -m "fix: unbuilt"; git tag operator-chart-v0.94.1-rc.6
git push -q origin main --tags 2>/dev/null; git fetch -q origin
check "a candidate whose chart is not in Harbor fails" "1" "$(run latest)"
check "the failure says the chart is missing from Harbor" "1" "$(grep -c 'cannot read the candidate operator chart 0.94.1-rc.6 from Harbor' "${root}/out")"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
