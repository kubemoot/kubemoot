{{/*
Expand the name of the chart.
*/}}
{{- define "kubemoot-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "kubemoot-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "kubemoot-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kubemoot-operator.labels" -}}
helm.sh/chart: {{ include "kubemoot-operator.chart" . }}
{{ include "kubemoot-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kubemoot-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubemoot-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kubemoot-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kubemoot-operator.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Image tag - defaults to appVersion
*/}}
{{- define "kubemoot-operator.imageTag" -}}
{{- .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
Resolve an image reference. A bare "name:tag" is prefixed with global.imageRegistry;
a reference that already names a registry or path (contains "/") is used as-is.
Usage: include "kubemoot-operator.image" (dict "root" $ "image" "indexer:1.2.3")
*/}}
{{- define "kubemoot-operator.image" -}}
{{- if contains "/" .image -}}
{{- .image -}}
{{- else -}}
{{- printf "%s/%s" .root.Values.global.imageRegistry .image -}}
{{- end -}}
{{- end }}

{{/*
The operator's own image: global.imageRegistry/image.repository:tag
*/}}
{{- define "kubemoot-operator.operatorImage" -}}
{{- include "kubemoot-operator.image" (dict "root" . "image" (printf "%s:%s" .Values.image.repository (include "kubemoot-operator.imageTag" .))) -}}
{{- end }}

{{/*
Image pull secrets: the chart-level imagePullSecrets plus global.imagePullSecrets (the
value the dashboard subchart also reads). Renders a YAML list, empty when none are set.
Usage: with (include "kubemoot-operator.pullSecrets" . | fromYamlArray)
*/}}
{{- define "kubemoot-operator.pullSecrets" -}}
{{- $global := (.Values.global | default dict).imagePullSecrets | default list -}}
{{- $all := concat (.Values.imagePullSecrets | default list) $global -}}
{{- if $all -}}
{{- toYaml ($all | uniq) -}}
{{- end -}}
{{- end }}

{{/*
The scheduling crew's name (scheduling-crew.yaml); the operator is told it too.
*/}}
{{- define "kubemoot-operator.schedulingCrew" -}}
kubemoot-scheduling
{{- end }}

{{/*
A Model object name for a catalog entry: the server name and the model, such as
ollama-gpu-qwen3-14b for qwen3:14b on ollama-gpu. Usage: include "..." (list serverName model)
*/}}
{{- define "kubemoot-operator.modelObjectName" -}}
{{- printf "%s-%s" (index . 0) (regexReplaceAll "[^a-z0-9-]+" (lower (index . 1)) "-") | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
A label map as "key=value,key=value", sorted by key, for the operator environment.
Usage: include "kubemoot-operator.labelList" $map
*/}}
{{- define "kubemoot-operator.labelList" -}}
{{- $out := list -}}
{{- range $k, $v := (. | default dict) -}}
{{- $out = append $out (printf "%s=%v" $k $v) -}}
{{- end -}}
{{- join "," $out -}}
{{- end }}

{{/*
One coordinator setting as "true" or "false": the value in coordinator.<key>, or the default when
the key is absent or null. A plain "default" would read an explicit false as unset.
Usage: include "kubemoot-operator.coordinatorFlag" (dict "root" . "key" "eventLog" "default" "true")
*/}}
{{- define "kubemoot-operator.coordinatorFlag" -}}
{{- $coordinator := .root.Values.coordinator | default dict -}}
{{- $value := index $coordinator .key -}}
{{- if kindIs "invalid" $value -}}
{{- .default -}}
{{- else -}}
{{- toString $value -}}
{{- end -}}
{{- end }}

{{/*
Whether each coordinator writes its decision log. On unless coordinator.eventLog is false.
*/}}
{{- define "kubemoot-operator.coordinatorEventLog" -}}
{{- include "kubemoot-operator.coordinatorFlag" (dict "root" . "key" "eventLog" "default" "true") -}}
{{- end }}

