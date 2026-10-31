{{/*
Chart name, truncated to the 63 character DNS label limit.
*/}}
{{- define "skill-ci-controller.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Resource name. The chart is meant to be installed once per namespace and the
controller hardcodes the runner ServiceAccount name, so the default is the chart
name (not release-chart) which matches the kustomize install in config/.
*/}}
{{- define "skill-ci-controller.fullname" -}}
{{- default (include "skill-ci-controller.name" .) .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Chart label value (name-version).
*/}}
{{- define "skill-ci-controller.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Selector labels. Only the name label, so the selector equals the one of the
kustomize-managed Deployment (selectors are immutable) and the Service keeps
matching the existing ServiceMonitor.
*/}}
{{- define "skill-ci-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "skill-ci-controller.name" . }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "skill-ci-controller.labels" -}}
helm.sh/chart: {{ include "skill-ci-controller.chart" . }}
{{ include "skill-ci-controller.selectorLabels" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: skill-ci
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{/*
Labels for the runner ServiceAccount and Role (not part of the controller selector).
*/}}
{{- define "skill-ci-controller.runnerLabels" -}}
helm.sh/chart: {{ include "skill-ci-controller.chart" . }}
app.kubernetes.io/name: skill-eval-runner
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: skill-ci
app.kubernetes.io/component: runner
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{/*
Controller ServiceAccount name.
*/}}
{{- define "skill-ci-controller.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "skill-ci-controller.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Namespace the controller watches and creates SkillEvals and Jobs in
(--namespace). Defaults to the release namespace.
*/}}
{{- define "skill-ci-controller.watchNamespace" -}}
{{- default .Release.Namespace .Values.controller.namespace }}
{{- end }}

{{/*
Render an image reference from {repository, tag, digest}. The tag falls back to
the chart appVersion for the controller, the runner and the sandbox image.
Usage: include "skill-ci-controller.image" (dict "image" .Values.image "defaultTag" .Chart.AppVersion)
*/}}
{{- define "skill-ci-controller.image" -}}
{{- $tag := default .defaultTag .image.tag | toString -}}
{{- if .image.digest -}}
{{- printf "%s@%s" .image.repository .image.digest -}}
{{- else if $tag -}}
{{- printf "%s:%s" .image.repository $tag -}}
{{- else -}}
{{- .image.repository -}}
{{- end -}}
{{- end }}

{{/*
OTEL_RESOURCE_ATTRIBUTES: the pod and namespace (from the downward API env), then otlp.resourceAttributes.
*/}}
{{- define "skill-ci-controller.otlpResourceAttributes" -}}
{{- $attrs := list "k8s.pod.name=$(POD_NAME)" "k8s.namespace.name=$(POD_NAMESPACE)" -}}
{{- range $k, $v := .Values.otlp.resourceAttributes -}}
{{- $attrs = append $attrs (printf "%s=%s" $k $v) -}}
{{- end -}}
{{- join "," $attrs -}}
{{- end }}

{{/*
Create-once object in the watch namespace: a SkillCIConfig or SandboxProfile the chart seeds from
values and then leaves to kubectl or GitOps. Rendered (with helm.sh/resource-policy: keep, so a later
upgrade that stops rendering it never deletes it) only when lookup finds no such object. helm template
and helm lint have no cluster, lookup returns nothing, and the object always renders.
An object this release created before the annotation existed (chart 0.9.0 SandboxProfiles) is rendered
once more from its live spec with the annotation added, so the upgrade adopts it instead of deleting it.
Usage: include "skill-ci-controller.createOnce" (dict "root" $ "kind" "SandboxProfile" "name" "default" "spec" $spec)
*/}}
{{- define "skill-ci-controller.createOnce" -}}
{{- $root := .root -}}
{{- $ns := include "skill-ci-controller.watchNamespace" $root -}}
{{- $spec := .spec | default dict -}}
{{- $render := true -}}
{{- $live := lookup "skillci.dev/v1alpha1" .kind $ns .name -}}
{{- if $live -}}
{{- $ann := $live.metadata.annotations | default dict -}}
{{- if and (eq (get $ann "meta.helm.sh/release-name") $root.Release.Name) (eq (get $ann "meta.helm.sh/release-namespace") $root.Release.Namespace) (ne (get $ann "helm.sh/resource-policy") "keep") -}}
{{- $spec = $live.spec | default dict -}}
{{- else -}}
{{- $render = false -}}
{{- end -}}
{{- end -}}
{{- if $render }}
---
apiVersion: skillci.dev/v1alpha1
kind: {{ .kind }}
metadata:
  name: {{ .name }}
  namespace: {{ $ns }}
  labels:
    {{- include "skill-ci-controller.labels" $root | nindent 4 }}
  annotations:
    # Created once from the chart values; edit the object (kubectl or GitOps), upgrades never overwrite it.
    helm.sh/resource-policy: keep
spec:
  {{- toYaml $spec | nindent 2 }}
{{- end }}
{{- end }}
