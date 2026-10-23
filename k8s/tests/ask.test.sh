#!/usr/bin/env bash
# Tests for the pure helpers in lib-ask.sh.
# Usage: bash k8s/tests/ask.test.sh   (exit 0 = all passed)
set -euo pipefail

# shellcheck source=k8s/tests/lib-ask.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib-ask.sh"
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

ready() { printf '{"metadata":{"namespace":"%s","name":"%s"},"status":{"phase":"%s"}}' "$1" "$2" "${3:-Ready}"; }
agent() { # namespace crew role channels-json
  printf '{"metadata":{"namespace":"%s","ownerReferences":[{"kind":"Crew","name":"%s"}]},"spec":{"discussRole":"%s","discussChannels":%s}}' "$1" "$2" "$3" "$4"
}
crews="{\"items\":[$(ready a x Degraded),$(ready grade g),$(ready pilot p),$(ready other o)]}"
agents_file=$(mktemp)
printf '{"items":[%s,%s,%s,%s]}' \
  "$(agent grade g coordinator '[]')" "$(agent pilot p coordinator '["kubernetes","general"]')" \
  "$(agent other o tooler '["general"]')" "$(agent a x coordinator '["general"]')" >"$agents_file"
check "crew without discussion channels is skipped" "pilot p" "$(printf '%s' "$crews" | first_discussion_crew "$agents_file")"
printf '{"items":[%s]}' "$(agent grade g coordinator '["library"]')" >"$agents_file"
check "no crew when no coordinator has the general channel" "" "$(printf '%s' "$crews" | first_discussion_crew "$agents_file")"
printf '{"items":[]}' >"$agents_file"
check "no crew when there are no agents" "" "$(printf '%s' "$crews" | first_discussion_crew "$agents_file")"
printf '%s' "$crews" >"$agents_file.crews"
check "SMOKE_CREW is honored" "ns1 c1" "$(SMOKE_CREW=ns1/c1 pick_crew "$agents_file.crews" "$agents_file")"
check "SMOKE_CREW unset uses the rule" "" "$(unset SMOKE_CREW; pick_crew "$agents_file.crews" "$agents_file")"
rm -f "$agents_file.crews"
check "stop message is a manual stop on the thread" "stop_requested human t1 general manual_stop" \
  "$(stop_json t1 n1 c1 | jq -r '[.messageType,.agentName,.threadId,.channel,.metadata.reason]|join(" ")')"

check "thread start fields" "human thread_start general t1 n1 c1" \
  "$(thread_start_json t1 m1 n1 c1 'hi' | jq -r '[.agentName,.messageType,.channel,.threadId,.metadata.namespace,.metadata.crew]|join(" ")')"
check "thread start keeps a quote in the question" 'say "hi"' \
  "$(thread_start_json t1 m1 n1 c1 'say "hi"' | jq -r .content)"
check "request has every required field" "true" \
  "$(request_json k n1 c1 'q' | jq '[.message,.conversationId,.namespace,.crew,.timestamp,.use_rag,.use_tools]|all(. != null)')"
check "request timestamp is RFC 3339" "true" \
  "$(request_json k n1 c1 'q' | jq '.timestamp|test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z$")')"

log=$(mktemp)
trap 'rm -f "$log" "$agents_file"' EXIT
printf '%s\n' \
  'not json' \
  '{"threadId":"t1","messageType":"thread_start","agentName":"human","content":"Smoke check k1"}' \
  '{"threadId":"t1","messageType":"proposal","agentName":"a1","content":"x"}' \
  '{"threadId":"t2","messageType":"synthesis","agentName":"coord","content":"other thread"}' >"$log"
has_synthesis "$log" t1 && r=yes || r=no
check "no synthesis on a thread that has none" "no" "$r"
printf '%s\n' '{"threadId":"t1","messageType":"synthesis","agentName":"coord","content":"done"}' >>"$log"
has_synthesis "$log" t1 && r=yes || r=no
check "synthesis on the thread" "yes" "$r"
has_synthesis "$log" t3 && r=yes || r=no
check "synthesis on another thread does not count" "no" "$r"
has_synthesis /nonexistent t1 && r=yes || r=no
check "missing capture file is no answer" "no" "$r"
check "thread found by marker" "t1" "$(thread_for_marker "$log" k1)"
check "no thread for an unseen marker" "" "$(thread_for_marker "$log" k9)"
check "last message on the thread" "synthesis from coord" "$(last_message_on "$log" t1)"
check "last message when the thread is silent" "no message seen on the thread" "$(last_message_on "$log" t9)"

sleep 30 &
pid=$!
wait_until true "$pid" 5 && r=0 || r=$?
check "wait ends at once when the state holds" "0" "$r"
wait_until false "$pid" 1 && r=0 || r=$?
check "wait gives up at the safety timeout" "1" "$r"
kill "$pid" 2>/dev/null || true
wait "$pid" 2>/dev/null || true
wait_until false "$pid" 5 && r=0 || r=$?
check "wait reports a dead capture process" "2" "$r"

# A question that gets no synthesis closes its thread. Fake nats: sub stays silent, pub is recorded.
pubs=$(mktemp)
nats() { case "$1" in sub) exec sleep 20 ;; pub) printf '%s %s\n' "$4" "$5" >>"$pubs" ;; esac; }
ask_crew bus ns1 c1 nats://x 1 >/dev/null && r=0 || r=$?
check "unanswered question reports a timeout" "1" "$r"
check "cleanup stop goes to the thread's subject on timeout" "1" \
  "$(grep -c '^kubemoot.discuss.ns1.c1.general.smoke.* {.*"stop_requested"' "$pubs")"
: >"$pubs"
nats() { case "$1" in sub) exec sleep 20 ;; pub) return 1 ;; esac; }
ask_crew bus ns1 c1 nats://x 1 >/dev/null && r=0 || r=$?
check "no cleanup when the publish itself failed" "3" "$r"
rm -f "$pubs"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
