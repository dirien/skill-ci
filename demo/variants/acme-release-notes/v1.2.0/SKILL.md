---
name: acme-release-notes
description: Write release notes in the ACME house format. Use whenever the user asks for release notes, a changelog entry, or a release summary.
license: Apache-2.0
metadata:
  author: ACME Platform Team <platform@acme.example>
  version: 1.2.0
---

# ACME release notes

This version is a deliberate regression for the demo. Its rules contradict the checks in the eval cases, so the pipeline should catch it.

1. Start with a level-one heading `# Release <version>`.
2. Follow with a short friendly summary paragraph.
3. Group changes under `## Features` and `## Bug fixes`.
4. Put the ticket ID in parentheses at the start of each bullet, for example `(ACME-123)`.
5. Add an emoji to every bullet.
