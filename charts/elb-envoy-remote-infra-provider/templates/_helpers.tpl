{{- define "elb-envoy-remote-infra-provider.name" -}}
{{- default .Chart.Name .Values.nameOverride -}}
{{- end -}}

{{- define "elb-envoy-remote-infra-provider.fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride -}}
{{- else if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
Name of the webhook service.
*/}}
{{- define "elb-envoy-remote-infra-provider.webhookService" -}}
{{- printf "%s-webhook" (include "elb-envoy-remote-infra-provider.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Generate (and reuse) self-signed webhook serving certificates.

Returns a YAML map with keys: caCert, clientCert, clientKey (all base64-encoded).
On a Helm upgrade, the existing Secret is looked up and its cert reused so the
caBundle in the MutatingWebhookConfiguration stays in sync and the cert is not
regenerated every upgrade. Mirrors the AWS Load Balancer Controller approach.
*/}}
{{- define "elb-envoy-remote-infra-provider.webhookCerts" -}}
{{- $svcName := include "elb-envoy-remote-infra-provider.webhookService" . -}}
{{- $altNames := list (printf "%s.%s" $svcName .Release.Namespace) (printf "%s.%s.svc" $svcName .Release.Namespace) (printf "%s.%s.svc.cluster.local" $svcName .Release.Namespace) -}}
{{- $secretName := printf "%s-tls" $svcName -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace $secretName -}}
{{- if and $existing $existing.data (index $existing.data "tls.crt") (index $existing.data "tls.key") (index $existing.data "ca.crt") -}}
caCert: {{ index $existing.data "ca.crt" }}
clientCert: {{ index $existing.data "tls.crt" }}
clientKey: {{ index $existing.data "tls.key" }}
{{- else -}}
{{- $ca := genCA (printf "%s-ca" $svcName) 3650 -}}
{{- $cert := genSignedCert $svcName nil $altNames 3650 $ca -}}
caCert: {{ $ca.Cert | b64enc }}
clientCert: {{ $cert.Cert | b64enc }}
clientKey: {{ $cert.Key | b64enc }}
{{- end -}}
{{- end -}}
