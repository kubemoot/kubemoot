#!/usr/bin/env bash
# Tests for the release scripts: release-operator-chart.sh builds chart candidates in a
# throwaway repository (charts at 0.0.0, release candidate tags, a bare origin), and
# publish-release.sh publishes one of those candidates. crane, cosign, `helm push`, and
# `helm pull` are stubbed (a directory stands in for Harbor); `helm package` is the real
# one. The last section checks this repository itself: no chart or Kubemoot image
# version in git, and no workflow that commits a version.
# Usage: RELEASE_LIB=<release-actions>/release-lib.sh bash .github/scripts/test-publish-release.sh
#        (exit 0 = all passed; in CI the release-actions setup action sets RELEASE_LIB)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(git -C "$here" rev-parse --show-toplevel)"
command -v helm >/dev/null || { echo "helm is required"; exit 1; }
[ -f "${RELEASE_LIB:-}" ] || { echo "RELEASE_LIB must point to release-lib.sh from kubemoot/release-actions"; exit 1; }
real_helm="$(command -v helm)"

failures=0
check() {
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; failures=$((failures + 1)); fi
}

root="$(mktemp -d)"
# Pushes into the scratch repositories start git's automatic gc in the background,
# which races the cleanup below ("Directory not empty"); the test needs no gc.
export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=gc.auto GIT_CONFIG_VALUE_0=0
trap 'rm -rf "$root"' EXIT
export LOG="${root}/calls.log" GHCR_STATE="${root}/ghcr" HARBOR_STATE="${root}/harbor" SIGNED_STATE="${root}/signed"
mkdir -p "${root}/bin" "$GHCR_STATE" "$HARBOR_STATE" "$SIGNED_STATE"
touch "$LOG"

# crane stub: Harbor digests are derived from the reference without its tag; a
# release-registry reference exists once copied (or planted by a test).
cat > "${root}/bin/crane" <<'EOF'
#!/usr/bin/env bash
# The real tool reads the secret it is given on stdin; a stub that exits first makes a
# caller's `echo secret | tool` fail with a broken pipe at random.
case " $* " in *" --password-stdin "*) cat > /dev/null ;; esac
echo "crane $*" >> "$LOG"
[ "$1" = "--insecure" ] && shift
args=("$@"); [ "${args[1]:-}" = "--insecure" ] && args=("${args[0]}" "${args[@]:2}")
case "${args[0]}" in
  auth) exit 0 ;;
  digest)
    ref="${args[1]}"
    case "$ref" in
      ghcr.test/*) f="$GHCR_STATE/$(echo "$ref" | tr '/:' '__')"; [ -f "$f" ] && cat "$f" && exit 0; exit 1 ;;
      *) echo "sha256:$(echo "${ref%:*}" | sha256sum | cut -d' ' -f1)" ;;
    esac ;;
  copy)
    dst="${args[2]}"; src="${args[1]}"
    echo "${src#*@}" > "$GHCR_STATE/$(echo "$dst" | tr '/:' '__')" ;;
  tag) exit 0 ;;
esac
EOF
# cosign stub: a signature is a file in SIGNED_STATE named for the reference; verify
# finds it. COSIGN_FAIL makes signing a reference that contains it fail; COSIGN_BROKEN
# makes cosign not run.
cat > "${root}/bin/cosign" <<'EOF'
#!/usr/bin/env bash
case " $* " in *" --password-stdin "*) cat > /dev/null ;; esac
echo "cosign $*" >> "$LOG"
[ -z "${COSIGN_BROKEN:-}" ] || exit 127
case "$1" in
  version) printf 'GitVersion:    v3.0.2\nGitCommit:     test\n' ;;
  login) exit 0 ;;
  verify) [ -f "$SIGNED_STATE/$(echo "$2" | tr '/:@' '___')" ] ;;
  sign)
    ref="${*: -1}"
    [ -z "${COSIGN_FAIL:-}" ] || [[ "$ref" != *"${COSIGN_FAIL}"* ]] || exit 1
    touch "$SIGNED_STATE/$(echo "$ref" | tr '/:@' '___')" ;;
  *) exit 1 ;;
esac
EOF
# helm stub: push to Harbor stores the chart in HARBOR_STATE (HELM_PUSH_FAIL makes
# every push fail), push to the release registry records the package's digest under its
# reference; pull serves it from Harbor; everything else is the real helm.
cat > "${root}/bin/helm" <<EOF
#!/usr/bin/env bash
case "\$1" in
  registry) case " \$* " in *" --password-stdin "*) cat > /dev/null ;; esac; echo "helm \$*" >> "\$LOG"; exit 0 ;;
  push)
    echo "helm \$*" >> "\$LOG"
    [ -z "\${HELM_PUSH_FAIL:-}" ] || exit 1
    tgz=""; dest=""
    for a in "\$@"; do case "\$a" in *.tgz) tgz="\$a" ;; oci://*) dest="\$a" ;; esac; done
    case "\$dest" in
      oci://harbor.test/*) cp "\$tgz" "\$HARBOR_STATE/" ;;
      oci://ghcr.test/*)
        meta="\$(tar -xzOf "\$tgz" --wildcards '*/Chart.yaml' | sed -n 's/^name: //p; s/^version: //p' | head -n 2 | paste -sd:)"
        echo "sha256:\$(sha256sum "\$tgz" | cut -d' ' -f1)" > "\$GHCR_STATE/\$(echo "\${dest#oci://}/\${meta}" | tr '/:' '__')" ;;
    esac
    exit 0 ;;
  pull)
    version=""; dir=""
    while [ \$# -gt 0 ]; do
      case "\$1" in --version) version="\$2"; shift ;; --untardir) dir="\$2"; shift ;; esac
      shift
    done
    [ -f "\$HARBOR_STATE/kubemoot-operator-\${version}.tgz" ] || exit 1
    tar -xzf "\$HARBOR_STATE/kubemoot-operator-\${version}.tgz" -C "\$dir" ;;
  *) exec "${real_helm}" "\$@" ;;
