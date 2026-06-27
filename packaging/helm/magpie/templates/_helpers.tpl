{{/*
Chart naming + labels — standard Helm boilerplate, extracted so every
manifest resource gets the same names/labels without duplication.
*/}}

{{- define "magpie.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "magpie.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "magpie.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels applied to every resource. Kept short — Kubernetes truncates
label values at 63 chars and having too many labels per selector slows
scheduler updates on large clusters.
*/}}
{{- define "magpie.labels" -}}
helm.sh/chart: {{ include "magpie.chart" . }}
app.kubernetes.io/name: {{ include "magpie.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: magpie
{{- end -}}

{{/*
Selector labels — the subset of common labels that's allowed to be immutable
on a Deployment's spec.selector. Keep this small: changing anything here
forces a Deployment replacement.
*/}}
{{- define "magpie.selectorLabels" -}}
app.kubernetes.io/name: {{ include "magpie.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Per-component selector labels — distinguishes magpied pods from ui pods
when both live in the same release.
*/}}
{{- define "magpie.magpiedSelectorLabels" -}}
{{ include "magpie.selectorLabels" . }}
app.kubernetes.io/component: magpied
{{- end -}}

{{- define "magpie.uiSelectorLabels" -}}
{{ include "magpie.selectorLabels" . }}
app.kubernetes.io/component: ui
{{- end -}}

{{/*
ServiceAccount name — honors .Values.serviceAccount.name when set, else
defaults to the chart fullname.
*/}}
{{- define "magpie.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "magpie.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Image tag resolver — falls back to Chart.appVersion when the value-supplied
tag is empty. Lets `helm upgrade --set magpied.image.tag=0.2.0` work without
requiring the user to also bump chart version, while keeping defaults
aligned with whichever appVersion shipped with the chart.
*/}}
{{- define "magpie.magpiedImage" -}}
{{- $tag := .Values.magpied.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.magpied.image.repository $tag -}}
{{- end -}}

{{- define "magpie.uiImage" -}}
{{- $tag := .Values.ui.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.ui.image.repository $tag -}}
{{- end -}}
