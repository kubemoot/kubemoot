#!/usr/bin/env bash
# Tests for Kubemoot's optional observability: the Grafana dashboards stay agnostic
# of any one cluster and query only metrics Kubemoot defines, and the chart renders
# the dashboard ConfigMaps, the optional GrafanaDashboard resources, and the
# optional PodMonitors as configured. Needs helm, yq, and python3.
# Run from anywhere: operator/chart/test-observability.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
repo="$(cd "$here/../.." && pwd)"
helm dependency build "$chart" >/dev/null
fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template t "$chart" "$@"; }

# ---- the dashboards ----
python3 - "$chart/dashboards" "$repo" <<'PY' || exit 1
import json, os, re, sys, pathlib
dash_dir, repo = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
problems = []

# Every metric name Kubemoot's main code defines (tests excluded), and the names
# defined in a file that publishes histogram buckets.
SKIP = {"node_modules", "build", ".git", "vendor", ".venv", "target", "test", "tests", "testdata"}
HISTOGRAM = re.compile(r"serviceLevelObjectives|publishPercentileHistogram|NewHistogram")
defined, with_buckets = set(), set()
for ext in ("*.java", "*.go", "*.py", "*.ts"):
    for f in repo.rglob(ext):
        if SKIP & set(f.parts) or re.search(r"(_test\.go|Test\.java|\.test\.ts|\.spec\.ts|test_.*\.py)$", f.name):
            continue
        text = f.read_text(errors="ignore")
        names = set(re.findall(r"\bkubemoot_[a-z0-9_]+", text))
        defined |= names
        if HISTOGRAM.search(text):
            with_buckets |= names

def is_defined(metric):
    """A queried series exists: its own name is defined, or the meter it is a series of."""
    if metric in defined:
        return True
    if metric.endswith("_bucket"):
        return metric[: -len("_bucket")] in with_buckets
    for suffix in ("_total", "_count", "_sum", "_max"):
        if metric.endswith(suffix) and metric[: -len(suffix)] in defined:
            return True
    return False

def walk(o, found):
    if isinstance(o, dict):
        for k, v in o.items():
            if k == "datasource":
                found["datasources"].append(v)
            elif k == "expr" and isinstance(v, str):
                found["exprs"].append(v)
            walk(v, found)
    elif isinstance(o, list):
        for v in o:
            walk(v, found)

for path in sorted(dash_dir.glob("*.json")):
    d = json.loads(path.read_text())
    found = {"datasources": [], "exprs": []}
    walk(d.get("panels", []), found)
    names = {v.get("name") for v in d.get("templating", {}).get("list", [])}
    if "datasource" not in names:
        problems.append(f"{path.name}: no datasource variable")
    for ds in found["datasources"]:
        uid = ds.get("uid") if isinstance(ds, dict) else ds
        if uid not in ("${datasource}", "$datasource"):
            problems.append(f"{path.name}: a panel uses datasource {ds!r}, not the datasource variable")
    for e in found["exprs"]:
        for label, value in re.findall(r'\b(namespace|server|model|pod|node|instance)\s*=\s*"([^"]*)"', e):
            problems.append(f"{path.name}: {label}=\"{value}\" is written literally: {e}")
        if re.search(r"ollama-|rig[0-9]|dijure|homelab", e):
            problems.append(f"{path.name}: names a cluster: {e}")
        for m in set(re.findall(r"\bkubemoot_[a-z0-9_]+", e)):
            if not is_defined(m):
                problems.append(f"{path.name}: queries {m}, which no Kubemoot code defines")
if problems:
    print("\n".join("FAIL: " + p for p in problems), file=sys.stderr)
    sys.exit(1)
PY