esac
EOF
chmod +x "${root}/bin/crane" "${root}/bin/cosign" "${root}/bin/helm"
export PATH="${root}/bin:${PATH}"

# Fixture repository: both charts as git holds them.
git init -q --bare "${root}/origin.git"
git clone -q "${root}/origin.git" "${root}/repo" 2>/dev/null
cd "${root}/repo"
git config user.email test@example.com
git config user.name test
git checkout -q -b main
chart=operator/chart/kubemoot-operator
dash=dashboard/charts/kubemoot-dashboard
mkdir -p "$chart/templates" "$dash"
cat > "$chart/Chart.yaml" <<'EOF'
apiVersion: v2
name: kubemoot-operator
version: 0.0.0
appVersion: "0.0.0"
dependencies:
  - name: kubemoot-dashboard
    alias: dashboard
    version: ">=0.0.0-0"
    repository: file://../../../dashboard/charts/kubemoot-dashboard
    condition: dashboard.enabled
EOF
cat > "$chart/values.yaml" <<'EOF'
verify:
  image: test-runner:0.0.0
liaison:
  image: crew-liaison:0.0.0
config:
  images:
    agentRuntime: agent-runtime:0.0.0
    mcpGateway: mcp-gateway:0.0.0
    doclingServe: quay.io/docling-project/docling-serve-cpu:latest
dashboard:
  enabled: false
EOF
printf 'apiVersion: v2\nname: kubemoot-dashboard\nversion: 0.0.0\nappVersion: 0.0.0\n' > "$dash/Chart.yaml"
mkdir -p "$chart/dashboards"
echo '{"title": "Kubemoot Test"}' > "$chart/dashboards/kubemoot-test.json"
printf 'verify:\n  image: ghcr.io/kubemoot/test-runner:0.0.0\n' > "$dash/values.yaml"
git add -A; git commit -q -m "chore: init"
for t in v0.343.40 agent-runtime-v0.342.31 mcp-gateway-v0.326.22-rc.5 mcp-gateway-v0.326.22 \
  test-runner-v0.46.2-rc.1 test-runner-v0.46.2 crew-liaison-v0.346.31 dashboard-v0.50.0 \
  operator-chart-v0.92.581-rc.3 operator-chart-v0.92.581; do
  git tag -a "$t" -m "$t"
done
c1_commit() { git commit -q --allow-empty -m "$1"; git rev-parse HEAD; }
c1=$(c1_commit "feat(runtime): stream tokens")
git tag -a agent-runtime-v0.342.32-rc.3 -m rc
c2=$(c1_commit "fix: code sandbox limit")
for t in code-sandbox-v0.16.32-rc.1 crew-liaison-v0.346.32-rc.0 v0.343.41-rc.2 dashboard-v0.50.1-rc.0 \
  test-runner-v0.46.3-rc.0 scheduling-mcp-v0.1.0-rc.2; do
  git tag -a "$t" -m rc
done
git push -q origin main --tags 2>/dev/null

export REGISTRY=harbor.test RELEASE_REGISTRY=ghcr.test/kubemoot
export HARBOR_USERNAME=u HARBOR_PASSWORD=p GHCR_USERNAME=u GHCR_TOKEN=t
# The variables GitHub Actions sets for a job with id-token: write.
export ACTIONS_ID_TOKEN_REQUEST_URL=https://oidc.test ACTIONS_ID_TOKEN_REQUEST_TOKEN=t GITHUB_WORKFLOW_REF=kubemoot/kubemoot/.github/workflows/publish-release.yaml@refs/heads/main
run_chart() {
  : > "${root}/gh-$1"
  GITHUB_OUTPUT="${root}/gh-$1" OUT_DIR="${root}/chart-$1" bash "${here}/release-operator-chart.sh" > "${root}/chart-$1.log" 2>&1
}
gh_out() { sed -n "s/^$2=//p" "${root}/gh-$1"; }
in_tgz() { tar -xzOf "$1" "$2"; }
# dash_file TGZ FILE: FILE of the dashboard subchart inside an operator chart package,
# which embeds it as a directory or as a package of its own, depending on the helm version.
dash_file() {
  local sub
  local listing
  listing="$(tar -tzf "$1")"
  if grep -qx "kubemoot-operator/charts/kubemoot-dashboard/$2" <<<"$listing"; then
    in_tgz "$1" "kubemoot-operator/charts/kubemoot-dashboard/$2"
  else
    sub="$(grep -E '^kubemoot-operator/charts/kubemoot-dashboard-.*\.tgz$' <<<"$listing")"
    in_tgz "$1" "$sub" | tar -xzO "kubemoot-dashboard/$2"
  fi
}
remote_tag() { git ls-remote --tags origin "refs/tags/$1" | wc -l | tr -d ' '; }

# 1. A dry chart build: the versions come from the tags; nothing is pushed or committed.
DRY_RUN=true run_chart dry && status=0 || status=$?
check "dry chart build succeeds" 0 "$status"
[ "$status" -eq 0 ] || sed 's/^/    /' "${root}/chart-dry.log"
tgz="${root}/chart-dry/kubemoot-operator-0.92.582-rc.0.tgz"
check "the next candidate after a published one starts the next patch" 1 "$([ -f "$tgz" ] && echo 1 || echo 0)"
cv="$(in_tgz "$tgz" kubemoot-operator/values.yaml)"
check "chart appVersion is the latest operator candidate" 1 "$(in_tgz "$tgz" kubemoot-operator/Chart.yaml | grep -c '^appVersion: 0.343.41-rc.2$')"
check "pins the latest runtime candidate" 1 "$(grep -c 'agentRuntime: agent-runtime:0.342.32-rc.3$' <<<"$cv")"
check "pins the latest liaison candidate" 1 "$(grep -c 'image: crew-liaison:0.346.32-rc.0$' <<<"$cv")"
check "pins the latest test-runner candidate" 1 "$(grep -c 'image: test-runner:0.46.3-rc.0$' <<<"$cv")"
check "pins a published candidate as it is" 1 "$(grep -c 'mcpGateway: mcp-gateway:0.326.22-rc.5$' <<<"$cv")"
check "leaves a third-party image alone" 1 "$(grep -c 'docling-serve-cpu:latest$' <<<"$cv")"
check "no 0.0.0 left in the chart" 0 "$(tar -xzOf "$tgz" | grep -c ':0\.0\.0' || true)"
check "embeds the latest dashboard candidate" "0.50.1-rc.0|0.50.1-rc.0" \
  "$(dash_file "$tgz" Chart.yaml | sed -n 's/^version: //p')|$(dash_file "$tgz" Chart.yaml | sed -n 's/^appVersion: //p')"
