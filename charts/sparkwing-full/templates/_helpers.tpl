{{/*
Expand the name of the chart.
*/}}
{{- define "sparkwing-full.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create the untruncated fully qualified app-name base. Component helpers
truncate this base before adding their suffix so long names stay distinct.
*/}}
{{- define "sparkwing-full.fullnameBase" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create a default fully qualified app name. Truncated at 63 chars to
satisfy DNS label constraints; suffix-trimmed so we never end on a
hyphen (DNS labels aren't allowed to).
*/}}
{{- define "sparkwing-full.fullname" -}}
{{- include "sparkwing-full.fullnameBase" . | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Reserve room for a component suffix before truncating the shared base.
Truncating a complete <base>-<component> from the right would erase the
component on long release names and make sibling resources collide.
*/}}
{{- define "sparkwing-full.componentFullname" -}}
{{- $suffix := printf "-%s" .component -}}
{{- $baseLimit := sub 63 (len $suffix) | int -}}
{{- $base := include "sparkwing-full.fullnameBase" .root | trunc $baseLimit | trimSuffix "-" -}}
{{- printf "%s%s" $base $suffix -}}
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
{{- include "sparkwing-full.componentFullname" (dict "root" . "component" "controller") }}
{{- end }}

{{- define "sparkwing-full.web.fullname" -}}
{{- include "sparkwing-full.componentFullname" (dict "root" . "component" "web") }}
{{- end }}

{{- define "sparkwing-full.controller.storageClassesFullname" -}}
{{- include "sparkwing-full.componentFullname" (dict "root" . "component" "controller-storageclasses") }}
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
ServiceAccount name for the warm-pool warmer pods. Release-scoped so two
releases in one namespace do not fight over the same account; the
controller is told the name with --warmer-service-account.
*/}}
{{- define "sparkwing-full.warmerServiceAccountName" -}}
{{- include "sparkwing-full.componentFullname" (dict "root" . "component" "cache-warmer") }}
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
{{- printf "http://%s.%s.svc.cluster.local" (include "sparkwing-full.bundle.logs.fullname" .) .Release.Namespace -}}
{{- end -}}
{{- end }}

{{/*
Resolved web.cache.url: explicit override wins; otherwise the
in-cluster cache Service from the runner-bundle sub-chart (only if
that sub-chart is enabled and its cache component is enabled).
Empty string when neither applies, in which case the web pod is
started without --cache and its services panel simply does not list
the cache -- the same panel a dashboard showed before the flag
existed. The cache is probe-only: nothing else the dashboard does
reads it, so an operator who runs their own git mirror can leave
this empty without losing anything else.
*/}}
{{- define "sparkwing-full.web.cacheURL" -}}
{{- if .Values.web.cache.url -}}
{{- .Values.web.cache.url -}}
{{- else if and (index .Values "sparkwing-runner-bundle" "enabled") (index .Values "sparkwing-runner-bundle" "cache" "enabled") -}}
{{- printf "http://%s.%s.svc.cluster.local" (include "sparkwing-full.bundle.cache.fullname" .) .Release.Namespace -}}
{{- end -}}
{{- end }}

{{/*
The runner-bundle sub-chart's untruncated release-qualified base,
reproducing its own helper because a parent chart cannot call into a
sub-chart's helpers.
*/}}
{{- define "sparkwing-full.bundle.fullnameBase" -}}
{{- $bundle := index .Values "sparkwing-runner-bundle" -}}
{{- if (index $bundle "fullnameOverride") -}}
{{- index $bundle "fullnameOverride" | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "sparkwing-runner-bundle" (index $bundle "nameOverride") -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end }}

{{- define "sparkwing-full.bundle.fullname" -}}
{{- include "sparkwing-full.bundle.fullnameBase" . | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/* Keep parent-computed URLs byte-for-byte aligned with sub-chart resources. */}}
{{- define "sparkwing-full.bundle.componentFullname" -}}
{{- $suffix := printf "-%s" .component -}}
{{- $baseLimit := sub 63 (len $suffix) | int -}}
{{- $base := include "sparkwing-full.bundle.fullnameBase" .root | trunc $baseLimit | trimSuffix "-" -}}
{{- printf "%s%s" $base $suffix -}}
{{- end }}

{{- define "sparkwing-full.bundle.logs.fullname" -}}
{{- include "sparkwing-full.bundle.componentFullname" (dict "root" . "component" "logs") -}}
{{- end }}

{{- define "sparkwing-full.bundle.cache.fullname" -}}
{{- include "sparkwing-full.bundle.componentFullname" (dict "root" . "component" "cache") -}}
{{- end }}

{{/*
Resolved web.tokenSecret: the explicit web Secret wins; otherwise the
runner-bundle's controller.tokenSecret, so the dashboard carries a
bearer whenever the bundled logs service validates one. Without this
default an operator who sets only the bundle's Secret gets a web pod
with no SPARKWING_AGENT_TOKEN and 401s on every log pane. Empty when
neither is set (the fully unauthenticated bootstrap install).
*/}}
{{- define "sparkwing-full.web.tokenSecretName" -}}
{{- if .Values.web.tokenSecret.name -}}
{{- .Values.web.tokenSecret.name -}}
{{- else if index .Values "sparkwing-runner-bundle" "enabled" -}}
{{- index .Values "sparkwing-runner-bundle" "controller" "tokenSecret" "name" -}}
{{- end -}}
{{- end }}

{{- define "sparkwing-full.web.tokenSecretKey" -}}
{{- if .Values.web.tokenSecret.name -}}
{{- .Values.web.tokenSecret.key -}}
{{- else if index .Values "sparkwing-runner-bundle" "enabled" -}}
{{- index .Values "sparkwing-runner-bundle" "controller" "tokenSecret" "key" -}}
{{- end -}}
{{- end }}
