#!/usr/bin/env bash
# Tests for wait-for-operator-chart.sh: the HelmRelease state for a chart version, and
# the wait against a fake kubectl that replays a sequence of HelmRelease states.
# Usage: bash .github/scripts/test-wait-for-operator-chart.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
unset GITHUB_OUTPUT
# shellcheck source-path=SCRIPTDIR source=wait-for-operator-chart.sh
source "${here}/wait-for-operator-chart.sh"

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

v=0.92.588-rc.11
# READY|REASON|HISTORY_VERSION|HISTORY_STATUS|ATTEMPTED|STALLED
check "applied" applied "$(chart_state "$v" "True|UpgradeSucceeded|${v}|deployed|${v}|")"
check "applied with an OCI digest suffix" applied "$(chart_state "$v" "True|UpgradeSucceeded|${v}+5e3f0b1a1d6c|deployed|${v}+5e3f0b1a1d6c|")"
check "the previous release Ready is pending" pending "$(chart_state "$v" "True|UpgradeSucceeded|0.92.588-rc.10|deployed|0.92.588-rc.10|")"
check "upgrading is pending" pending "$(chart_state "$v" "Unknown|Progressing|0.92.588-rc.10|deployed|${v}|")"
check "deployed but not yet Ready is pending" pending "$(chart_state "$v" "Unknown|Progressing|${v}|deployed|${v}|")"
check "a failed history entry" failed "$(chart_state "$v" "False|UpgradeFailed|${v}|failed|${v}|")"
check "a failed attempt after rollback" failed "$(chart_state "$v" "False|UpgradeFailed|0.92.588-rc.10|deployed|${v}|")"
check "a failed attempt of an older version is pending" pending "$(chart_state "$v" "False|UpgradeFailed|0.92.588-rc.9|deployed|0.92.588-rc.10|")"
check "stalled" failed "$(chart_state "$v" "False|ArtifactFailed|0.92.588-rc.10|deployed|0.92.588-rc.10|True")"
check "a later candidate supersedes" superseded "$(chart_state "$v" "True|UpgradeSucceeded|0.92.588-rc.12|deployed|0.92.588-rc.12|")"
check "the final supersedes its candidate" superseded "$(chart_state "$v" "True|UpgradeSucceeded|0.92.588|deployed|0.92.588|")"
check "rc.9 does not supersede rc.11" pending "$(chart_state "$v" "True|UpgradeSucceeded|0.92.588-rc.9|deployed|0.92.588-rc.9|")"
check "a later patch supersedes" superseded "$(chart_state "$v" "True|UpgradeSucceeded|0.92.589-rc.0|deployed|0.92.589-rc.0|")"
check "an unreadable HelmRelease is pending" pending "$(chart_state "$v" "")"
check "a final applied" applied "$(chart_state 0.92.588 "True|UpgradeSucceeded|0.92.588|deployed|0.92.588|")"

check_status() {
  local name="$1" want="$2"; shift 2
  local got=0
  "$@" >/dev/null 2>&1 || got=$?
  [ "$got" -ne 0 ] && got=1
  check "$name" "$want" "$got"
}
check_status "1.2.10 above 1.2.9" 0 version_gt 1.2.10 1.2.9
check_status "rc.10 above rc.9" 0 version_gt 1.2.3-rc.10 1.2.3-rc.9
check_status "final above its rc" 0 version_gt 1.2.3 1.2.3-rc.9
check_status "rc below its final" 1 version_gt 1.2.3-rc.9 1.2.3
check_status "equal is not above" 1 version_gt 1.2.3-rc.1 1.2.3-rc.1

# The wait, against a fake kubectl that answers with the next line of a state file.
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cat > "${work}/kubectl" <<'EOF'
#!/usr/bin/env bash
n=$(( $(cat "${STATES}.n" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "${STATES}.n"
line="$(sed -n "${n}p" "$STATES")"
[ -n "$line" ] || line="$(tail -n 1 "$STATES")"
printf '%s' "$line"
EOF
chmod +x "${work}/kubectl"
wait_run() {
  local states="$1"; shift
  rm -f "${states}.n"
  STATES="$states" KUBECTL="${work}/kubectl" POLL=0 TIMEOUT="${TIMEOUT:-5}" \
    bash "${here}/wait-for-operator-chart.sh" "$@" 2>"${work}/err"
}
printf '%s\n' "True|UpgradeSucceeded|0.92.588-rc.10|deployed|0.92.588-rc.10|" \
  "Unknown|Progressing|0.92.588-rc.10|deployed|${v}|" \
  "True|UpgradeSucceeded|${v}|deployed|${v}|" > "${work}/rollout"
check "waits through the old release and the upgrade" "state=applied" "$(wait_run "${work}/rollout" "$v")"
check "read the HelmRelease three times" 3 "$(cat "${work}/rollout.n")"
printf '%s\n' "False|UpgradeFailed|0.92.588-rc.10|deployed|${v}|" > "${work}/failed"
check "a failed upgrade fails at once" "1|1" "$(wait_run "${work}/failed" "$v" >/dev/null && echo 0 || echo 1)|$(cat "${work}/failed.n")"
printf '%s\n' "True|UpgradeSucceeded|0.92.588-rc.12|deployed|0.92.588-rc.12|" > "${work}/later"
check "a later chart ends the wait" "state=superseded" "$(wait_run "${work}/later" "$v")"
printf '%s\n' "True|UpgradeSucceeded|0.92.588-rc.10|deployed|0.92.588-rc.10|" > "${work}/never"
check "the safety net ends a wait that never applies" 1 "$(TIMEOUT=0 wait_run "${work}/never" "$v" >/dev/null && echo 0 || echo 1)"
check "and says it was the safety net" 1 "$(grep -c 'safety net' "${work}/err")"
printf '%s\n' "True|UpgradeSucceeded|${v}|deployed|${v}|" > "${work}/applied"
check "writes the state to GITHUB_OUTPUT in Actions" "|state=applied" \
  "$(GITHUB_OUTPUT="${work}/out" wait_run "${work}/applied" "$v")|$(cat "${work}/out")"
check "refuses a version that is not one" 1 "$(wait_run "${work}/rollout" latest >/dev/null && echo 0 || echo 1)"

if [ "$failures" -ne 0 ]; then echo "${failures} test(s) failed"; exit 1; fi
echo "all wait-for-operator-chart tests passed"
