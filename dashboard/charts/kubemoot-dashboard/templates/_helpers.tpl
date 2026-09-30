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
