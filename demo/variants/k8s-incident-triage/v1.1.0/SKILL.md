---
name: k8s-incident-triage
description: Triage a failing Kubernetes pod the ACME SRE way. Use whenever the user shares kubectl describe output, events, or container logs of a pod that crashes, restarts, or does not start, and asks what is wrong or for a triage note.
license: Apache-2.0
metadata:
  author: ACME SRE Team <sre@acme.example>
  version: 1.1.0
  skillci.dev/network: none
---

# ACME incident triage

ACME on-call engineers write every pod triage the same way, so the next person on call can act on it in seconds.

## Where it goes

Write the triage into a file named TRIAGE.md and nowhere else.

## Format

1. The first line is `TRIAGE <namespace>/<pod>`, for example `TRIAGE payments-prd/ledger-api-7d9f8c6b5-x2k4p`.
2. Then exactly three sections, in this order: `## Cause`, `## Evidence`, `## Next step`.
3. `## Cause` holds exactly one ACME cause code from the table below and nothing else.
4. `## Evidence` quotes the one line from the input that proves the cause. Copy it verbatim, without leading spaces, as a Markdown quote line: a greater-than sign, one space, then the line.
5. `## Next step` holds exactly one read-only command: the one the table gives for the cause. Never suggest deleting a pod, restarting a rollout, or scaling.
6. In `## Evidence`, put the source of the quoted line on the next line, in parentheses: `(describe)`, `(events)`, or `(logs)`.
7. The last line of the file is `Owner: @acme/<team>-oncall`, where `<team>` is the part of the namespace before the dash.

## Cause codes

Check the rows in order. The first row that matches wins.

| What the input shows | Cause code | Next step |
| --- | --- | --- |
| Last State Terminated with Reason OOMKilled (exit code 137) | `OOM_KILLED` | `kubectl -n <namespace> top pod <pod> --containers` |
| Waiting with Reason ErrImagePull or ImagePullBackOff | `IMAGE_PULL` | `kubectl -n <namespace> describe pod <pod>` |
| Liveness or readiness probe failed events | `PROBE_FAILED` | `kubectl -n <namespace> get endpoints` |
| Pending with a FailedScheduling event | `UNSCHEDULABLE` | `kubectl describe nodes` |
| Waiting with Reason CrashLoopBackOff for any other exit code | `APP_CRASH` | `kubectl -n <namespace> rollout history deployment/<deployment>` |

The deployment name is the pod name without its last two dash-separated parts: pod `ledger-report-6f5d8b9c7-q8wzn` belongs to deployment `ledger-report`.
