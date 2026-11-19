#!/usr/bin/env bash
# The crew `kmctl create` scaffolds, installed on the quickstart's cluster and asked one
# question about its own namespace; the answer is checked against the cluster.
#
# Runs after quickstart.sh, on the same kind cluster (Kubemoot, NATS, the CPU Ollama and its
# catalog model are already up). It uses the latest PUBLISHED kmctl: kmctl releases after
# Kubemoot, so no kmctl candidate exists when Kubemoot's Publish Release runs. What it
# proves is that the scaffold users get today still makes a crew that answers on the
# Kubemoot about to be published.
#
# 1. Install the latest kmctl release, checksum and build provenance verified.
# 2. kmctl create <crew> --chart --members N --no-input, helm install it.
# 3. Wait by state: the Crew and every Agent report ready, every Deployment is rolled out.
# 4. kmctl conversation ask ... -o json: "List the pods running in this namespace."
# 5. Check the answer against kubectl, deterministically (no model judges it):
#    - it names every agent of the crew (each is a Deployment, so a pod) and
#    - every crew-prefixed name it writes is a prefix of a real pod, Deployment or Service.
#
# Requires: kubectl on the target cluster, helm, python3, curl, tar, sha256sum, gh (for the
# provenance check; GH_TOKEN or GITHUB_TOKEN set).
#
# Environment (all optional):
#   SCAFFOLD_CREW     crew name (default starter-guide)
#   SCAFFOLD_MEMBERS  specialists beside the coordinator, 1 to 5 (default 1)
#   KMCTL_VERSION     kmctl release to use, without the v (default: the latest)
#   KMCTL_BIN         use this kmctl instead of downloading one
#   READY_TIMEOUT     safety net in seconds for each readiness wait (default 900)
#   ASK_TIMEOUT       safety net for the discussion, a Go duration (default 15m)
#
# Every wait polls a status field; the timeouts only stop a run that will never finish.
# Tested by test-quickstart-scaffold-crew.sh.
set -euo pipefail

SCAFFOLD_CREW="${SCAFFOLD_CREW:-starter-guide}"
SCAFFOLD_MEMBERS="${SCAFFOLD_MEMBERS:-1}"
KMCTL_VERSION="${KMCTL_VERSION:-}"
KMCTL_BIN="${KMCTL_BIN:-}"
READY_TIMEOUT="${READY_TIMEOUT:-900}"
ASK_TIMEOUT="${ASK_TIMEOUT:-15m}"
QUESTION="List the pods running in this namespace and the status of each."

step() { printf '\n== %s ==\n' "$*"; }
fail() { echo "scaffold crew: $*" >&2; exit 1; }

# wait_for LABEL SECONDS COMMAND...: poll COMMAND until it prints "true".
wait_for() {
  local label="$1" budget="$2"; shift 2
  local deadline=$(( $(date +%s) + budget ))
  until [ "$("$@" 2>/dev/null)" = "true" ]; do
    [ "$(date +%s)" -le "$deadline" ] || fail "$label not ready after ${budget}s"
    sleep 5
  done
  echo "$label ready"
}

# install_kmctl DIR: the latest kmctl release into DIR/kmctl, its checksum and build
# provenance checked before it runs. Prints nothing on stdout.
install_kmctl() {
  local dir="$1" version="$KMCTL_VERSION" os arch base archive
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
  if [ -z "$version" ]; then
    version="$(curl -fsSLo /dev/null -w '%{url_effective}' https://github.com/kubemoot/kmctl/releases/latest | sed 's|.*/v||')"
  fi
  [ -n "$version" ] || fail "cannot find the latest kmctl release"
  base="https://github.com/kubemoot/kmctl/releases/download/v${version}"
  archive="kmctl_${version}_${os}_${arch}.tar.gz"
  (
    cd "$dir"
    curl -fsSLO "${base}/${archive}"
    curl -fsSLO "${base}/checksums.txt"
    grep " ${archive}\$" checksums.txt | sha256sum -c - >&2
    gh attestation verify "$archive" --repo kubemoot/kmctl >&2
    tar xzf "$archive" kmctl
  )
  echo "kmctl ${version}" >&2
}

# answer_of: the answer of a kmctl -o json stream on stdin (the "result" line).
answer_of() {
  python3 -c '
import json, sys
answer = None
for line in sys.stdin:
    line = line.strip()
    if not line.startswith("{"):
        continue
    event = json.loads(line)
    if event.get("type") == "result":
        answer = event.get("answer", "")
if answer is None:
    sys.exit("no result line in the kmctl output")
print(answer)
'
}

