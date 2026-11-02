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
Whether raw-output toolers store each tool call's result as its own workspace object. A boolean,
false when unset; any other value fails the render.
*/}}
{{- define "kubemoot-operator.perCallArtifacts" -}}
{{- $value := (.Values.artifacts | default dict).perCallArtifacts -}}
{{- if kindIs "invalid" $value -}}
false
{{- else if kindIs "bool" $value -}}
{{- $value -}}
{{- else -}}
{{- fail (printf "artifacts.perCallArtifacts must be true or false, got %v" $value) -}}
{{- end -}}
{{- end }}

{{/*
The artifact query engine, none, sqlite or duckdb; any other value fails the render.
*/}}
{{- define "kubemoot-operator.artifactQueryEngine" -}}
{{- $engine := (.Values.artifacts | default dict).queryEngine | default "sqlite" -}}
{{- if not (has $engine (list "none" "sqlite" "duckdb")) -}}
{{- fail (printf "artifacts.queryEngine must be none, sqlite or duckdb, got %q" $engine) -}}
{{- end -}}
{{- $engine -}}
{{- end }}
