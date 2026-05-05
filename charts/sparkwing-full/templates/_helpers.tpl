{{/*
Expand the name of the chart.
*/}}
{{- define "sparkwing-full.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name. Truncated at 63 chars to
satisfy DNS label constraints; suffix-trimmed so we never end on a
hyphen (DNS labels aren't allowed to).
*/}}
{{- define "sparkwing-full.fullname" -}}
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
Chart label, e.g. sparkwing-full-0.1.0.
*/}}
{{- define "sparkwing-full.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels emitted on every resource.
*/}}
{{- define "sparkwing-full.labels" -}}
helm.sh/chart: {{ include "sparkwing-full.chart" . }}
{{ include "sparkwing-full.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels (must be stable across upgrades).
*/}}
{{- define "sparkwing-full.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sparkwing-full.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Per-component selector labels. Each Deployment + Service uses
component=<name> alongside the shared release labels so a single
release can host all workloads without selector collisions.
*/}}
{{- define "sparkwing-full.componentSelectorLabels" -}}
{{ include "sparkwing-full.selectorLabels" .root }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{- define "sparkwing-full.componentLabels" -}}
{{ include "sparkwing-full.labels" .root }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/*
Per-component fully qualified resource names. Component suffix
keeps controller + web distinct under one release.
*/}}
{{- define "sparkwing-full.controller.fullname" -}}
{{- printf "%s-controller" (include "sparkwing-full.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "sparkwing-full.web.fullname" -}}
{{- printf "%s-web" (include "sparkwing-full.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
ServiceAccount name for the controller. If serviceAccount.create is
true and no explicit name is provided, fall back to a per-component
name so it doesn't collide with the sub-chart's SA.
*/}}
{{- define "sparkwing-full.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "sparkwing-full.controller.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Resolved image tag for a component: per-component image.tag wins,
otherwise fall back to .Chart.AppVersion.
Usage: {{ include "sparkwing-full.image" (dict "img" .Values.controller.image "root" .) }}
*/}}
{{- define "sparkwing-full.image" -}}
{{- $tag := default .root.Chart.AppVersion .img.tag -}}
{{- printf "%s:%s" .img.repository $tag -}}
{{- end }}

{{/*
In-cluster URL of the controller Service. Used as a default for
web.controller.url and for the runner-bundle sub-chart's
controller.url override (so the bundled runner claims work from
the bundled controller).
*/}}
{{- define "sparkwing-full.controller.serviceURL" -}}
{{- printf "http://%s.%s.svc.cluster.local" (include "sparkwing-full.controller.fullname" .) .Release.Namespace -}}
{{- end }}

{{/*
Resolved web.controller.url: explicit override wins; otherwise the
in-cluster controller Service.
*/}}
{{- define "sparkwing-full.web.controllerURL" -}}
{{- if .Values.web.controller.url -}}
{{- .Values.web.controller.url -}}
{{- else -}}
{{- include "sparkwing-full.controller.serviceURL" . -}}
{{- end -}}
{{- end }}

{{/*
Resolved web.logs.url: explicit override wins; otherwise the
in-cluster logs Service from the runner-bundle sub-chart (only if
that sub-chart is enabled and its logs component is enabled).
Empty string when neither applies, in which case the web pod runs
in local-log mode (which won't find any logs in cluster mode --
operators should set web.logs.url explicitly if they disable the
sub-chart logs).
*/}}
{{- define "sparkwing-full.web.logsURL" -}}
{{- if .Values.web.logs.url -}}
{{- .Values.web.logs.url -}}
{{- else if and (index .Values "sparkwing-runner-bundle" "enabled") (index .Values "sparkwing-runner-bundle" "logs" "enabled") -}}
{{- /* Sub-chart's logs Service name follows its own fullname helper:
       <release-name>-sparkwing-runner-bundle-logs. We can't call into
       the sub-chart's helpers from here, so reproduce the pattern. */ -}}
{{- $bundleFull := printf "%s-sparkwing-runner-bundle" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- if contains "sparkwing-runner-bundle" .Release.Name -}}
{{- $bundleFull = .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- printf "http://%s-logs.%s.svc.cluster.local" $bundleFull .Release.Namespace -}}
{{- end -}}
{{- end }}
