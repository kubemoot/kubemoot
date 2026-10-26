#!/usr/bin/env bash
# Render tests for the secret use grant policies (templates/secret-use-grants.yaml). Needs helm
# and yq. Run from anywhere: operator/chart/test-secret-use-grants.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template "$@" "$chart"; }
# exempt <rendered> <policy name>: the user list of the first match condition, one per line.
exempt() {
  yq -N "select(.kind == \"ValidatingAdmissionPolicy\" and .metadata.name == \"$2\") | .spec.matchConditions[0].expression" <<<"$1" \
    | sed -e 's/^!(request.userInfo.username in \[//' -e 's/\])$//' | tr ',' '\n' | tr -d '" '
}

# Flag off (the default): nothing is rendered.
out="$(render kubemoot-operator)"
grep -q 'kubemoot-secret-use' <<<"$out" && fail "default render contains use-grant policies or bindings"
grep -q 'kubemoot-kubernetes-mcp-use' <<<"$out" && fail "default render contains the kubernetes MCP use grant"

# Flag on, defaults: one policy and one Deny binding each for MCPServer and Agent; the operator is
# exempt under the name the API server gives its ServiceAccount.
out="$(render kubemoot-operator --namespace km --set security.secretUseGrants.enabled=true)"
for kind in mcpserver agent; do
  n="$(yq -N "select(.kind == \"ValidatingAdmissionPolicy\" and .metadata.name == \"kubemoot-secret-use-$kind\") | .metadata.name" <<<"$out")"
  [ "$n" = "kubemoot-secret-use-$kind" ] || fail "no policy for $kind"
  b="$(yq -N "select(.kind == \"ValidatingAdmissionPolicyBinding\" and .metadata.name == \"kubemoot-secret-use-$kind\") | .spec.policyName + \" \" + (.spec.validationActions | join(\",\"))" <<<"$out")"
  [ "$b" = "kubemoot-secret-use-$kind Deny" ] || fail "binding for $kind is '$b', want Deny"
  l="$(yq -N "select(.kind == \"ValidatingAdmissionPolicy\" and .metadata.name == \"kubemoot-secret-use-$kind\") | .metadata.labels[\"app.kubernetes.io/managed-by\"]" <<<"$out")"
  [ "$l" = "Helm" ] || fail "policy $kind has no chart labels"
  [ "$(yq -N "select(.kind == \"ValidatingAdmissionPolicy\" and .metadata.name == \"kubemoot-secret-use-$kind\") | .spec.failurePolicy" <<<"$out")" = "Fail" ] || fail "failurePolicy is not Fail"
  [ "$(exempt "$out" "kubemoot-secret-use-$kind")" = "system:serviceaccount:km:kubemoot-operator" ] \
    || fail "$kind exempt list is '$(exempt "$out" "kubemoot-secret-use-$kind")'"
done
grep -q 'matchResources' <<<"$out" && fail "no namespaceSelector was set but the binding has matchResources"

# The kubernetes MCP server's account may use its own ServiceAccount, and only that.
r="$(yq -N 'select(.kind == "Role" and .metadata.name == "kubemoot-kubernetes-mcp-use") | .rules[0].resources[0] + "/" + .rules[0].resourceNames[0] + "/" + .rules[0].verbs[0]' <<<"$out")"
[ "$r" = "serviceaccounts/kubemoot-kubernetes-mcp/use" ] || fail "kubernetes MCP use grant is '$r'"
s="$(yq -N 'select(.kind == "RoleBinding" and .metadata.name == "kubemoot-kubernetes-mcp-use") | .subjects[0].name + "@" + .subjects[0].namespace' <<<"$out")"
[ "$s" = "kubemoot-kubernetes-mcp@km" ] || fail "kubernetes MCP use grant binds '$s'"
out2="$(render kubemoot-operator --set security.secretUseGrants.enabled=true --set internalAgents.mcpServers.kubernetes.enabled=false)"
grep -q 'kubemoot-kubernetes-mcp-use' <<<"$out2" && fail "use grant rendered without the kubernetes MCP server"

# Every setting: serviceAccount.name, fullnameOverride, exemptUsers, namespaceSelector, validationActions.
out="$(render rel --namespace ops \
  --set security.secretUseGrants.enabled=true \
  --set serviceAccount.name=op-sa --set fullnameOverride=custom \
  --set 'security.secretUseGrants.exemptUsers={alice,system:serviceaccount:flux:applier}' \
  --set 'security.secretUseGrants.namespaceSelector.matchLabels.tier=crew' \
  --set 'security.secretUseGrants.validationActions={Audit,Warn}')"
want=$'system:serviceaccount:ops:op-sa\nalice\nsystem:serviceaccount:flux:applier'
for kind in mcpserver agent; do
  got="$(exempt "$out" "kubemoot-secret-use-$kind")"
  [ "$got" = "$want" ] || fail "$kind exempt list is '$got', want '$want'"
  b="$(yq -N "select(.kind == \"ValidatingAdmissionPolicyBinding\" and .metadata.name == \"kubemoot-secret-use-$kind\") | (.spec.validationActions | join(\",\")) + \" \" + .spec.matchResources.namespaceSelector.matchLabels.tier" <<<"$out")"
  [ "$b" = "Audit,Warn crew" ] || fail "$kind binding is '$b', want 'Audit,Warn crew'"
done

# With a fullnameOverride and no serviceAccount.name, the operator account follows the fullname.
out="$(render rel --namespace ops --set security.secretUseGrants.enabled=true --set fullnameOverride=custom)"
[ "$(exempt "$out" kubemoot-secret-use-mcpserver)" = "system:serviceaccount:ops:custom" ] \
  || fail "exempt list does not follow fullnameOverride: $(exempt "$out" kubemoot-secret-use-mcpserver)"

# A user name with a quote cannot break out of the CEL list.
out="$(render rel --set security.secretUseGrants.enabled=true --set 'security.secretUseGrants.exemptUsers={a"b}')"
# (Capture first: grep -q on a pipe can close it early and fail the pipeline under pipefail.)
cond="$(yq -N 'select(.kind == "ValidatingAdmissionPolicy") | .spec.matchConditions[0].expression' <<<"$out")"
grep -qF 'a\"b' <<<"$cond" || fail "a quote in an exempt user was not escaped: $cond"

echo "secret use grant render tests passed"
