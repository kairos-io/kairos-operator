{{/*
Expand the name of the chart.
*/}}
{{- define "kairos-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "kairos-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "kairos-operator.namespace" -}}
{{- .Release.Namespace }}
{{- end }}

{{- define "kairos-operator.serviceAccountName" -}}
{{- .Values.serviceAccount.name }}
{{- end }}

{{- define "kairos-operator.operatorImage" -}}
{{- $tag := .Values.image.operator.tag | default (printf "%s" .Chart.AppVersion) }}
{{- printf "%s:%s" .Values.image.operator.repository $tag }}
{{- end }}

{{- define "kairos-operator.nodeLabellerImage" -}}
{{- $tag := .Values.image.nodeLabeler.tag | default (printf "%s" .Chart.AppVersion) }}
{{- printf "%s:%s" .Values.image.nodeLabeler.repository $tag }}
{{- end }}

{{- define "kairos-operator.labels" -}}
app.kubernetes.io/name: {{ include "kairos-operator.name" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kairos-operator.selectorLabels" -}}
app.kubernetes.io/name: kairos-operator
app.kubernetes.io/component: operator
{{- end }}