{{/*
Whether a restarted coordinator resumes its threads from the log. On unless coordinator.resume is
false. Resume rebuilds a thread from the decision log, so resume with the log off is refused.
*/}}
{{- define "kubemoot-operator.coordinatorResume" -}}
{{- $resume := include "kubemoot-operator.coordinatorFlag" (dict "root" . "key" "resume" "default" "true") -}}
{{- if and (eq $resume "true") (eq (include "kubemoot-operator.coordinatorEventLog" .) "false") -}}
{{- fail "coordinator.resume needs coordinator.eventLog: a coordinator resumes a thread from its decision log. Resume is on by default, so a release that sets coordinator.eventLog to false must also set coordinator.resume to false" -}}
{{- end -}}
{{- $resume -}}
{{- end }}

{{/*
Whether more than one coordinator replica may run, each owning the threads it holds a lease for.
Leases take a thread over through resume, so lease without resume is refused.
*/}}
{{- define "kubemoot-operator.coordinatorLease" -}}
{{- $lease := include "kubemoot-operator.coordinatorFlag" (dict "root" . "key" "lease" "default" "false") -}}
{{- if and (eq $lease "true") (eq (include "kubemoot-operator.coordinatorResume" .) "false") -}}
{{- fail "coordinator.lease needs coordinator.resume (and coordinator.eventLog): a replica takes a thread over by resuming it from its log" -}}
{{- end -}}
{{- $lease -}}
{{- end }}

{{/*
The oldest a thread's last logged event may be for a restarted coordinator to resume it: a Go
duration such as 30m or 1h30m. Empty or 0 turns the limit off and renders as "0". Anything else is refused.
*/}}
{{- define "kubemoot-operator.coordinatorResumeMaxAge" -}}
{{- $coordinator := .Values.coordinator | default dict -}}
{{- $age := toString ($coordinator.resumeMaxAge | default "") | trim -}}
{{- if or (eq $age "") (eq $age "0") -}}
0
{{- else if regexMatch "^([0-9]+(\\.[0-9]+)?(ns|us|ms|s|m|h))+$" $age -}}
{{- $age -}}
{{- else -}}
{{- fail (printf "coordinator.resumeMaxAge must be a duration such as 30m or 1h30m, or 0 for no limit, got %q" $age) -}}
{{- end -}}
{{- end }}

{{/*
The workspace query engine, none, sqlite or duckdb; any other value fails the render. It is
workspace.queryEngine, else the old artifacts.queryEngine, else sqlite.
*/}}
{{- define "kubemoot-operator.workspaceQueryEngine" -}}
{{- $engine := (.Values.workspace | default dict).queryEngine | default ((.Values.artifacts | default dict).queryEngine) | default "sqlite" -}}
{{- if not (has $engine (list "none" "sqlite" "duckdb")) -}}
{{- fail (printf "workspace.queryEngine must be none, sqlite or duckdb, got %q" $engine) -}}
{{- end -}}
{{- $engine -}}
{{- end }}

{{/*
The cluster-wide default workspace access, or empty when none is set. Any value but none, read,
write or readWrite fails the render.
*/}}
{{- define "kubemoot-operator.workspaceDefaultAccess" -}}
{{- $access := (.Values.workspace | default dict).defaultAccess | default "" -}}
{{- if and $access (not (has $access (list "none" "read" "write" "readWrite"))) -}}
{{- fail (printf "workspace.defaultAccess must be none, read, write or readWrite, got %q" $access) -}}
{{- end -}}
{{- $access -}}
{{- end }}