check "dashboard public reference takes the latest final" 1 \
  "$(dash_file "$tgz" values.yaml | grep -c 'ghcr.io/kubemoot/test-runner:0.46.2$')"
check "dry build pushes no chart" 0 "$(grep -c '^helm push' "$LOG" || true)"
check "dry build pushes no tag" 0 "$(remote_tag operator-chart-v0.92.582-rc.0)"
check "dry build leaves git at 0.0.0" "" "$(git status --porcelain)"
check "dry build reports no release" false "$(gh_out dry released)"

# 2. The real build: the tag claims the version on HEAD, then the chart goes to Harbor.
DRY_RUN=false run_chart rc0 && status=0 || status=$?
check "chart build succeeds" 0 "$status"
check "tags the candidate on HEAD" "$c2" "$(git ls-remote origin refs/tags/operator-chart-v0.92.582-rc.0^{} | cut -f1)"
check "pushes the chart to Harbor" 1 "$([ -f "${HARBOR_STATE}/kubemoot-operator-0.92.582-rc.0.tgz" ] && echo 1 || echo 0)"
check "reports the release" "true|0.92.582-rc.0|0.46.3-rc.0" \
  "$(gh_out rc0 released)|$(gh_out rc0 chart_version)|$(gh_out rc0 test_runner)"
git fetch -q origin --tags
check "the tag records the versions" 1 \
  "$(git tag -l --format='%(contents:body)' operator-chart-v0.92.582-rc.0 | grep -c '^agent-runtime 0.342.32-rc.3$')"
check "the build commits nothing" "$c2" "$(git ls-remote origin refs/heads/main | cut -f1)"

# 3. Nothing changed: no new candidate. A forced build makes one.
DRY_RUN=false run_chart same && status=0 || status=$?
check "an unchanged chart builds nothing" "0|false" "${status}|$(gh_out same released)"
check "and pushes no chart" 1 "$(grep -c '^helm push' "$LOG")"
FORCE=true DRY_RUN=true run_chart force && status=0 || status=$?
check "a forced build makes the next candidate" 1 "$([ -f "${root}/chart-force/kubemoot-operator-0.92.582-rc.1.tgz" ] && echo 1 || echo 0)"

# 4. An image the chart does not pin does not rebuild it; a pinned one, or a chart
# source change, does.
c3=$(c1_commit "fix: later work not yet built")
git tag -a artifact-access-v0.342.33-rc.0 -m rc
git push -q origin main --tags 2>/dev/null
DRY_RUN=false run_chart standalone && status=0 || status=$?
check "a standalone image builds no chart" "0|false" "${status}|$(gh_out standalone released)"
# A runtime release that finished after the candidate was built, tagged on an older commit.
git tag -a agent-runtime-v0.342.33-rc.0 -m rc "$c1"
git push -q origin --tags 2>/dev/null
DRY_RUN=false run_chart rc1 && status=0 || status=$?
check "a new pinned candidate builds the next chart" "0|0.92.582-rc.1" "${status}|$(gh_out rc1 chart_version)"
check "with that candidate" 1 \
  "$(in_tgz "${root}/chart-rc1/kubemoot-operator-0.92.582-rc.1.tgz" kubemoot-operator/values.yaml | grep -c 'agent-runtime:0.342.33-rc.0$')"
echo "# a template" > "$chart/templates/notes.txt"
git add "$chart/templates/notes.txt"; git commit -q -m "feat: chart notes"
DRY_RUN=true run_chart source && status=0 || status=$?
check "a chart source change builds a chart" "0|0.92.582-rc.2" "${status}|$(gh_out source chart_version)"
git reset -q --hard "$c3"

# 5. Unexpected inputs.
printf '    newThing: new-thing:0.0.0\n' >> "$chart/values.yaml"
git commit -q -am "feat: a component with no release yet"
DRY_RUN=false run_chart untagged && status=0 || status=$?
check "an image with no candidate fails" 1 "$status"
check "and names it" 1 "$(grep -c 'no rc new-thing-vX.Y.Z tag' "${root}/chart-untagged.log")"
check "and pushes no tag" 0 "$(remote_tag operator-chart-v0.92.582-rc.2)"
git reset -q --hard "$c3"
git tag -a crew-liaison-v0.346.33-rc.0 -m rc
git push -q origin --tags 2>/dev/null
: > "$LOG"
HELM_PUSH_FAIL=1 DRY_RUN=false run_chart pushfail && status=0 || status=$?
check "a failed chart push fails" 1 "$status"
check "after three attempts" 3 "$(grep -c '^helm push' "$LOG")"
check "and removes the tag it claimed" 0 "$(remote_tag operator-chart-v0.92.582-rc.2)"
check "locally too" "" "$(git tag -l operator-chart-v0.92.582-rc.2)"
git clone -q -b main "${root}/origin.git" "${root}/bare-tags" 2>/dev/null
(
  cd "${root}/bare-tags"
  git tag -l 'dashboard-v*' | xargs git tag -d >/dev/null
  DRY_RUN=true OUT_DIR="${root}/chart-nodash" bash "${here}/release-operator-chart.sh" > "${root}/chart-nodash.log" 2>&1
) && status=0 || status=$?
check "no dashboard candidate fails" 1 "$status"
(
  cd "${root}/bare-tags"
  git tag -l 'operator-chart-v*' | xargs git tag -d >/dev/null
  git -c user.name=test -c user.email=test@example.com tag -a dashboard-v0.50.1-rc.0 -m rc
  DRY_RUN=true OUT_DIR="${root}/chart-nochart" bash "${here}/release-operator-chart.sh" > "${root}/chart-nochart.log" 2>&1
) && status=0 || status=$?
check "no chart candidate to count from fails" "1|1" "${status}|$(grep -c 'no operator-chart-v' "${root}/chart-nochart.log")"
(
  cd "${root}/bare-tags"
  unset REGISTRY
  DRY_RUN=false OUT_DIR="${root}/chart-noreg" bash "${here}/release-operator-chart.sh" > /dev/null 2>&1
) && status=0 || status=$?
check "a real build needs a registry" 1 "$status"