# check_answer CREW ANSWERFILE NAMESFILE: NAMESFILE lists what the cluster holds, one name per
# line; the first line group (before a blank line) is the agents the answer must name. Fails
# with every problem found.
check_answer() {
  python3 - "$1" "$2" "$3" <<'PY'
import re, sys
crew, answer_file, names_file = sys.argv[1:4]
answer = open(answer_file).read()
agents, existing, current = [], [], None
for line in open(names_file).read().splitlines():
    if line == "":
        current = existing
        continue
    (agents if current is None else existing).append(line)
problems = []
for agent in agents:
    if agent not in answer:
        problems.append("the answer does not name the crew's agent " + agent)
for token in sorted(set(re.findall(re.escape(crew) + r"-[a-z0-9-]*[a-z0-9]", answer))):
    if not any(name.startswith(token) for name in existing):
        problems.append("the answer names " + token + ", which matches no pod, Deployment or Service in the namespace")
if problems:
    print("\n".join(problems), file=sys.stderr)
    sys.exit(1)
print("the answer names every agent and invents no name")
PY
}

# cluster_names CREW NS FILE: the agents of the crew, a blank line, then every pod, Deployment
# and Service name in the namespace. The agents are the Deployments the operator makes for
# the crew's Agents, named for them.
cluster_names() {
  local crew="$1" ns="$2" file="$3"
  {
    kubectl get agents -n "$ns" -l "kubemoot.ai/crew=${crew}" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'
    echo
    kubectl get pods,deployments,services -n "$ns" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'
  } > "$file"
}

# agents_ready: "true" when the crew has agents and every one reports ready.
agents_ready() {
  kubectl get agents -n "$1" -l "kubemoot.ai/crew=$2" -o jsonpath='{range .items[*]}{.status.ready}{"\n"}{end}' \
    | awk 'NF { n++; if ($1 != "true") bad = 1 } END { print (n > 0 && !bad) ? "true" : "false" }'
}

main() {
  local work ns="crew-${SCAFFOLD_CREW}" kmctl answer
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT

  step "1/5 kmctl"
  if [ -n "$KMCTL_BIN" ]; then
    kmctl="$KMCTL_BIN"
  else
    mkdir -p "$work/bin"
    install_kmctl "$work/bin"
    kmctl="$work/bin/kmctl"
  fi
  "$kmctl" version

  step "2/5 Scaffold and install ${SCAFFOLD_CREW} (${SCAFFOLD_MEMBERS} specialist(s), namespace ${ns})"
  "$kmctl" create "$SCAFFOLD_CREW" --chart --members "$SCAFFOLD_MEMBERS" --no-input --output "$work/crew"
  helm upgrade --install "$SCAFFOLD_CREW" "$work/crew/$SCAFFOLD_CREW" --namespace "$ns" --create-namespace

  step "3/5 Wait for the crew"
  wait_for "Crew ${SCAFFOLD_CREW}" "$READY_TIMEOUT" \
    kubectl get crew "$SCAFFOLD_CREW" -n "$ns" -o jsonpath='{.status.ready}'
  wait_for "Agents of ${SCAFFOLD_CREW}" "$READY_TIMEOUT" agents_ready "$ns" "$SCAFFOLD_CREW"
  kubectl rollout status deployment -n "$ns" -l "kubemoot.ai/crew=${SCAFFOLD_CREW}" --timeout="${READY_TIMEOUT}s"

  step "4/5 Ask: ${QUESTION}"
  "$kmctl" conversation ask "$SCAFFOLD_CREW" "$QUESTION" -n "$ns" -o json --timeout "$ASK_TIMEOUT" > "$work/ask.jsonl" \
    || { cat "$work/ask.jsonl" >&2; fail "kmctl conversation ask failed"; }
  answer="$(answer_of < "$work/ask.jsonl")"
  printf '\n--- scaffold answer ---\n%s\n-----------------------\n' "$answer"
  [ -n "$answer" ] || fail "the crew gave no answer"

  step "5/5 Check the answer against the cluster"
  kubectl get pods -n "$ns"
  printf '%s\n' "$answer" > "$work/answer.txt"
  cluster_names "$SCAFFOLD_CREW" "$ns" "$work/names.txt"
  check_answer "$SCAFFOLD_CREW" "$work/answer.txt" "$work/names.txt" || fail "the answer does not match the cluster"
  echo "scaffold crew: PASS"
}

# Sourced (by the tests), it defines the functions and runs nothing.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
