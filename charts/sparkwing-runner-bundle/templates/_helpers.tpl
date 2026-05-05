{{/*
Expand the name of the chart.
*/}}
{{- define "sparkwing-runner-bundle.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name. Truncated at 63 chars to
satisfy DNS label constraints; suffix-trimmed so we never end on a
hyphen (DNS labels aren't allowed to).
*/}}
{{- define "sparkwing-runner-bundle.fullname" -}}
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
Chart label, e.g. sparkwing-runner-bundle-0.1.0. Used in the
helm.sh/chart label across every workload.
*/}}
{{- define "sparkwing-runner-bundle.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels emitted on every resource. Includes the
recommended app.kubernetes.io/* set plus the chart label so
`kubectl get -l app.kubernetes.io/instance=<release>` finds
everything this chart owns.
*/}}
{{- define "sparkwing-runner-bundle.labels" -}}
helm.sh/chart: {{ include "sparkwing-runner-bundle.chart" . }}
{{ include "sparkwing-runner-bundle.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels (must be stable across upgrades; do NOT add
app.kubernetes.io/version here -- changing the version on upgrade
would change the selector and break the in-flight rollout).
*/}}
{{- define "sparkwing-runner-bundle.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sparkwing-runner-bundle.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Per-component selector labels. Each Deployment + Service uses
component=<name> alongside the shared release labels so a single
release can host all three workloads without selector collisions.
*/}}
{{- define "sparkwing-runner-bundle.componentSelectorLabels" -}}
{{ include "sparkwing-runner-bundle.selectorLabels" .root }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{- define "sparkwing-runner-bundle.componentLabels" -}}
{{ include "sparkwing-runner-bundle.labels" .root }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/*
ServiceAccount name to use. If serviceAccount.create is true and no
explicit name is provided, fall back to the fullname.
*/}}
{{- define "sparkwing-runner-bundle.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "sparkwing-runner-bundle.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Per-component fully qualified resource names (Deployments,
Services, PVCs). Component suffix keeps the three workloads
distinct under one release.
*/}}
{{- define "sparkwing-runner-bundle.runner.fullname" -}}
{{- printf "%s-runner" (include "sparkwing-runner-bundle.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "sparkwing-runner-bundle.cache.fullname" -}}
{{- printf "%s-cache" (include "sparkwing-runner-bundle.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "sparkwing-runner-bundle.logs.fullname" -}}
{{- printf "%s-logs" (include "sparkwing-runner-bundle.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Resolved image tag for a component: per-component image.tag wins,
otherwise fall back to .Chart.AppVersion. Lets users pin per-binary
images independently while keeping a single appVersion default.
Usage: {{ include "sparkwing-runner-bundle.image" (dict "img" .Values.runner.image "root" .) }}
*/}}
{{- define "sparkwing-runner-bundle.image" -}}
{{- $tag := default .root.Chart.AppVersion .img.tag -}}
{{- printf "%s:%s" .img.repository $tag -}}
{{- end }}

{{/*
Render runner --label flags. Each entry in .Values.runner.labels
becomes a separate --label=<value> arg. Done in a helper so the
deployment template stays readable.
*/}}
{{- define "sparkwing-runner-bundle.runnerLabelArgs" -}}
{{- range .Values.runner.labels }}
- {{ printf "--label=%s" . | quote }}
{{- end }}
{{- end }}