# 6. Publishing the first candidate (operator-chart-v0.92.582-rc.0, built in 2).
run_publish() {
  : > "${root}/gh-publish-$1"
  GITHUB_OUTPUT="${root}/gh-publish-$1" OUT_DIR="${root}/out-$1" bash "${here}/publish-release.sh" > "${root}/run-$1.log" 2>&1
}
cand=operator-chart-v0.92.582-rc.0
# mcp-gateway 0.326.22 was published before: GHCR holds the candidate's digest.
crane digest --insecure harbor.test/kubemoot/mcp-gateway:x > "${GHCR_STATE}/ghcr.test_kubemoot_mcp-gateway_0.326.22"
: > "$LOG"
DRY_RUN=true RC_TAG="$cand" run_publish dry && status=0 || status=$?
check "the publish dry run succeeds" 0 "$status"
[ "$status" -eq 0 ] || sed 's/^/    /' "${root}/run-dry.log"
out="$(cat "${root}/run-dry.log")"
check "plans the operator image" 1 "$(grep -c 'image kubemoot-operator: 0.343.41-rc.2 .* -> 0.343.41' <<<"$out")"
check "plans the runtime the candidate ran, not a later one" 1 "$(grep -c 'image agent-runtime: 0.342.32-rc.3 .* -> 0.342.32' <<<"$out")"
check "plans the liaison pin" 1 "$(grep -c 'image crew-liaison: 0.346.32-rc.0' <<<"$out")"
check "plans the test-runner pin" 1 "$(grep -c 'image test-runner: 0.46.3-rc.0' <<<"$out")"
check "plans the dashboard subchart" 1 "$(grep -c 'image dashboard: 0.50.1-rc.0' <<<"$out")"
check "plans a standalone image" 1 "$(grep -c 'image code-sandbox: 0.16.32-rc.1' <<<"$out")"
check "plans scheduling-mcp with the standalone images" 1 "$(grep -c 'image scheduling-mcp: 0.1.0-rc.2 .* -> 0.1.0' <<<"$out")"
check "skips a standalone candidate newer than the chart candidate" 0 "$(grep -c 'image artifact-access' <<<"$out" || true)"
check "a published pin is not published again" 1 "$(grep -c 'image mcp-gateway: .*already published' <<<"$out")"
check "dry run copies nothing" 0 "$(grep -c '^crane copy' "$LOG" || true)"
check "dry run pushes no chart" 0 "$(grep -c '^helm push' "$LOG" || true)"
check "dry run creates no tag" "" "$(git tag -l 'operator-chart-v0.92.582')"
check "dry run checks cosign and the signing identity" 1 \
  "$(grep -c '^signing: cosign v3.0.2, identity https://github.com/kubemoot/kubemoot/.github/workflows/publish-release.yaml@refs/heads/main, issuer https://token.actions.githubusercontent.com$' <<<"$out")"
check "dry run plans signing every image by digest" 8 "$(grep -cE '^sign ghcr.test/kubemoot/[a-z-]+@sha256:[0-9a-f]{64} \(dry run: not signed\)$' <<<"$out")"
check "dry run plans signing the chart" 1 "$(grep -c '^sign ghcr.test/kubemoot/charts/kubemoot-operator@.*(dry run: not signed)$' <<<"$out")"
check "dry run signs nothing" 0 "$(grep -cE '^cosign (sign|login)' "$LOG" || true)"
check "dry run records no signed subject" "[]" "$(sed -n 's/^subjects=//p' "${root}/gh-publish-dry")"
check "dry run maps no provenance" 0 "$(wc -l < "${root}/out-dry/provenance.tsv" | tr -d ' ')"
tgz="${root}/out-dry/kubemoot-operator-0.92.582.tgz"
check "packages the final operator chart" 1 "$([ -f "$tgz" ] && echo 1 || echo 0)"
values="$(in_tgz "$tgz" kubemoot-operator/values.yaml)"
check "final chart pins the runtime's final" 1 "$(grep -c 'agent-runtime:0.342.32$' <<<"$values")"
check "final chart pins the test-runner's final" 1 "$(grep -c 'image: test-runner:0.46.3$' <<<"$values")"
check "final chart pins the published gateway's final" 1 "$(grep -c 'mcp-gateway:0.326.22$' <<<"$values")"
check "final chart pins no candidate" 0 "$(tar -xzOf "$tgz" | grep -c -- '-rc\.' || true)"
check "final chart pins no 0.0.0" 0 "$(tar -xzOf "$tgz" | grep -c ':0\.0\.0' || true)"
check "final chart appVersion" 1 "$(in_tgz "$tgz" kubemoot-operator/Chart.yaml | grep -cE '^appVersion: 0.343.41$')"
check "final chart embeds the final dashboard" "0.50.1|0.50.1" \
  "$(dash_file "$tgz" Chart.yaml | sed -n 's/^version: //p')|$(dash_file "$tgz" Chart.yaml | sed -n 's/^appVersion: //p')"
