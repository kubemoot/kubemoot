#!/usr/bin/env bash
# Tests for the NATS bootstrap job's script, run against a fake `nats` command that
# keeps its buckets in a directory: the retired kubemoot_provider_* buckets are
# removed when present, an absent one is not an error, the new tickets bucket is
# created, and no other bucket is touched. Needs helm and yq.
# Run from anywhere: operator/chart/test-nats-bootstrap.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null
fail() { echo "FAIL: $*" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/kv"

# The job's script, as rendered.
helm template t "$chart" --show-only templates/nats-streams-job.yaml \
  | yq 'select(.kind == "Job") | .spec.template.spec.containers[0].args[0]' >"$work/bootstrap.sh"
[ -s "$work/bootstrap.sh" ] || fail "no bootstrap script rendered"

# A fake nats: kv add creates a bucket, kv info succeeds only for one that exists,
# kv del removes it; every other command succeeds and does nothing.
cat >"$work/bin/nats" <<'EOF'
#!/bin/sh
echo "$*" >>"$FAKE_LOG"
if [ "$1" = "kv" ]; then
  case "$2" in
    add) : >"$FAKE_KV/$3" ;;
    info) [ -e "$FAKE_KV/$3" ] || exit 1 ;;
    del) case "$*" in *--force*) ;; *) exit 1 ;; esac; [ -e "$FAKE_KV/$3" ] || exit 1; rm -f "$FAKE_KV/$3" ;;
  esac
fi
exit 0
EOF
chmod +x "$work/bin/nats"

run_bootstrap() {
  : >"$work/log"
  FAKE_LOG="$work/log" FAKE_KV="$work/kv" PATH="$work/bin:$PATH" sh "$work/bootstrap.sh" >"$work/out" 2>&1 \
    || { cat "$work/out" >&2; fail "bootstrap exited non-zero"; }
}
buckets() { ls "$work/kv" | sort | tr '\n' ' '; }

# Only one retired bucket exists (a half-finished earlier run), plus an unrelated one.
: >"$work/kv/kubemoot_provider_tickets"
: >"$work/kv/unrelated_bucket"
run_bootstrap
[ ! -e "$work/kv/kubemoot_provider_tickets" ] || fail "the lone retired bucket was not removed"
[ "$(grep -c '^kv del ' "$work/log")" = "1" ] || fail "kv del ran other than once"
rm -f "$work/kv/kubemoot_server_tickets"

# Both retired buckets exist.
: >"$work/kv/kubemoot_provider_state"
: >"$work/kv/kubemoot_provider_tickets"
: >"$work/kv/unrelated_bucket"
run_bootstrap
[ ! -e "$work/kv/kubemoot_provider_state" ] || fail "kubemoot_provider_state was not removed"
[ ! -e "$work/kv/kubemoot_provider_tickets" ] || fail "kubemoot_provider_tickets was not removed"
[ -e "$work/kv/kubemoot_server_tickets" ] || fail "kubemoot_server_tickets was not created"
[ -e "$work/kv/unrelated_bucket" ] || fail "an unrelated bucket was removed"
[ -e "$work/kv/kubemoot_placement_leader" ] || fail "kubemoot_placement_leader was not created"
grep -q '^kv add kubemoot_placement_leader --ttl=15s --history=1 --max-value-size=1024 ' "$work/log" || fail "the leader bucket has the wrong settings"
grep -q '^kv del kubemoot_provider_state --force' "$work/log" || fail "kv del ran without --force"
grep -q "Removing retired KV bucket kubemoot_provider_state" "$work/out" || fail "the removal was not logged"
[ "$(grep -c '^kv del ' "$work/log")" = "2" ] || fail "kv del ran other than twice: $(grep '^kv del ' "$work/log")"

grep -q '^stream add KUBEMOOT_COORDINATOR --subjects=kubemoot.coordinator.> --retention=limits --max-age=24h ' "$work/log" \
  || fail "the coordinator log stream is missing or has the wrong settings"
grep -q -- '--max-msgs-per-subject=512' "$work/log" || fail "the coordinator log stream has no per-thread bound"

# Run again: the retired buckets are absent, which is not an error, and nothing else is deleted.
run_bootstrap
grep -q "kubemoot_provider_state is not present" "$work/out" || fail "an absent bucket was not reported"
[ "$(grep -c '^kv del ' "$work/log")" = "0" ] || fail "kv del ran with nothing to remove"
[ -e "$work/kv/unrelated_bucket" ] || fail "an unrelated bucket was removed on the second run"
[ -e "$work/kv/kubemoot_server_tickets" ] || fail "kubemoot_server_tickets was lost"

echo "PASS: nats bootstrap tests ($(buckets))"
