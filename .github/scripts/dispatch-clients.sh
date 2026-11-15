#!/usr/bin/env bash
# Asks the repositories that build against Kubemoot's finals (the crews, kmctl and CrewForge)
# for a new release candidate: a repository_dispatch event "kubemoot-published" carrying the
# operator chart version just published. Publish Release runs this after its release job;
# publishing those candidates stays a Publish Release run in each repository.
#
# Usage: dispatch-clients.sh [repo ...]   (default: crews kmctl vscode-crewforge)
# Environment:
#   DRY_RUN        "true" (default) dispatches nothing and prints what would be dispatched
#   CHART_VERSION  the operator chart version just published
#   GH_TOKEN       a token with contents write on each repository (RELEASE_TOKEN); not needed
#                  in a dry run
#   RELEASE_STATUS_OWNER  the GitHub owner of the repositories (default kubemoot)
# Every repository is tried even when an earlier one fails; exit 1 when any dispatch failed.
# Tested by test-dispatch-clients.sh.
set -euo pipefail

OWNER="${RELEASE_STATUS_OWNER:-kubemoot}"
DRY_RUN="${DRY_RUN:-true}"
EVENT_TYPE="kubemoot-published"
repos=("$@")
[ "${#repos[@]}" -gt 0 ] || repos=(crews kmctl vscode-crewforge)

case "$DRY_RUN" in
  true|false) ;;
  *) echo "dispatch-clients: DRY_RUN must be true or false, got '${DRY_RUN}'" >&2; exit 64 ;;
esac
if [ "$DRY_RUN" = false ] && [ -z "${CHART_VERSION:-}" ]; then
  echo "dispatch-clients: CHART_VERSION is required for a real dispatch" >&2
  exit 64
fi
# The payload is a published final version: X.Y.Z, nothing else reaches the other repositories.
if [ -n "${CHART_VERSION:-}" ] && ! [[ "$CHART_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "dispatch-clients: CHART_VERSION must be a version X.Y.Z, got '${CHART_VERSION}'" >&2
  exit 64
fi

failed=0
for repo in "${repos[@]}"; do
  if [ "$DRY_RUN" = true ]; then
    echo "Dry run: would dispatch ${EVENT_TYPE} to ${OWNER}/${repo} (chart_version ${CHART_VERSION:-unknown})"
    continue
  fi
  if gh api -X POST "repos/${OWNER}/${repo}/dispatches" \
      -f "event_type=${EVENT_TYPE}" -f "client_payload[chart_version]=${CHART_VERSION}" >/dev/null; then
    echo "Dispatched ${EVENT_TYPE} to ${OWNER}/${repo} (chart_version ${CHART_VERSION})"
  else
    echo "FAILED to dispatch ${EVENT_TYPE} to ${OWNER}/${repo}"
    failed=1
  fi
done
exit "$failed"
