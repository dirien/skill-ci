---
name: k8s-resource-naming
description: Apply the ACME platform team's Kubernetes naming and labelling conventions. Use whenever the user asks to write or review Kubernetes manifests such as Deployments, Services, ConfigMaps, Secrets, Ingresses, CronJobs, or PersistentVolumeClaims for ACME.
license: Apache-2.0
metadata:
  author: ACME Platform Team <platform@acme.example>
  version: "1.0.0"
---

# ACME Kubernetes naming

Every ACME manifest follows these conventions. Reviewers reject manifests that break them.

## Environment codes

Use only `dev`, `stg`, and `prd`. Never write `staging`, `prod`, or `production`.

## Namespaces

The namespace is `<team>-<env>`, for example `payments-prd`.

## Resource names

- Names are lowercase kebab-case: `<app>-<component>`, for example `ledger-api`.
- Never put the environment in a resource name. The namespace carries it.
- Deployments, StatefulSets, and Services use the plain name. A Service has the same name as the workload it fronts.
- Other kinds get a suffix:

| Kind | Suffix | Example |
| --- | --- | --- |
| ConfigMap | `-cfg` | `ledger-api-cfg` |
| Secret | `-sec` | `ledger-api-sec` |
| PersistentVolumeClaim | `-pvc` | `ledger-db-pvc` |
| Ingress | `-ing` | `ledger-api-ing` |
| CronJob | `-cron` | `ledger-report-cron` |
| HorizontalPodAutoscaler | `-hpa` | `ledger-api-hpa` |
| NetworkPolicy | `-netpol` | `ledger-api-netpol` |

## Labels

Put these labels on every resource, in `metadata.labels`:

- `app.kubernetes.io/name`: the app name
- `acme.io/team`: the owning team
- `acme.io/app`: the app name
- `acme.io/env`: `dev`, `stg`, or `prd`
- `acme.io/cost-center`: `cc-` followed by the four-digit cost center number, for example `cc-4127`
