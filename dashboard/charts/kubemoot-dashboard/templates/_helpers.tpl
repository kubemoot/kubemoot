{{/*
Expand the name of the chart.
*/}}
{{- define "kubemoot-dashboard.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "kubemoot-dashboard.fullname" -}}
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
{{- define "kubemoot-dashboard.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kubemoot-dashboard.labels" -}}
helm.sh/chart: {{ include "kubemoot-dashboard.chart" . }}
{{ include "kubemoot-dashboard.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: kubemoot
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kubemoot-dashboard.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubemoot-dashboard.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kubemoot-dashboard.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kubemoot-dashboard.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Get the image tag
*/}}
{{- define "kubemoot-dashboard.imageTag" -}}
{{- .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
Create the full image path. When global.imageRegistry is set (as it is when this chart
is the subchart of kubemoot-operator) it replaces image.registry and any path in
image.repository, so one global value points every Kubemoot image at a mirror.
*/}}
{{- define "kubemoot-dashboard.image" -}}
{{- $globalRegistry := (.Values.global | default dict).imageRegistry -}}
{{- if $globalRegistry -}}
{{- printf "%s/%s:%s" $globalRegistry (base .Values.image.repository) (include "kubemoot-dashboard.imageTag" .) -}}
{{- else -}}
{{- printf "%s/%s:%s" .Values.image.registry .Values.image.repository (include "kubemoot-dashboard.imageTag" .) -}}
{{- end -}}
{{- end }}

{{/*
Image pull secrets: imagePullSecrets plus global.imagePullSecrets. Renders a YAML list,
empty when none are set.
*/}}
{{- define "kubemoot-dashboard.pullSecrets" -}}
{{- $global := (.Values.global | default dict).imagePullSecrets | default list -}}
{{- $all := concat (.Values.imagePullSecrets | default list) $global -}}
{{- if $all -}}
{{- toYaml ($all | uniq) -}}
{{- end -}}
{{- end }}

{{/*
ClusterRole name. Defaults to <fullname>-reader when rbac.clusterRole.name is empty, so
two releases of this chart (or the operator chart's subchart and a standalone release)
never share a cluster-scoped name.
*/}}
{{- define "kubemoot-dashboard.clusterRoleName" -}}
{{- default (printf "%s-reader" (include "kubemoot-dashboard.fullname" .)) .Values.rbac.clusterRole.name -}}
{{- end }}

{{/*
Workshop instance (workshop.enabled): a second, read-only, namespace-scoped Deployment of
this same image. Its name is the dashboard's fullname plus "-workshop". Its pods carry a
different app.kubernetes.io/name, so the main Service never selects them and the
workshop Service never selects the main dashboard's pods.
*/}}
{{- define "kubemoot-dashboard.workshop.fullname" -}}
{{- printf "%s-workshop" (include "kubemoot-dashboard.fullname" . | trunc 54 | trimSuffix "-") -}}
{{- end }}

{{- define "kubemoot-dashboard.workshop.selectorLabels" -}}
app.kubernetes.io/name: {{ printf "%s-workshop" (include "kubemoot-dashboard.name" . | trunc 54 | trimSuffix "-") }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kubemoot-dashboard.workshop.labels" -}}
helm.sh/chart: {{ include "kubemoot-dashboard.chart" . }}
{{ include "kubemoot-dashboard.workshop.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: kubemoot
app.kubernetes.io/component: dashboard-workshop
{{- end }}

{{/*
The NATS helpers below are a copy of the operator chart's natsConn* helpers: a subchart cannot call
its parent's templates. Keep the mount paths, volume names and variables in step with them; a Go test
in the operator (internal/natspod/chart_contract_test.go) fails when either drifts from the operator
package that builds the same pods.
*/}}

{{/*
The NATS connection options (nats.tls, nats.auth), checked: a credential Secret needs exactly one
kind of credential (a creds key, a user key with a password key, or a token key).
*/}}
{{- define "kubemoot-dashboard.natsCheck" -}}
{{- $a := (.Values.nats.auth | default dict) -}}
{{- $kinds := 0 -}}
{{- if $a.credsKey }}{{ $kinds = add1 $kinds }}{{ end -}}
{{- if or $a.userKey $a.passwordKey }}{{ $kinds = add1 $kinds }}{{ end -}}
{{- if $a.tokenKey }}{{ $kinds = add1 $kinds }}{{ end -}}
{{- if and $a.secret (ne (int $kinds) 1) -}}
{{- fail "nats.auth.secret needs exactly one of nats.auth.credsKey, nats.auth.userKey with nats.auth.passwordKey, or nats.auth.tokenKey" -}}
{{- end -}}
{{- if and (not $a.secret) (gt (int $kinds) 0) -}}
{{- fail "nats.auth keys need nats.auth.secret" -}}
{{- end -}}
{{- if or (and $a.userKey (not $a.passwordKey)) (and $a.passwordKey (not $a.userKey)) -}}
{{- fail "nats.auth.userKey and nats.auth.passwordKey go together" -}}
{{- end -}}
{{- end }}

{{/* Environment the dashboard reads for NATS; the names every Kubemoot component reads. */}}
{{- define "kubemoot-dashboard.natsEnv" -}}
{{- include "kubemoot-dashboard.natsCheck" . -}}
{{- $tls := (.Values.nats.tls | default dict) -}}
{{- $a := (.Values.nats.auth | default dict) -}}
{{- with .Values.nats.url }}
- name: NATS_URL
  value: {{ . | quote }}
{{- end }}
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
{{- if $a.secret }}
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
{{- end }}

{{- define "kubemoot-dashboard.natsMounts" -}}
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

{{- define "kubemoot-dashboard.natsVolumes" -}}
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
