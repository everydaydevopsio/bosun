{{- define "bosun.fullname" -}}
{{- printf "%s-bosun" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
