---
name: python-docstrings
description: Write Python docstrings in Google style. Use whenever the user asks to write, add, or fix docstrings or documentation comments for Python functions, classes, or modules.
license: Apache-2.0
metadata:
  author: ACME Platform Team <platform@acme.example>
  version: 1.0.0
---

# Python docstrings

Document Python code with Google-style docstrings.

1. Start with a one-line summary in the imperative mood, ending with a period (`Parse a duration string.`).
2. Leave a blank line, then add a longer description only when the summary is not enough.
3. Document every parameter in an `Args:` section, one entry per parameter, as `name: description.` Types stay in the signature as type hints, not in the docstring.
4. Describe the return value in a `Returns:` section. Generators use `Yields:` instead.
5. List every exception the function raises on purpose in a `Raises:` section.
6. Classes get a class docstring describing the purpose. Public methods get their own docstrings. Document constructor parameters in the class docstring under `Args:`.
7. Do not restate what the code obviously does.
