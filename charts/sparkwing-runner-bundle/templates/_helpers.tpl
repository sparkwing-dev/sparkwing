{{/*
Expand the name of the chart.
*/}}
{{- define "sparkwing-runner-bundle.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create the untruncated fully qualified app-name base. Component helpers
truncate this base before adding their suffix so long names stay distinct.
*/}}
{{- define "sparkwing-runner-bundle.fullnameBase" -}}
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
{{- define "sparkwing-runner-bundle.fullname" -}}
{{- include "sparkwing-runner-bundle.fullnameBase" . | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Reserve the suffix so long component names remain valid and distinct. */}}
{{- define "sparkwing-runner-bundle.componentFullname" -}}
{{- $suffix := printf "-%s" .component -}}
{{- $baseLimit := sub 63 (len $suffix) | int -}}
{{- $base := include "sparkwing-runner-bundle.fullnameBase" .root | trunc $baseLimit | trimSuffix "-" -}}
{{- printf "%s%s" $base $suffix -}}
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
Cache and logs ServiceAccount names. The cache and logs servers never
call the Kubernetes API, so they get their own unbound ServiceAccounts
instead of sharing the runner's Role.
*/}}
{{- define "sparkwing-runner-bundle.cacheServiceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- include "sparkwing-runner-bundle.cache.fullname" . }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "sparkwing-runner-bundle.logsServiceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- include "sparkwing-runner-bundle.logs.fullname" . }}
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
{{- include "sparkwing-runner-bundle.componentFullname" (dict "root" . "component" "runner") }}
{{- end }}

{{- define "sparkwing-runner-bundle.cache.fullname" -}}
{{- include "sparkwing-runner-bundle.componentFullname" (dict "root" . "component" "cache") }}
{{- end }}

{{- define "sparkwing-runner-bundle.logs.fullname" -}}
{{- include "sparkwing-runner-bundle.componentFullname" (dict "root" . "component" "logs") }}
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
Resolve the controller URL. The parent chart cannot pass a value that
contains its computed release name, so its bundled marker asks this
sub-chart to reproduce the parent Service's default name. An explicit
URL always wins, including for parent installs with naming overrides.
*/}}
{{- define "sparkwing-runner-bundle.controllerURL" -}}
{{- if .Values.controller.url -}}
{{- .Values.controller.url -}}
{{- else if .Values.controller.bundled -}}
{{- $parentName := "sparkwing-full" -}}
{{- $parentFullname := printf "%s-%s" .Release.Name $parentName -}}
{{- if contains $parentName .Release.Name -}}
{{- $parentFullname = .Release.Name -}}
{{- end -}}
{{- $controllerSuffix := "-controller" -}}
{{- $parentBaseLimit := sub 63 (len $controllerSuffix) | int -}}
{{- $parentFullname = $parentFullname | trunc $parentBaseLimit | trimSuffix "-" -}}
{{- $controllerName := printf "%s%s" $parentFullname $controllerSuffix -}}
{{- printf "http://%s.%s.svc.cluster.local" $controllerName .Release.Namespace -}}
{{- end -}}
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

{{/*
Dependency-proxy env for the runner container: point go / npm / pip at
the cache pod's pull-through registry proxy so a build's dependency
fetch is served in-cluster instead of egressing on every run.

Constraints encoded in the values:
  - GOPROXY separates proxy from upstream with "|" so ANY proxy error
    falls through to proxy.golang.org; "," only falls through on 404 and
    410, which would fail every build while the cache pod rolls.
  - "direct" stays last so GOPRIVATE modules keep resolving through the
    ~/.netrc that runner-entrypoint.sh seeds from $GITHUB_TOKEN.
  - pip ignores a plain-HTTP index unless its host is also named in
    PIP_TRUSTED_HOST, and then fails outright rather than falling back
    to PyPI.
  - the proxy rewrites the file URLs inside /proxy/pypi/simple/ onto
    /proxy/pythonhosted, so the download half needs no separate env.

A name already present in runner.extraEnv is skipped rather than
emitted twice: K8s rejects duplicate env names outright, so "user value
wins" has to be decided here, not by list order.
*/}}
{{- define "sparkwing-runner-bundle.runnerDependencyProxyEnv" -}}
{{- if and .Values.cache.enabled .Values.cache.dependencyProxy.enabled -}}
{{- $host := printf "%s.%s.svc.cluster.local" (include "sparkwing-runner-bundle.cache.fullname" .) .Release.Namespace -}}
{{- $base := printf "http://%s" $host -}}
{{- $taken := dict -}}
{{- range .Values.runner.extraEnv }}
{{- $_ := set $taken (.name | toString) true }}
{{- end }}
{{- $defaults := list
  (dict "name" "GOPROXY" "value" (printf "%s/proxy/golang|https://proxy.golang.org,direct" $base))
  (dict "name" "npm_config_registry" "value" (printf "%s/proxy/npm" $base))
  (dict "name" "PIP_INDEX_URL" "value" (printf "%s/proxy/pypi/simple/" $base))
  (dict "name" "PIP_TRUSTED_HOST" "value" $host) -}}
{{- $out := list -}}
{{- range $defaults }}
{{- if not (hasKey $taken .name) }}
{{- $out = append $out . }}
{{- end }}
{{- end }}
{{- if $out }}
{{- toYaml $out }}
{{- end }}
{{- end }}
{{- end }}