check "final dashboard keeps the candidate's public test-runner" 1 "$(dash_file "$tgz" values.yaml | grep -c 'test-runner:0.46.2$')"
check "notes list the feature" 1 "$(grep -c '^- Stream tokens (' "${root}/out-dry/notes.md")"
check "notes stop at the candidate" 0 "$(grep -c 'later work' "${root}/out-dry/notes.md" || true)"

# 7. Unexpected inputs to the release.
DRY_RUN=true RC_TAG=v9.9.9-rc.1 run_publish badtag && status=0 || status=$?
check "refuses an unknown tag" 1 "$status"
DRY_RUN=true RC_TAG=v0.343.40 run_publish final && status=0 || status=$?
check "refuses a final tag as input" 1 "$status"
echo "sha256:someotherdigest" > "${GHCR_STATE}/ghcr.test_kubemoot_agent-runtime_0.342.32"
DRY_RUN=true RC_TAG="$cand" run_publish clash && status=0 || status=$?
check "refuses a published version with another digest" 1 "$status"
check "names the clash" 1 "$(grep -c 'exists with digest' "${root}/run-clash.log")"
rm "${GHCR_STATE}/ghcr.test_kubemoot_agent-runtime_0.342.32"
mv "${HARBOR_STATE}/kubemoot-operator-0.92.582-rc.0.tgz" "${root}/held.tgz"
DRY_RUN=true RC_TAG="$cand" run_publish nochart && status=0 || status=$?
check "refuses a candidate Harbor does not hold" "1|1" "${status}|$(grep -c 'cannot read the candidate' "${root}/run-nochart.log")"
mv "${root}/held.tgz" "${HARBOR_STATE}/kubemoot-operator-0.92.582-rc.0.tgz"

# A dry run in GitHub Actions without id-token: write fails: signing is not wired.
GITHUB_ACTIONS=true ACTIONS_ID_TOKEN_REQUEST_URL='' DRY_RUN=true RC_TAG="$cand" run_publish nooidc && status=0 || status=$?
check "a dry run without an OIDC token fails" "1|1" "${status}|$(grep -c 'needs permissions id-token: write' "${root}/run-nooidc.log")"
: > "$LOG"
ACTIONS_ID_TOKEN_REQUEST_URL='' DRY_RUN=false RC_TAG="$cand" run_publish nooidcreal && status=0 || status=$?
check "a real run without an OIDC token fails before publishing" "1|0" "${status}|$(grep -c '^crane copy' "$LOG" || true)"
COSIGN_BROKEN=1 DRY_RUN=true RC_TAG="$cand" run_publish nocosign && status=0 || status=$?
check "a run where cosign does not run fails" "1|1" "${status}|$(grep -c 'cosign is required' "${root}/run-nocosign.log")"
: > "$LOG"
GHCR_TOKEN='' DRY_RUN=false RC_TAG="$cand" run_publish notoken && status=0 || status=$?
check "a real run without a registry token fails before publishing" "1|0" "${status}|$(grep -c '^crane copy' "$LOG" || true)"

