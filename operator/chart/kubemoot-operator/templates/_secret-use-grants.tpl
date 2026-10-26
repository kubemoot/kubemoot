{{- /*
  Building blocks of the secret use grant policies (templates/secret-use-grants.yaml). Every
  policy is built from these, so the checks for MCPServer and Agent cannot drift apart.
*/ -}}

{{- /* CEL: the names of the Secrets an env list (at .base) takes values from. */ -}}
{{- define "kubemoot-operator.useGrants.envSecrets" -}}
(has({{ .base }}.env) ? {{ .base }}.env.filter(e, has(e.valueFrom) && has(e.valueFrom.secretKeyRef)).map(e, e.valueFrom.secretKeyRef.name) : [])
{{- end -}}

{{- /* CEL: true when the requester holds use on the object named n. */ -}}
{{- define "kubemoot-operator.useGrants.authz" -}}
authorizer.group('').resource('{{ . }}').namespace(object.metadata.namespace).name(n).check('use').allowed()
{{- end -}}

{{- /* One check: every name in variable .var is empty (unset), the default account, or granted. */ -}}
{{- define "kubemoot-operator.useGrants.check" -}}
{{- $authz := include "kubemoot-operator.useGrants.authz" .resource -}}
{{- $skip := ternary "n == 'default' || " "" (eq .resource "serviceaccounts") -}}
{{- if .nested -}}
variables.{{ .var }}.all(l, l.all(n, n == '' || {{ $authz }}))
{{- else -}}
variables.{{ .var }}.all(n, n == '' || {{ $skip }}{{ $authz }})
{{- end -}}
{{- end -}}

{{- /* The rejection message. It cannot use the authorizer, so it lists the names in the field. */ -}}
{{- define "kubemoot-operator.useGrants.message" -}}
{{- $prefix := ternary "serviceaccounts/" "secrets/" (eq .resource "serviceaccounts") -}}
{{- $names := ternary (printf "variables.%s.map(l, l.map(n, '%s' + n).join(', ')).filter(t, t != '').join(', ')" .var $prefix) (printf "variables.%s.map(n, '%s' + n).join(', ')" .var $prefix) (default false .nested) -}}
'{{ .kind }} {{ .field }}: ' + request.userInfo.username + ' needs the verb "use" on every one of: ' + {{ $names }} + '. Ask an admin for a Role with that verb and resourceNames.'
{{- end -}}

{{- /*
  One ValidatingAdmissionPolicy. Takes root (the chart context), kind, resource, suffix,
  exempt (a CEL list literal of user names), vars (list of {name, expr}) and checks (list of
  {field, var, resource, nested}). Each check is its own validation because a message cannot
  see the authorizer: it names the field and the objects in it.
*/ -}}
{{- define "kubemoot-operator.useGrants.policy" -}}
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: kubemoot-secret-use-{{ .suffix }}
  labels:
    {{- include "kubemoot-operator.labels" .root | nindent 4 }}
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: ["kubemoot.ai"]
        apiVersions: ["v1alpha1"]
        operations: ["CREATE", "UPDATE"]
        resources: [{{ .resource | quote }}]
  matchConditions:
    - name: not-exempt-user
      expression: {{ printf "!(request.userInfo.username in %s)" .exempt | toJson }}
    - name: spec-changed
      expression: "request.operation == 'CREATE' || object.spec != oldObject.spec"
  variables:
    {{- range .vars }}
    - name: {{ .name }}
      expression: {{ .expr | toJson }}
    {{- end }}
  validations:
    {{- $kind := .kind }}
    {{- range .checks }}
    - expression: {{ include "kubemoot-operator.useGrants.check" . | toJson }}
      reason: Forbidden
      messageExpression: {{ include "kubemoot-operator.useGrants.message" (merge (dict "kind" $kind) .) | toJson }}
    {{- end }}
{{- end -}}
