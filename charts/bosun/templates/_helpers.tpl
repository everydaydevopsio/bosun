{{- define "bosun.fullname" -}}
{{- printf "%s-bosun" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Selector labels. Release-scoped: the Service selector and the Deployment
selector must not match another release's pods, or two installs in one
namespace route webhook traffic -- and their secrets -- into each other.
*/}}
{{- define "bosun.selectorLabels" -}}
app.kubernetes.io/name: bosun
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