{{/*
The NATS connection options (nats.tls and nats.auth), checked. A credential Secret needs
exactly one kind of credential: a creds key, a user key with a password key, or a token key.
Renders nothing; fails the render on a combination that cannot work.
*/}}
{{- define "kubemoot-operator.natsConnCheck" -}}
{{- $a := (.Values.nats.auth | default dict) -}}
{{- $kinds := 0 -}}
{{- if $a.credsKey }}{{ $kinds = add1 $kinds }}{{ end -}}
{{- if or $a.userKey $a.passwordKey }}{{ $kinds = add1 $kinds }}{{ end -}}
{{- if $a.tokenKey }}{{ $kinds = add1 $kinds }}{{ end -}}
{{- if and $a.secret (ne (int $kinds) 1) -}}
{{- fail "nats.auth.secret needs exactly one of nats.auth.credsKey, nats.auth.userKey with nats.auth.passwordKey, or nats.auth.tokenKey" -}}
{{- end -}}
{{- if and (not $a.secret) (gt (int $kinds) 0) -}}
{{- fail "nats.auth.credsKey, userKey, passwordKey and tokenKey need nats.auth.secret" -}}
{{- end -}}
{{- if and $a.userKey (not $a.passwordKey) -}}
{{- fail "nats.auth.userKey needs nats.auth.passwordKey" -}}
{{- end -}}
{{- if and $a.passwordKey (not $a.userKey) -}}
{{- fail "nats.auth.passwordKey needs nats.auth.userKey" -}}
{{- end -}}
{{- end }}

{{/*
Environment of a workload that dials NATS: the names every Kubemoot component reads the same
way (docs/operating/nats-connection.md). Pass the root context as "root"; pass "operator" true
for the operator, which also gets the names it hands on to the workloads it creates.
*/}}
{{- define "kubemoot-operator.natsConnEnv" -}}
{{- include "kubemoot-operator.natsConnCheck" .root -}}
{{- $tls := (.root.Values.nats.tls | default dict) -}}
{{- $a := (.root.Values.nats.auth | default dict) -}}
{{- if $tls.caSecret }}
- name: NATS_CA_FILE
  value: /etc/kubemoot/nats-conn/ca/ca.crt
{{- end }}
{{- if $tls.clientSecret }}
- name: NATS_CLIENT_CERT_FILE
  value: /etc/kubemoot/nats-conn/client/tls.crt
- name: NATS_CLIENT_KEY_FILE
  value: /etc/kubemoot/nats-conn/client/tls.key
{{- end }}
{{- if and $a.secret (not .skipAuth) }}
{{- if $a.credsKey }}
- name: NATS_CREDS_FILE
  value: /etc/kubemoot/nats-conn/auth/nats.creds
{{- else if $a.userKey }}
- name: NATS_USER
  valueFrom:
    secretKeyRef:
      name: {{ $a.secret | quote }}
      key: {{ $a.userKey | quote }}
- name: NATS_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ $a.secret | quote }}
      key: {{ $a.passwordKey | quote }}
{{- else if $a.tokenKey }}
- name: NATS_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ $a.secret | quote }}
      key: {{ $a.tokenKey | quote }}
{{- end }}
{{- end }}
{{- if .operator }}
{{- if $tls.caSecret }}
- name: KUBEMOOT_NATS_CA_SECRET
  value: {{ $tls.caSecret | quote }}
- name: KUBEMOOT_NATS_CA_KEY
  value: {{ $tls.caKey | default "ca.crt" | quote }}
{{- end }}
{{- if $tls.clientSecret }}
- name: KUBEMOOT_NATS_CLIENT_SECRET
  value: {{ $tls.clientSecret | quote }}
{{- end }}
{{- if $a.secret }}
- name: KUBEMOOT_NATS_AUTH_SECRET
  value: {{ $a.secret | quote }}
{{- with $a.credsKey }}
- name: KUBEMOOT_NATS_AUTH_CREDS_KEY
  value: {{ . | quote }}
{{- end }}
{{- with $a.userKey }}
- name: KUBEMOOT_NATS_AUTH_USER_KEY
  value: {{ . | quote }}
{{- end }}
{{- with $a.passwordKey }}
- name: KUBEMOOT_NATS_AUTH_PASSWORD_KEY
  value: {{ . | quote }}
{{- end }}
{{- with $a.tokenKey }}
- name: KUBEMOOT_NATS_AUTH_TOKEN_KEY
  value: {{ . | quote }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{/* The volume mounts of a container that dials NATS; pair with natsConnVolumes. */}}
{{- define "kubemoot-operator.natsConnMounts" -}}
{{- $tls := (.Values.nats.tls | default dict) -}}
{{- $a := (.Values.nats.auth | default dict) -}}
{{- if $tls.caSecret }}
- name: nats-conn-ca
  mountPath: /etc/kubemoot/nats-conn/ca
  readOnly: true
{{- end }}
{{- if $tls.clientSecret }}
- name: nats-conn-client
  mountPath: /etc/kubemoot/nats-conn/client
  readOnly: true
{{- end }}
{{- if and $a.secret $a.credsKey }}
- name: nats-conn-auth
  mountPath: /etc/kubemoot/nats-conn/auth
  readOnly: true
{{- end }}
{{- end }}

