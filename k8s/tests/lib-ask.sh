#!/usr/bin/env bash
# Helpers for the smoke test's "ask a crew a question" check. Sourced by test-smoke.sh;
# the pure functions are tested by ask.test.sh. Needs jq (and, to talk to the bus, nats).

# First Ready crew from `kubectl get crews -A -o json` on stdin, as "<namespace> <crew>".
# Prints nothing when no crew is Ready.
first_ready_crew() {
    jq -r '[.items[]? | select(.status.phase == "Ready")][0] // empty | "\(.metadata.namespace) \(.metadata.name)"'
}

# The question a person asks in the dashboard: a thread_start on the crew's general channel.
# Args: threadId messageId namespace crew question
thread_start_json() {
    jq -cn --arg t "$1" --arg m "$2" --arg ns "$3" --arg c "$4" --arg q "$5" \
        '{messageId: $m, threadId: $t, agentName: "human", messageType: "thread_start",
          content: $q, channel: "general", metadata: {crew: $c, namespace: $ns}}'
}

# A queued request for the crew's coordinator (kubemoot.request.<ns>.<crew>).
# Args: conversationId namespace crew question
request_json() {
    jq -cn --arg id "$1" --arg ns "$2" --arg c "$3" --arg q "$4" \
        '{message: $q, conversationId: $id, namespace: $ns, crew: $c,
          timestamp: (now | strftime("%Y-%m-%dT%H:%M:%SZ")), use_rag: false, use_tools: false}'
}

# Exit 0 when the capture file (one JSON message per line) holds a synthesis on the thread.
# Args: file threadId
has_synthesis() {
    jq -R -e --arg t "$2" 'fromjson? | select(.threadId == $t and .messageType == "synthesis")' \
        "$1" >/dev/null 2>&1
}

# The thread the crew started for a queued request: the threadId of the first message whose
# content carries the marker. Prints nothing until one is seen. Args: file marker
thread_for_marker() {
    jq -R -r --arg k "$2" 'fromjson? | select((.content // "") | contains($k)) | .threadId' \
        "$1" 2>/dev/null | head -n 1
}

# One line naming the last message on the thread, for a failure report. Args: file threadId
last_message_on() {
    local line
    line=$(jq -R -r --arg t "$2" \
        'fromjson? | select(.threadId == $t) | "\(.messageType) from \(.agentName)"' "$1" 2>/dev/null | tail -n 1)
    echo "${line:-no message seen on the thread}"
}

# Wait for a state, not a duration: poll `check_cmd` until it succeeds, the capture process
# dies, or the safety timeout passes. Returns 0 on success, 1 on timeout, 2 when the
# capture process died. Args: check_cmd capture_pid timeout_seconds
wait_until() {
    local check="$1" pid="$2" timeout="$3" start
    start=$(date +%s)
    while ! eval "$check"; do
        kill -0 "$pid" 2>/dev/null || return 2
        [ $(($(date +%s) - start)) -lt "$timeout" ] || return 1
        sleep 3
    done
}

# Ask one crew a question by `how` (bus or queue) and wait for its synthesis.
# Args: how namespace crew nats_url timeout_seconds. Prints progress; returns 0 on an answer.
ask_crew() {
    local how="$1" ns="$2" crew="$3" url="$4" timeout="$5"
    local marker log thread payload rc=0
    marker="smoke-$(date +%s)-$RANDOM"
    local question="Smoke check ${marker}: reply with one short sentence."
    log=$(mktemp)
    nats sub -s "$url" --raw "kubemoot.discuss.${ns}.${crew}.>" >"$log" 2>&1 &
    local sub=$!
    sleep 1
    if [ "$how" = bus ]; then
        thread="smoke${RANDOM}${RANDOM}"
        payload=$(thread_start_json "$thread" "msg-${marker}" "$ns" "$crew" "$question")
        nats pub -s "$url" "kubemoot.discuss.${ns}.${crew}.general.${thread}" "$payload" >/dev/null || rc=3
    else
        payload=$(request_json "$marker" "$ns" "$crew" "$question")
        nats pub -s "$url" "kubemoot.request.${ns}.${crew}" "$payload" >/dev/null || rc=3
        if [ "$rc" = 0 ]; then
            wait_until "[ -n \"\$(thread_for_marker '$log' '$marker')\" ]" "$sub" "$timeout" || rc=$?
            thread=$(thread_for_marker "$log" "$marker")
            [ -n "$thread" ] || thread="(no thread started for $marker)"
        fi
    fi
    if [ "$rc" = 0 ]; then
        wait_until "has_synthesis '$log' '$thread'" "$sub" "$timeout" || rc=$?
    fi
    kill "$sub" 2>/dev/null || true
    case "$rc" in
        0) echo "answered on thread ${thread}" ;;
        3) echo "publish failed" ;;
        2) echo "bus subscription ended early; thread ${thread}" ;;
        *) echo "no synthesis within ${timeout}s on thread ${thread}; last message: $(last_message_on "$log" "$thread")" ;;
    esac
    rm -f "$log"
    return "$rc"
}
