#!/usr/bin/env bash
# Tests for dispatch-clients.sh against a fake gh that records its calls.
# Usage: bash .github/scripts/test-dispatch-clients.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"

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
mkdir -p "${work}/bin"
calls="${work}/calls"

# Fake gh: records each call; a repository named in FAIL_REPO fails.
cat > "${work}/bin/gh" <<EOF2
#!/usr/bin/env bash
echo "\$*" >> "${calls}"
case "\$*" in *"repos/kubemoot/\${FAIL_REPO:-none}/dispatches"*) exit 1 ;; esac
EOF2
chmod +x "${work}/bin/gh"

run() { : > "$calls"; PATH="${work}/bin:${PATH}" bash "${here}/dispatch-clients.sh" "$@" 2>&1; }
code() { : > "$calls"; if PATH="${work}/bin:${PATH}" bash "${here}/dispatch-clients.sh" >/dev/null 2>&1; then echo 0; else echo $?; fi; }

out="$(DRY_RUN=true CHART_VERSION=0.94.0 run)"
check "a dry run calls gh never" "0" "$(wc -l < "$calls" | tr -d ' ')"
check "a dry run names the three repositories" "3" "$(grep -c '^Dry run: would dispatch kubemoot-published to kubemoot/' <<<"$out")"
check "a dry run names CrewForge" "1" "$(grep -c 'kubemoot/vscode-crewforge (chart_version 0.94.0)' <<<"$out")"
check "the default is a dry run" "0" "$(unset DRY_RUN; CHART_VERSION=0.94.0 run >/dev/null; wc -l < "$calls" | tr -d ' ')"
check "a dry run needs no chart version" "0" "$(DRY_RUN=true code)"

out="$(DRY_RUN=false CHART_VERSION=0.94.0 run)"
check "a real run dispatches crews, kmctl and CrewForge" "3" "$(grep -c '^api -X POST repos/kubemoot/\(crews\|kmctl\|vscode-crewforge\)/dispatches' "$calls")"
check "the event type is kubemoot-published" "3" "$(grep -c 'event_type=kubemoot-published' "$calls")"
check "the payload carries the chart version" "3" "$(grep -c 'client_payload\[chart_version\]=0.94.0' "$calls")"
check "a real run reports each dispatch" "3" "$(grep -c '^Dispatched kubemoot-published' <<<"$out")"
check "a real run exits 0" "0" "$(DRY_RUN=false CHART_VERSION=0.94.0 code)"

check "named repositories replace the default" "1" "$(DRY_RUN=false CHART_VERSION=1 run kmctl >/dev/null; wc -l < "$calls" | tr -d ' ')"

out="$(DRY_RUN=false CHART_VERSION=0.94.0 FAIL_REPO=kmctl run || true)"
check "a failed dispatch is named" "1" "$(grep -c '^FAILED to dispatch kubemoot-published to kubemoot/kmctl' <<<"$out")"
check "a failed dispatch does not stop the others" "2" "$(grep -c '^Dispatched' <<<"$out")"
check "a failed dispatch exits 1" "1" "$(DRY_RUN=false CHART_VERSION=0.94.0 FAIL_REPO=kmctl code)"

check "a real run without a chart version is refused" "64" "$(DRY_RUN=false code)"
check "a real run without a chart version dispatches nothing" "0" "$(wc -l < "$calls" | tr -d ' ')"
check "an invalid DRY_RUN is refused" "64" "$(DRY_RUN=maybe CHART_VERSION=1 code)"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
