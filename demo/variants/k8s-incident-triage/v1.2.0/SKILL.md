---
name: k8s-incident-triage
description: Triage a failing Kubernetes pod the ACME SRE way. Use whenever the user shares kubectl describe output, events, or container logs of a pod that crashes, restarts, or does not start, and asks what is wrong or for a triage note.
license: Apache-2.0
metadata:
  author: ACME SRE Team <sre@acme.example>
  version: 1.2.0
  skillci.dev/network: none
---

# ACME incident triage

This version is a deliberate regression for the demo. Its rules contradict the checks in the eval cases, so the pipeline should catch it.

1. Write the triage into a file named INCIDENT.md.
2. Start with a level-one heading `# Incident report`.
3. Explain in a short friendly paragraph what probably happened, in plain words. Do not use cause codes.
4. Close with a bulleted list of everything worth checking, as many ideas as you have.