{{/* The pod volumes behind natsConnMounts: the CA bundle, the client certificate, the creds file. */}}
{{- define "kubemoot-operator.natsConnVolumes" -}}
{{- $tls := (.Values.nats.tls | default dict) -}}
{{- $a := (.Values.nats.auth | default dict) -}}
{{- if $tls.caSecret }}
- name: nats-conn-ca
  secret:
    secretName: {{ $tls.caSecret | quote }}
    items:
      - key: {{ $tls.caKey | default "ca.crt" | quote }}
        path: ca.crt
{{- end }}
{{- if $tls.clientSecret }}
- name: nats-conn-client
  secret:
    secretName: {{ $tls.clientSecret | quote }}
{{- end }}
{{- if and $a.secret $a.credsKey }}
- name: nats-conn-auth
  secret:
    secretName: {{ $a.secret | quote }}
    items:
      - key: {{ $a.credsKey | quote }}
        path: nats.creds
{{- end }}
{{- end }}

{{/*
Renders a natsConn* helper's output indented for a list under a container or pod key, or
nothing when the helper rendered nothing. Pass "name" (the helper), "ctx" (what to pass it) and
"indent".
*/}}
{{- define "kubemoot-operator.natsConnBlock" -}}
{{- $out := include .name .ctx | trim -}}
{{- if $out }}
{{ $out | nindent (int .indent) }}
{{- end }}
{{- end }}

{{/*
GOMEMLIMIT in MiB for a Go container: goMemLimitPercent of the memory limit in its resources
map. Pass "root" and "resources" (the whole map, which may be empty). Fails the render unless
goMemLimitPercent is a whole number from 0 to 100. Renders nothing when the percent is 0 or
the limit is not a whole number of Mi or Gi, so a limit written any other way gets no GOMEMLIMIT
rather than a wrong one.
*/}}
{{- define "kubemoot-operator.goMemLimit" -}}
{{- $raw := toString .root.Values.goMemLimitPercent -}}
{{- if not (regexMatch "^[0-9]+$" $raw) -}}
{{- fail (printf "goMemLimitPercent must be a whole number from 0 to 100, got %q" $raw) -}}
{{- end -}}
{{- $pct := int $raw -}}
{{- if gt $pct 100 -}}
{{- fail (printf "goMemLimitPercent must be a whole number from 0 to 100, got %d" $pct) -}}
{{- end -}}
{{- $mem := toString (dig "limits" "memory" "" (.resources | default dict)) -}}
{{- if and (gt $pct 0) (regexMatch "^[0-9]+(Mi|Gi)$" $mem) -}}
{{- $n := int (regexFind "^[0-9]+" $mem) -}}
{{- if hasSuffix "Gi" $mem }}{{ $n = mul $n 1024 }}{{ end -}}
{{- printf "%dMiB" (div (mul $n $pct) 100) -}}
{{- end -}}
{{- end }}

{{/*
The GOMEMLIMIT list item for a Go container's env, indented by "indent" with a leading newline,
or nothing. Pass "root", "resources" (the container's whole resources map) and "indent".
*/}}
{{- define "kubemoot-operator.goMemLimitEnv" -}}
{{- $v := include "kubemoot-operator.goMemLimit" . -}}
{{- if $v -}}
{{- $pad := repeat (int .indent) " " -}}
{{- printf "\n%s- name: GOMEMLIMIT\n%s  value: %s" $pad $pad ($v | quote) -}}
{{- end -}}
{{- end }}
