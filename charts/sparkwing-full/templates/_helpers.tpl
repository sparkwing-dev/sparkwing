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
Per-component fully qualified resource names. The component suffix
keeps the controller distinct from the runner-bundle's resources under
one release.
*/}}
{{- define "sparkwing-full.controller.fullname" -}}
{{- include "sparkwing-full.componentFullname" (dict "root" . "component" "controller") }}
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
In-cluster URL of the controller Service. Used for the runner-bundle
sub-chart's controller.url override (so the bundled runner claims work
from the bundled controller).
*/}}
{{- define "sparkwing-full.controller.serviceURL" -}}
{{- printf "http://%s.%s.svc.cluster.local" (include "sparkwing-full.controller.fullname" .) .Release.Namespace -}}
{{- end }}

{{/*
Resolved controller logs URL: explicit override wins; otherwise the
in-cluster logs Service from the runner-bundle sub-chart (only if
that sub-chart is enabled and its logs component is enabled). Empty
string when neither applies, in which case the controller announces
no logs service and dashboard log panes stay empty.
*/}}
{{- define "sparkwing-full.controller.logsURL" -}}
{{- if .Values.controller.logs.url -}}
{{- .Values.controller.logs.url -}}
{{- else if and (index .Values "sparkwing-runner-bundle" "enabled") (index .Values "sparkwing-runner-bundle" "logs" "enabled") -}}
{{- printf "http://%s.%s.svc.cluster.local" (include "sparkwing-full.bundle.logs.fullname" .) .Release.Namespace -}}
{{- end -}}
{{- end }}

{{/*
Resolved controller cache URL: explicit override wins; otherwise the
in-cluster cache Service from the runner-bundle sub-chart (only if
that sub-chart is enabled and its cache component is enabled). Empty
string when neither applies, in which case the controller's gitcache
proxy stays off and answers 404.
*/}}
{{- define "sparkwing-full.controller.cacheURL" -}}
{{- if .Values.controller.cache.url -}}
{{- .Values.controller.cache.url -}}
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
The controller's credentials directory sources: one projected volume
source per configured Secret, each item renamed to the fixed file name
the controller reads. Empty when no credential is configured.
*/}}
{{- define "sparkwing-full.controller.credentialSources" -}}
{{- $c := .Values.controller -}}
{{- $items := list
      (dict "ref" $c.databaseSecret "path" "pg-url")
      (dict "ref" $c.secretsKey "path" "secrets-key")
      (dict "ref" $c.secretsPreviousKey "path" "secrets-key.previous")
      (dict "ref" $c.bootstrapAdminToken "path" "bootstrap-admin-token") -}}
{{- $bundle := index .Values "sparkwing-runner-bundle" -}}
{{- if and (index $bundle "enabled") (index $bundle "cache" "enabled") -}}
{{- $items = append $items (dict "ref" (index $bundle "cache" "tokenSecret" | default dict) "path" "cache-token") -}}
{{- $items = append $items (dict "ref" (index $bundle "cache" "grantKeySecret" | default dict) "path" "cache-grant-key") -}}
{{- end -}}
{{- with include "sparkwing-full.controller.credentialsBundle" . }}
- secret:
    name: {{ . | quote }}
{{- end }}
{{- range $items }}
{{- if and (index .ref "name") (index .ref "key") }}
- secret:
    name: {{ index .ref "name" | quote }}
    items:
      - key: {{ index .ref "key" | quote }}
        path: {{ .path }}
{{- end }}
{{- end }}
{{- end }}

{{/*
The controller.credentialsSecret name, or empty. A release installed before
the value existed has no credentialsSecret map under --reuse-values.
*/}}
{{- define "sparkwing-full.controller.credentialsBundle" -}}
{{- get (.Values.controller.credentialsSecret | default dict) "name" -}}
{{- end -}}

{{/*
"true" when the controller may receive a bootstrap admin token: through
bootstrapAdminToken, or possibly through credentialsSecret, whose keys the
chart cannot see. Empty otherwise.
*/}}
{{- define "sparkwing-full.controller.hasBootstrapAdminToken" -}}
{{- if or .Values.controller.bootstrapAdminToken.name (include "sparkwing-full.controller.credentialsBundle" .) -}}
true
{{- end -}}
{{- end -}}

{{/*
"true" when the controller may receive a current secrets key: through
secretsKey, or possibly through credentialsSecret. Empty otherwise.
*/}}
{{- define "sparkwing-full.controller.hasSecretsKey" -}}
{{- if or .Values.controller.secretsKey.name (include "sparkwing-full.controller.credentialsBundle" .) -}}
true
{{- end -}}
{{- end -}}