# ---- the dashboard ConfigMaps ----
out="$(render)"
for d in overview discussions scheduler; do
  [ "$(yq -N "select(.kind == \"ConfigMap\" and .metadata.name == \"t-kubemoot-operator-dashboard-$d\") | .metadata.labels.grafana_dashboard" <<<"$out")" = "1" ] \
    || fail "dashboard $d is not a sidecar ConfigMap"
done
custom="$(render --set grafana.dashboards.label=dash --set grafana.dashboards.labelValue=yes \
  --set grafana.dashboards.folder=Ops --set grafana.dashboards.namespace=monitoring)"
cm='select(.kind == "ConfigMap" and .metadata.name == "t-kubemoot-operator-dashboard-scheduler")'
[ "$(yq -N "$cm | .metadata.labels.dash" <<<"$custom")" = "yes" ] || fail "the sidecar label is not configurable"
[ "$(yq -N "$cm | .metadata.annotations.grafana_folder" <<<"$custom")" = "Ops" ] || fail "the folder is not configurable"
[ "$(yq -N "$cm | .metadata.namespace" <<<"$custom")" = "monitoring" ] || fail "the namespace is not configurable"
[ -z "$(render --set grafana.dashboards.enabled=false | yq -N 'select(.kind == "ConfigMap" and (.metadata.name | test("dashboard-")))')" ] \
  || fail "dashboards render when disabled"

# GrafanaDashboard: only when asked for and the Grafana Operator's API is served.
go=(--set grafana.dashboards.grafanaOperator.enabled=true --set grafana.dashboards.grafanaOperator.instanceSelector.matchLabels.dashboards=grafana)
[ "$(render "${go[@]}" | yq -N 'select(.kind == "GrafanaDashboard")' | grep -c 'kind' || true)" = 0 ] \
  || fail "GrafanaDashboard renders without the Grafana Operator's API"
gd="$(render "${go[@]}" --api-versions grafana.integreatly.org/v1beta1/GrafanaDashboard)"
[ "$(yq -N 'select(.kind == "GrafanaDashboard") | .metadata.name' <<<"$gd" | wc -l)" = 3 ] || fail "expected a GrafanaDashboard per dashboard"
[ "$(yq -N 'select(.kind == "GrafanaDashboard" and .metadata.name == "t-kubemoot-operator-dashboard-scheduler") | .spec.configMapRef.key' <<<"$gd")" = "kubemoot-scheduler.json" ] \
  || fail "the GrafanaDashboard does not point at its ConfigMap"
[ -z "$(render --api-versions grafana.integreatly.org/v1beta1/GrafanaDashboard | yq -N 'select(.kind == "GrafanaDashboard")')" ] \
  || fail "GrafanaDashboard renders when not asked for"

render --set grafana.dashboards.grafanaOperator.enabled=true >/dev/null 2>&1 \
  && fail "an empty Grafana Operator instanceSelector must be refused"

# ---- the PodMonitors ----
[ -z "$(render | yq -N 'select(.kind == "PodMonitor")')" ] || fail "PodMonitors render without the PodMonitor API"
pm="$(render --api-versions monitoring.coreos.com/v1/PodMonitor --set metrics.podMonitor.labels.release=kube-prometheus-stack)"
[ "$(yq -N 'select(.kind == "PodMonitor") | .metadata.labels.release' <<<"$pm" | sort -u)" = "kube-prometheus-stack" ] \
  || fail "the monitor labels are not applied"
[ "$(yq -N 'select(.kind == "PodMonitor" and .metadata.name == "t-kubemoot-operator-agents") | .spec.podMetricsEndpoints[0].path' <<<"$pm")" = "/q/metrics" ] \
  || fail "the agents are not scraped at /q/metrics"
[ "$(yq -N 'select(.kind == "PodMonitor" and .metadata.name == "t-kubemoot-operator-agents") | .spec.namespaceSelector.any' <<<"$pm")" = "true" ] \
  || fail "agents in every crew namespace must be scraped"
[ "$(yq -N 'select(.kind == "PodMonitor" and .metadata.name == "t-kubemoot-operator-query-services") | .spec.selector.matchLabels["app.kubernetes.io/component"]' <<<"$pm")" = "query-service" ] \
  || fail "the RAG query services are not scraped"
op='select(.kind == "PodMonitor" and .metadata.name == "t-kubemoot-operator-operator")'
[ "$(yq -N "$op | .spec.podMetricsEndpoints[0].scheme" <<<"$pm")" = "https" ] || fail "the operator serves metrics over HTTPS"
[ "$(yq -N "$op | .spec.podMetricsEndpoints[0].authorization.credentials.name" <<<"$pm")" = "t-kubemoot-operator-metrics-reader" ] \
  || fail "the operator's metrics need the reader's token"
[ "$(yq -N 'select(.kind == "ClusterRole" and .metadata.name == "t-kubemoot-operator-metrics-reader") | .rules[0].nonResourceURLs[0]' <<<"$pm")" = "/metrics" ] \
  || fail "the metrics reader may read only /metrics"
[ -z "$(render --api-versions monitoring.coreos.com/v1/PodMonitor --set metrics.podMonitor.enabled=false | yq -N 'select(.kind == "PodMonitor")')" ] \
  || fail "PodMonitors render when disabled"

echo "PASS: observability tests"