# A signing failure stops the release before any tag, so no GitHub Release is written.
# The registry state is restored afterwards so the real release below starts clean.
cp -a "$GHCR_STATE" "${root}/ghcr-held"
for target in agent-runtime charts/kubemoot-operator; do
  : > "$LOG"
  COSIGN_FAIL="$target" DRY_RUN=false RC_TAG="$cand" run_publish "signfail-${target##*/}" && status=0 || status=$?
  check "a failure signing ${target} fails the release" "1|1" \
    "${status}|$(grep -c "signing ghcr.test/kubemoot/${target}@sha256:.* failed" "${root}/run-signfail-${target##*/}.log")"
  check "and pushes no final tag" 0 "$(git ls-remote --tags origin 'refs/tags/*' | grep -cE 'refs/tags/(operator-chart-v0.92.582|v0.343.41|agent-runtime-v0.342.32)$' || true)"
  check "and names no GitHub Release" 0 "$(wc -l < "${root}/out-signfail-${target##*/}/releases.tsv" | tr -d ' ')"
done
check "a chart signing failure comes after the chart push" 2 "$(grep -c '^helm push' "$LOG")"
check "signs an image an earlier release published when it carries no signature" 1 \
  "$(grep -c '^cosign sign --yes ghcr.test/kubemoot/mcp-gateway@' "$LOG")"
check "a rerun does not sign again what the failed run signed" 0 \
  "$(grep -c '^cosign sign --yes ghcr.test/kubemoot/kubemoot-operator@' "$LOG" || true)"
check "but still records it for the provenance" 1 \
  "$(grep -c '^ghcr.test/kubemoot/kubemoot-operator	' "${root}/out-signfail-kubemoot-operator/subjects.tsv")"
rm -rf "$GHCR_STATE" "$SIGNED_STATE"; mv "${root}/ghcr-held" "$GHCR_STATE"; mkdir -p "$SIGNED_STATE"
# mcp-gateway, published by an earlier release, carries that release's signature.
touch "${SIGNED_STATE}/$(echo "ghcr.test/kubemoot/mcp-gateway@$(crane digest --insecure harbor.test/kubemoot/mcp-gateway:x)" | tr '/:@' '___')"

# 8. The real release.
: > "$LOG"
DRY_RUN=false RC_TAG="$cand" run_publish real && status=0 || status=$?
check "the release succeeds" 0 "$status"
[ "$status" -eq 0 ] || sed 's/^/    /' "${root}/run-real.log"
git fetch -q origin --tags
check "chart final tag on the candidate commit" "$c2" "$(git rev-list -n 1 operator-chart-v0.92.582 2>/dev/null)"
check "component final tag on its candidate commit" "$c1" "$(git rev-list -n 1 agent-runtime-v0.342.32 2>/dev/null)"
check "operator final tag" "$c2" "$(git rev-list -n 1 v0.343.41 2>/dev/null)"
check "standalone scheduling-mcp final tag on its candidate commit" "$c2" "$(git rev-list -n 1 scheduling-mcp-v0.1.0 2>/dev/null)"
check "notes of the real run start at the previous release" 1 "$(grep -c '^## Changes since operator-chart-v0.92.581' "${root}/out-real/notes.md")"
check "names the GitHub Release" "operator-chart-v0.92.582|Kubemoot 0.92.582|notes.md" "$(tr '\t' '|' < "${root}/out-real/releases.tsv")"
check "attaches the chart's dashboards to it" "operator-chart-v0.92.582|dashboards/kubemoot-test.json|Kubemoot Test" \
  "$(tr '\t' '|' < "${root}/out-real/assets.tsv")|$(jq -r .title "${root}/out-real/dashboards/kubemoot-test.json")"
check "leaves no scratch worktree" 1 "$(git worktree list | wc -l | tr -d ' ')"
check "copies seven images" 7 "$(grep -c '^crane copy' "$LOG")"
check "copies by digest" 7 "$(grep '^crane copy' "$LOG" | grep -c '@sha256:')"
check "pushes the chart to both registries" 2 "$(grep -c '^helm push' "$LOG")"
check "logs cosign in to the release registry" 1 "$(grep -c '^cosign login ghcr.test -u u --password-stdin$' "$LOG")"
copied="$(sed -nE 's#^crane copy --insecure harbor.test/kubemoot/([a-z-]+@sha256:[0-9a-f]+) .*#\1#p' "$LOG" | sort)"
signed="$(sed -nE 's#^cosign sign --yes ghcr.test/kubemoot/([a-z-]+@sha256:[0-9a-f]+)$#\1#p' "$LOG" | sort)"
check "signs every image it publishes, by the digest it copied" "$copied" "$signed"
check "signs seven images" 7 "$(wc -l <<<"$signed" | tr -d ' ')"
check "does not sign again an image signed by an earlier release" 0 "$(grep -c '^cosign sign --yes ghcr.test/kubemoot/mcp-gateway@' "$LOG" || true)"
check "nor records it for the provenance" 0 "$(grep -c 'mcp-gateway' "${root}/out-real/subjects.tsv" || true)"
check "signs the chart by the digest the release registry holds" 1 \
  "$(grep -cx "cosign sign --yes ghcr.test/kubemoot/charts/kubemoot-operator@$(cat "${GHCR_STATE}/ghcr.test_kubemoot_charts_kubemoot-operator_0.92.582")" "$LOG")"
check "signs nothing by tag" 0 "$(grep '^cosign sign' "$LOG" | grep -vc '@sha256:' || true)"
check "records each signed artifact for the provenance" 8 "$(wc -l < "${root}/out-real/subjects.tsv" | tr -d ' ')"
check "outputs them as the attest matrix" "8|ghcr.test/kubemoot/charts/kubemoot-operator" \
  "$(sed -n 's/^subjects=//p' "${root}/gh-publish-real" | python3 -c 'import json,sys; s=json.load(sys.stdin); print(len(s), s[-1]["name"], sep="|")')"
check "maps every signed artifact's provenance to the one GitHub Release" \
  "$(awk -F'\t' '{ print "operator-chart-v0.92.582|kubemoot_0.92.582.intoto.jsonl|" $1 "|" $2 }' "${root}/out-real/subjects.tsv")" \
  "$(tr '\t' '|' < "${root}/out-real/provenance.tsv")"

# 8b. The dashboard assets on their own: a broken dashboard stops the run, a chart
# without dashboards attaches none, and a rerun leaves no stale file behind.
dash_assets() {
  # shellcheck disable=SC2016 # expanded by the inner bash
  REGISTRY=harbor.test RELEASE_REGISTRY=ghcr.test OUT_DIR="${root}/assets-$1" \
    bash -c 'source "$1"; RL_CHECKOUT="$2"; write_dashboard_assets operator-chart-v1.0.0' \
    _ "${here}/publish-release.sh" "${root}/checkout-$1" > "${root}/assets-$1.log" 2>&1
}
mkdir -p "${root}/checkout-broken/${chart}/dashboards"
echo '{"title": ' > "${root}/checkout-broken/${chart}/dashboards/kubemoot-broken.json"
dash_assets broken && status=0 || status=$?
check "a broken dashboard fails the run" "1|1" \
  "$((status != 0))|$(grep -c 'dashboard kubemoot-broken.json is not valid JSON' "${root}/assets-broken.log")"
mkdir -p "${root}/checkout-none/${chart}"
dash_assets none && status=0 || status=$?
check "a chart without dashboards attaches none" "0|0" "${status}|$(wc -l < "${root}/assets-none/assets.tsv" | tr -d ' ')"
mkdir -p "${root}/assets-stale/dashboards" "${root}/checkout-stale/${chart}/dashboards"
echo '{}' > "${root}/assets-stale/dashboards/kubemoot-old.json"
echo '{}' > "${root}/checkout-stale/${chart}/dashboards/kubemoot-new.json"
dash_assets stale && status=0 || status=$?
check "a rerun keeps only the chart's dashboards" "0|kubemoot-new.json" \
  "${status}|$(find "${root}/assets-stale/dashboards" -type f -printf '%f\n' | paste -sd' ')"

# 9. A second release of the same candidate is refused; the next chart candidate
# starts the next patch.
DRY_RUN=false RC_TAG="$cand" run_publish again && status=0 || status=$?
check "refuses a candidate already released" 1 "$status"
check "says it is released" 1 "$(grep -c 'already released' "${root}/run-again.log")"
git checkout -q main; git reset -q --hard "$c3"
DRY_RUN=true run_chart after && status=0 || status=$?
check "after a release the next candidate starts the next patch" "0|0.92.583-rc.0" "${status}|$(gh_out after chart_version)"

# 10. A candidate built before the versions moved to the tags: git at its commit holds
# the real versions, and the chart in Harbor was packaged from them.
sed -i 's/^version: 0.0.0/version: 0.92.583-rc.0/; s/^appVersion: "0.0.0"/appVersion: "0.343.41-rc.2"/' "$chart/Chart.yaml"
sed -i 's/^version: 0.0.0/version: 0.50.1-rc.0/; s/^appVersion: 0.0.0/appVersion: 0.50.1-rc.0/' "$dash/Chart.yaml"
sed -i 's/test-runner:0.0.0/test-runner:0.46.3-rc.0/; s/crew-liaison:0.0.0/crew-liaison:0.346.33-rc.0/;
  s/agent-runtime:0.0.0/agent-runtime:0.342.33-rc.0/; s/mcp-gateway:0.0.0/mcp-gateway:0.326.22-rc.5/' "$chart/values.yaml"
sed -i 's/test-runner:0.0.0/test-runner:0.46.2/' "$dash/values.yaml"
git commit -q -am "chore: update pins, operator chart to v0.92.583-rc.0 [skip ci]"
git tag -a operator-chart-v0.92.583-rc.0 -m rc
git push -q origin main --tags 2>/dev/null
"$real_helm" package --dependency-update "$chart" --destination "$HARBOR_STATE" >/dev/null
git checkout -q -- . 2>/dev/null || true
rm -rf "$chart/charts" "$chart/Chart.lock"
DRY_RUN=true RC_TAG=operator-chart-v0.92.583-rc.0 run_publish old && status=0 || status=$?
check "publishes a candidate built before the migration" 0 "$status"
[ "$status" -eq 0 ] || sed 's/^/    /' "${root}/run-old.log"
tgz="${root}/out-old/kubemoot-operator-0.92.583.tgz"
values="$(in_tgz "$tgz" kubemoot-operator/values.yaml 2>/dev/null || true)"
check "with the finals of its pins" "1|1" \
  "$(grep -c 'agent-runtime:0.342.33$' <<<"$values")|$(grep -c 'crew-liaison:0.346.33$' <<<"$values")"
check "and no candidate left" 0 "$(tar -xzOf "$tgz" 2>/dev/null | grep -c -- '-rc\.' || true)"

# 10b. The kubemoot-vllm chart releases beside the operator chart when it has a candidate
# that is not yet published. It references no Kubemoot image, so it pins nothing.
cd "${root}/repo"
git checkout -q main
mkdir -p vllm/charts
cp -r "${repo_root}/vllm/charts/kubemoot-vllm" vllm/charts/
git add vllm
git commit -q -m "feat(vllm): the kubemoot-vllm chart"
vllm_commit=$(git rev-parse HEAD)
git tag -a vllm-chart-v0.1.0-rc.0 -m rc
git commit -q --allow-empty -m "feat: later work, not in the vllm chart"
git push -q origin main --tags 2>/dev/null
DRY_RUN=true RC_TAG=latest run_publish vllm && status=0 || status=$?
check "a dry run with a vllm chart candidate succeeds" 0 "$status"
[ "$status" -eq 0 ] || sed 's/^/    /' "${root}/run-vllm.log"
out="$(cat "${root}/run-vllm.log")"
vtgz="${root}/out-vllm/kubemoot-vllm-0.1.0.tgz"
check "packages the vllm chart at the final version" "1|0.1.0|0.1.0" \
  "$([ -f "$vtgz" ] && echo 1 || echo 0)|$(in_tgz "$vtgz" kubemoot-vllm/Chart.yaml | sed -n 's/^version: //p')|$(in_tgz "$vtgz" kubemoot-vllm/Chart.yaml | sed -n 's/^appVersion: //p' | tr -d '"')"
check "plans signing the vllm chart" 1 "$(grep -c '^sign ghcr.test/kubemoot/charts/kubemoot-vllm@.*(dry run: not signed)$' <<<"$out")"
check "plans the operator chart as before" 1 "$(grep -c '^sign ghcr.test/kubemoot/charts/kubemoot-operator@.*(dry run: not signed)$' <<<"$out")"
check "plans its final tag with the others" 1 "$(grep -c '^Final tags:.*vllm-chart-v0.1.0' <<<"$out")"
check "names its own GitHub Release" 1 "$(grep -c '^vllm-chart-v0.1.0	kubemoot-vllm chart 0.1.0	notes-vllm.md$' "${root}/out-vllm/releases.tsv")"
check "the operator release is still named" 1 "$(grep -c '^operator-chart-v0.92.583	Kubemoot 0.92.583	notes.md$' "${root}/out-vllm/releases.tsv")"
check "its notes give the install command" 1 "$(grep -c 'helm install vllm-gpu oci://ghcr.test/kubemoot/charts/kubemoot-vllm --version 0.1.0' "${root}/out-vllm/notes-vllm.md")"
check "its notes list the chart's changes only" "1|0" \
  "$(grep -c '^- The kubemoot-vllm chart (' "${root}/out-vllm/notes-vllm.md")|$(grep -c 'later work' "${root}/out-vllm/notes-vllm.md" || true)"
check "dry run creates no vllm tag" "" "$(git tag -l 'vllm-chart-v0.1.0')"
check "the vllm chart is stamped from its candidate's commit" "$vllm_commit" "$(git rev-list -n 1 vllm-chart-v0.1.0-rc.0)"
check "git keeps the vllm chart at 0.0.0" "version: 0.0.0" "$(grep '^version:' "${vllm_chart_dir:-vllm/charts/kubemoot-vllm}/Chart.yaml")"

# A candidate whose final is already published is not published again.
git tag -a vllm-chart-v0.1.0 -m final "$vllm_commit"
DRY_RUN=true RC_TAG=latest run_publish vllmdone && status=0 || status=$?
check "a published vllm chart is skipped" "0|0" \
  "${status}|$(find "${root}/out-vllmdone" -name 'kubemoot-vllm-*' | wc -l | tr -d ' ')"
git tag -d vllm-chart-v0.1.0 >/dev/null

# Provenance: the vllm chart's goes to its own release, everything else to the operator's.
printf 'ghcr.test/kubemoot/agent-runtime\tsha256:aaa\nghcr.test/kubemoot/charts/kubemoot-operator\tsha256:bbb\nghcr.test/kubemoot/charts/kubemoot-vllm\tsha256:ccc\n' \
  > "${root}/subjects.tsv"
mkdir -p "${root}/prov"
cp "${root}/subjects.tsv" "${root}/prov/subjects.tsv"
# shellcheck disable=SC2016 # expanded by the inner bash
REGISTRY=harbor.test RELEASE_REGISTRY=ghcr.test OUT_DIR="${root}/prov" \
  bash -c 'source "$1"; write_provenance_map operator-chart-v1.0.0 kubemoot_1.0.0.intoto.jsonl vllm-chart-v0.1.0 kubemoot-vllm_0.1.0.intoto.jsonl' _ "${here}/publish-release.sh" > /dev/null 2>&1
check "splits the provenance between the two releases" \
  "operator-chart-v1.0.0|kubemoot_1.0.0.intoto.jsonl|ghcr.test/kubemoot/agent-runtime|sha256:aaa;operator-chart-v1.0.0|kubemoot_1.0.0.intoto.jsonl|ghcr.test/kubemoot/charts/kubemoot-operator|sha256:bbb;vllm-chart-v0.1.0|kubemoot-vllm_0.1.0.intoto.jsonl|ghcr.test/kubemoot/charts/kubemoot-vllm|sha256:ccc" \
  "$(tr '\t' '|' < "${root}/prov/provenance.tsv" | paste -sd';')"
# shellcheck disable=SC2016 # expanded by the inner bash
REGISTRY=harbor.test RELEASE_REGISTRY=ghcr.test OUT_DIR="${root}/prov" \
  bash -c 'source "$1"; write_provenance_map operator-chart-v1.0.0 kubemoot_1.0.0.intoto.jsonl' _ "${here}/publish-release.sh" > /dev/null 2>&1
check "without a vllm release everything goes to the operator's" 3 \
  "$(grep -c '^operator-chart-v1.0.0	kubemoot_1.0.0.intoto.jsonl	' "${root}/prov/provenance.tsv")"

# 11. This repository: versions live only in tags.
cd "$repo_root"
for f in $(git ls-files '*Chart.yaml'); do
  check "${f} holds version 0.0.0" "0.0.0|0.0.0" \
    "$(sed -nE 's/^version: *"?([^"]*)"?$/\1/p' "$f")|$(sed -nE 's/^appVersion: *"?([^"]*)"?$/\1/p' "$f")"
done
images="$(sed -nE 's#^[[:space:]]*IMAGE_NAME: kubemoot/([a-z0-9-]+)$#\1#p' .github/workflows/*.yaml | sort -u | paste -sd'|')"
check "knows the Kubemoot images" 1 "$([ -n "$images" ] && grep -q 'agent-runtime' <<<"$images" && echo 1 || echo 0)"
for f in $(git ls-files '*values.yaml' | grep -E '^(operator/chart|dashboard/charts)/'); do
  typed="$(grep -nE "(^|[\"' /])(${images}):[A-Za-z0-9._-]+" "$f" | grep -vE ":(${images}):0\.0\.0([\"' ]|$)|/(${images}):0\.0\.0([\"' ]|$)| (${images}):0\.0\.0([\"' ]|$)" || true)"
  check "${f} types no Kubemoot image version" "" "$typed"
done
# A script that sets pipefail must not pipe a secret into a login: a login that exits
# before reading it would fail the script with a broken pipe (a here-string cannot).
piped="$(grep -lE '^set -[a-z]*o pipefail|^set -euo pipefail' .github/scripts/*.sh \
  | grep -v 'test-publish-release\.sh$' | xargs -r grep -nE '\|[^|].*--password-stdin' | grep -vE '^[^:]+:[0-9]+:[[:space:]]*#' || true)"
check "no pipefail script pipes a secret into a login" "" "$piped"
# No workflow or script commits: generated code is committed by the developer and checked
# by ci.yaml, and a docs change reaches kubemoot-docs as a dispatch event.
commits="$(grep -nE 'git commit|git push.*(origin main|HEAD:main)|\[skip ci\]' .github/workflows/*.yaml .github/scripts/*.sh \
  | grep -vE '^[^:]+:[0-9]+:[[:space:]]*(#|echo )' | cut -d: -f1 | sort -u \
  | grep -vxE '\.github/scripts/test-publish-release\.sh' || true)"
check "no workflow or script commits to main" "" "$commits"
# kubemoot-docs' Release Docs Site listens for exactly this event type; a dispatch with
# no listener succeeds and builds nothing.
check "a docs change dispatches kubemoot-docs-changed" "1" \
  "$(grep -cE '^[[:space:]]+event-type: kubemoot-docs-changed$' .github/workflows/trigger-docs-rebuild.yaml)"
# The crews' release workflows listen for exactly this event and build new candidates
# with the finals just published.
check "a published release dispatches kubemoot-published to the crews" "1|1" \
  "$(grep -cE '^[[:space:]]+repository: kubemoot/crews$' .github/workflows/publish-release.yaml)|$(grep -cE '^[[:space:]]+event-type: kubemoot-published$' .github/workflows/publish-release.yaml)"
check "no workflow writes a chart file" "" \
  "$(grep -nE '(sed|yq).*(Chart|values)\.yaml' .github/workflows/*.yaml | grep -v 'integration-test' || true)"

if [ "$failures" -ne 0 ]; then echo "${failures} test(s) failed"; exit 1; fi
echo "all release script tests passed"
