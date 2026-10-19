{{/* The release's name, used for the Deployment, the Service, and as the prefix of the rest. */}}
{{- define "kubemoot-vllm.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* Labels on every object. The Service adds kubemoot.ai/engine: vllm (service.yaml). */}}
{{- define "kubemoot-vllm.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "kubemoot-vllm.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* Stops the render on a value that cannot work. */}}
{{- define "kubemoot-vllm.validate" -}}
{{- $image := .Values.template.container.image | default "" -}}
{{- if not (regexMatch "^[^@\\s]+@sha256:[a-f0-9]{64}$" $image) -}}
{{- fail "template.container.image is required and must be pinned by digest (repo/name:tag@sha256:...); there is no default tag" -}}
{{- end -}}
{{- if not .Values.weights.size -}}
{{- fail "weights.size is required, for example 100Gi" -}}
{{- end -}}
{{- if not (has (int .Values.sleepLevel) (list 1 2)) -}}
{{- fail "sleepLevel must be 1 or 2" -}}
{{- end -}}
{{- end -}}

{{/*
The servers as JSON: one per model, sorted by served name, on consecutive ports from
8000, each with its full `vllm serve` arguments. Fails on a model without a name,
repository, or revision, on two models with one served name, and on a model argument
the chart sets itself.
*/}}
{{- define "kubemoot-vllm.servers" -}}
{{- $owned := list "--model" "--served-model-name" "--revision" "--port" "--host" "--enable-sleep-mode" -}}
{{- if not .Values.models -}}
{{- fail "models: list at least one model ({servedName, repo, revision, args})" -}}
{{- end -}}
{{- $byName := dict -}}
{{- range $i, $m := .Values.models -}}
{{- range $field := list "servedName" "repo" "revision" -}}
{{- if not (get $m $field) -}}
{{- fail (printf "models[%d].%s is required" $i $field) -}}
{{- end -}}
{{- end -}}
{{- if hasKey $byName $m.servedName -}}
{{- fail (printf "models[%d]: the served name %q appears twice" $i $m.servedName) -}}
{{- end -}}
{{- range $arg := ($m.args | default list) -}}
{{- range $flag := $owned -}}
{{- if or (eq (toString $arg) $flag) (hasPrefix (printf "%s=" $flag) (toString $arg)) -}}
{{- fail (printf "models[%d] (%s): the argument %q is set by the chart; remove it from args" $i $m.servedName (toString $arg)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $_ := set $byName $m.servedName $m -}}
{{- end -}}
{{- $out := list -}}
{{- range $i, $name := (keys $byName | sortAlpha) -}}
{{- $m := get $byName $name -}}
{{- $port := add 8000 $i | int -}}
{{- $args := list "serve" $m.repo (printf "--revision=%s" $m.revision) (printf "--served-model-name=%s" $name) "--host=0.0.0.0" (printf "--port=%d" $port) "--enable-sleep-mode" -}}
{{- range $arg := ($m.args | default list) -}}
{{- $args = append $args (toString $arg) -}}
{{- end -}}
{{- $out = append $out (dict "name" $name "repo" $m.repo "revision" $m.revision "port" $port "args" $args) -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{/* The launcher's input: the sleep level and the servers. */}}
{{- define "kubemoot-vllm.launcherConfig" -}}
{{- $servers := include "kubemoot-vllm.servers" . | fromJsonArray -}}
{{- $launch := list -}}
{{- range $s := $servers -}}
{{- $launch = append $launch (dict "name" $s.name "port" $s.port "args" $s.args) -}}
{{- end -}}
{{- dict "sleepLevel" (int .Values.sleepLevel) "servers" $launch | toPrettyJson -}}
{{- end -}}

{{/* A name safe for a Kubernetes object from a served name such as qwen3:14b. */}}
{{- define "kubemoot-vllm.objectName" -}}
{{- regexReplaceAll "[^a-z0-9-]+" (lower .) "-" | trunc 63 | trimSuffix "-" | trimPrefix "-" -}}
{{- end -}}
