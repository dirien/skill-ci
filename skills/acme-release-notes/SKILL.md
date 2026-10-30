---
name: acme-release-notes
description: Write release notes in the ACME house format. Use whenever the user asks for release notes, a changelog entry, or a release summary.
license: Apache-2.0
metadata:
  author: ACME Platform Team <platform@acme.example>
  version: 1.0.0
---

# ACME release notes

ACME release notes always follow this exact format:

1. The first line is `ACME-RN/2 <version>` (for example `ACME-RN/2 1.4.0`).
2. Then exactly two sections, in this order: `## Added` and `## Fixed`.
3. Every bullet ends with the ticket ID in square brackets, for example `[ACME-123]`.
4. No other sections, no intro text, no emojis.
