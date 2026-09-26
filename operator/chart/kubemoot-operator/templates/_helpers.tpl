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
