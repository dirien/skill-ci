"""Read the Dockerfile that SkillEvaluator generates for a task, to replay it on a prebuilt image.

A task image is a generic part (a base image, the tools and Python packages of the verifier,
the workspace directories) plus a task-specific part (``COPY skills/``, ``COPY input/``, the
final ``WORKDIR`` and ``ENV``). The prebuilt sandbox image (``sandbox/Dockerfile``) contains the
generic part, so a trial only has to add the task-specific part when its sandbox starts.

This module decides whether that is possible and says what to do:

* ``COPY`` and ``ADD`` of local files become :class:`Transfer` objects (uploads at start).
* ``ENV``, ``WORKDIR`` and ``USER`` become container settings (:class:`Plan`).
* ``FROM`` and every ``RUN`` must be covered by the sandbox image: the base image is one of
  :data:`SANDBOX_BASE_IMAGES` and each ``RUN`` has a digest in :data:`COVERED_RUNS`. Anything
  else (a skill with ``requirements.txt``, a custom ``evals/environment/Dockerfile``) raises
  :class:`DockerfileError`, so a mismatch is visible instead of silently missing a package.

The module uses only the standard library: it is also run by hand to refresh the digests after
a SkillEvaluator upgrade::

    python -m harbor_agent_sandbox.dockerfile digests path/to/generated/Dockerfile
"""

from __future__ import annotations

import hashlib
import json
import posixpath
import re
import shlex
import sys
from dataclasses import dataclass, field
from pathlib import Path

# FROM lines the sandbox image stands in for. SkillEvaluator 0.6.0 writes `FROM python:3.12-slim`;
# sandbox/Dockerfile is built on a pinned python:3.12 slim image.
SANDBOX_BASE_IMAGES = frozenset({"python:3.12-slim"})

# RUN steps of the Dockerfile SkillEvaluator 0.6.0 generates for a skill without extra
# requirements (see tests/fixtures/skillevaluator-0.6.0.Dockerfile), by digest of the normalized
# command (:func:`run_digest`). Each is done by sandbox/Dockerfile or is a reset of directories
# that are empty in a fresh container. Another RUN, for example an apt or pip install with more
# packages, is not in the image. Refresh with the `digests` command in the module docstring.
COVERED_RUNS: dict[str, str] = {
    "5fb191b393befe02fa9e1dca52d8f9bdc29ae9069ca4f4646f24537806310e1f": (
        "apt-get install bash curl git jq ripgrep"
    ),
    "ede8054a843fb58f0c8eb2170d245408f3ac6c88dd9fe5ed3320a5fd1b5a9a44": (
        "pip install the verifier packages (ragas, langchain-community, openai, anthropic, ...)"
    ),
    "bbc29a80038b4a2084bc8289ce7fb383771db1269de7bf695bde2404a40e7b1e": (
        "import smoke test of the verifier packages"
    ),
    "eb2df16d732f2f2192c2fdd5e7f51a696846d691c83003b646e0721d5137ad02": (
        "mkdir /workspace/{skills,input,output} /logs/{verifier,agent}"
    ),
    "fe32887e65d406e29057d7caab79d9eab3ff4031674c34e3f80a18b65a793ed0": (
        "root check before the runtime projection"
    ),
    "141fc4a282dbce07a0b7d92e8193eb6d694e71d904cbac433eb39040fd837098": (
        "rm the skill directories (reset)"
    ),
    "df1e865e272373cfd66ccfd139d21a40ee2fc18efa73b79aa2b5eb797e9ed300": (
        "mkdir the skill directories (reset)"
    ),
    "2017ece6e3b6e6a98f1686d30c0f51ef5bf422145a9ceaa758c018e845bc6cff": (
        "reset skill directories above the working directory"
    ),
    "7e01c91ce66427bad10a3f4fc506bddcc989573fdbba3c2c6efd8ff3a7a6d9bf": (
        "clear skill roots in $HOME"
    ),
    "7d2e1733ea46eb0f09f535fb68de93fbfffaa4621a1f3c4c2b3cd53503b7a74c": (
        "clear skill roots in every home directory of /etc/passwd"
    ),
    "9abebe11a34bf3c1b2485ef65ad921a149d82245b8ce6ec7c7f44ade5d49ede1": (
        "clear skill roots of CLAUDE_CONFIG_DIR, CODEX_HOME and the like"
    ),
    "50919401b2685b540aa2ed964ca0b539d399f30066c527873568402489a0f71c": ("rm /workspace/repo"),
    "fd3776fb1f24d7825686b9d5e58afb721efe1896a764a25a426a33bdf199bf97": ("mkdir /workspace/repo"),
    "07deb321247a4705cb4fded7a644560483b3a5bae1e90c1dac68008bcb4f36db": ("rm /workspace/input"),
    "dd2bcabf8eb37f17301a947d4bfabfe0e493d4c0f8182e014fc0fc0119419de4": ("mkdir /workspace/input"),
    "587ea364c6488e6d1e4cfe659f2b86c2ddb0c5a040b1b5706dba9d5b32de0038": (
        "check that the working directory and homes do not overlap a skill directory"
    ),
}

_COPY_FLAGS_IGNORED = ("--link",)
_IGNORED_INSTRUCTIONS = frozenset(
    {"LABEL", "EXPOSE", "ENTRYPOINT", "CMD", "HEALTHCHECK", "STOPSIGNAL", "MAINTAINER"}
)
_UNSUPPORTED_INSTRUCTIONS = frozenset({"ARG", "SHELL", "VOLUME", "ONBUILD"})
_ARCHIVE_SUFFIXES = (".tar", ".tar.gz", ".tgz", ".tar.bz2", ".tbz2", ".tar.xz", ".txz", ".zip")
_VARIABLE = re.compile(r"\$(?:(\w+)|\{(\w+)\})")
_GLOB = re.compile(r"[*?\[]")


class DockerfileError(ValueError):
    """The Dockerfile cannot be reproduced on the prebuilt sandbox image."""


@dataclass(frozen=True)
class Copy:
    """One ``COPY`` or ``ADD`` of local files, with the destination resolved against ``WORKDIR``."""

    sources: tuple[str, ...]
    dest: str
    chown: str | None = None
    chmod: str | None = None
    line: int = 0


@dataclass(frozen=True)
class Run:
    """One ``RUN`` step: the normalized command, its digest and the line it starts on."""

    command: str
    digest: str
    line: int


@dataclass(frozen=True)
class Plan:
    """What a Dockerfile asks for, in the parts a prebuilt image cannot have baked in."""

    base_image: str
    copies: tuple[Copy, ...] = ()
    runs: tuple[Run, ...] = ()
    env: dict[str, str] = field(default_factory=dict)
    workdir: str = "/"
    user: str | None = None

    def problems(
        self,
        base_images: frozenset[str] | set[str] = SANDBOX_BASE_IMAGES,
        covered_runs: dict[str, str] | None = None,
    ) -> list[str]:
        """List why the sandbox image cannot stand in for this Dockerfile, empty when it can."""
        covered = COVERED_RUNS if covered_runs is None else covered_runs
        problems = []
        if self.base_image not in base_images:
            problems.append(
                f"FROM {self.base_image} "
                f"(the sandbox image replaces {', '.join(sorted(base_images))})"
            )
        problems.extend(
            f"line {run.line}: RUN {_snippet(describe_run(run.command))}"
            for run in self.runs
            if run.digest not in covered
        )
        return problems

    def require_covered(
        self,
        base_images: frozenset[str] | set[str] = SANDBOX_BASE_IMAGES,
        covered_runs: dict[str, str] | None = None,
    ) -> None:
        """Raise :class:`DockerfileError` unless the sandbox image covers FROM and every RUN."""
        problems = self.problems(base_images, covered_runs)
        if not problems:
            return
        shown = "; ".join(problems[:3]) + (
            f"; and {len(problems) - 3} more" if len(problems) > 3 else ""
        )
        raise DockerfileError(
            f"the task Dockerfile needs {len(problems)} step(s) the prebuilt sandbox image "
            f"does not have: {shown}. Add them to sandbox/Dockerfile, or build task images instead "
            "(TASK_IMAGE_REGISTRY with your own registry and builder, see CONTRACT.md)"
        )


@dataclass(frozen=True)
class Transfer:
    """One upload that stands in for a ``COPY`` source.

    ``target`` is an absolute path in the sandbox. For a directory it receives the contents of
    ``source``. For a file it is the path of the file. ``maybe_dir`` marks a single file copied
    to a destination without a trailing ``/``: if that path is an existing directory in the
    image, the file goes inside it, as Docker does.
    """

    source: Path
    target: str
    is_dir: bool
    maybe_dir: bool = False
    chown: str | None = None
    chmod: str | None = None


def _snippet(text: str, limit: int = 120) -> str:
    """Shorten text to ``limit`` characters, keeping its start and its end where changes show."""
    if len(text) <= limit:
        return text
    head, tail = limit * 2 // 5, limit * 3 // 5 - 3
    return f"{text[:head]}...{text[-tail:]}"


def describe_run(command: str) -> str:
    """Return a normalized RUN command as a person would read it.

    SkillEvaluator wraps its commands in a fixed ``/bin/sh -c`` program that takes the real
    command after the word ``skillevaluator``; only that command is shown.
    """
    kind, _, body = command.partition(":")
    if kind != "exec":
        return body
    items = json.loads(body)
    if "skillevaluator" in items:
        items = items[items.index("skillevaluator") + 1 :]
    return " ".join(items)


def normalize_run(args: str) -> str:
    """Return a RUN command in a form that ignores formatting but not content.

    Exec form (``RUN ["a", "b"]``) is re-serialized as compact JSON, shell form has its
    whitespace (including line continuations) collapsed.
    """
    text = args.strip()
    if text.startswith("["):
        try:
            items = json.loads(text)
        except json.JSONDecodeError:
            items = None
        if isinstance(items, list) and all(isinstance(item, str) for item in items):
            return "exec:" + json.dumps(items, separators=(",", ":"), ensure_ascii=False)
    return "sh:" + " ".join(text.split())


def run_digest(command: str) -> str:
    """Return the digest that identifies a normalized RUN command in :data:`COVERED_RUNS`."""
    return hashlib.sha256(command.encode()).hexdigest()


def logical_lines(text: str) -> list[tuple[int, str]]:
    """Split a Dockerfile into instructions, joining continuations and dropping comments.

    Returns ``(first line number, text)`` pairs. A comment line inside a continued instruction
    is skipped, as Docker does.
    """
    result: list[tuple[int, str]] = []
    current: list[str] = []
    start = 0
    for number, raw in enumerate(text.splitlines(), start=1):
        line = raw.rstrip()
        stripped = line.lstrip()
        if re.match(r"#\s*escape\s*=", stripped) and not re.match(
            r"#\s*escape\s*=\s*\\\s*$", stripped
        ):
            raise DockerfileError(f"line {number}: a custom escape character is not supported")
        if stripped.startswith("#"):
            continue
        if not stripped:
            continue  # blank lines separate instructions and are ignored inside a continuation
        if not current:
            start = number
        if stripped.endswith("\\"):
            current.append(stripped[:-1])
            continue
        current.append(stripped)
        result.append(
            (start, "".join(current) if len(current) == 1 else " ".join(p.strip() for p in current))
        )
        current = []
    if current:
        result.append((start, " ".join(p.strip() for p in current)))
    return result


def _expand(value: str, env: dict[str, str]) -> str:
    return _VARIABLE.sub(lambda m: env.get(m.group(1) or m.group(2), ""), value)


def _parse_env(args: str, line: int) -> dict[str, str]:
    try:
        tokens = shlex.split(args)
    except ValueError as e:
        raise DockerfileError(f"line {line}: cannot read ENV: {e}") from e
    if not tokens:
        raise DockerfileError(f"line {line}: ENV needs a name and a value")
    if "=" not in tokens[0]:  # legacy form: ENV name value with spaces
        name, _, rest = args.strip().partition(" ")
        if not rest.strip():
            raise DockerfileError(f"line {line}: ENV needs a name and a value")
        return {name: rest.strip()}
    pairs: dict[str, str] = {}
    for token in tokens:
        name, sep, value = token.partition("=")
        if not sep or not name:
            raise DockerfileError(f"line {line}: ENV expects name=value pairs, got {token!r}")
        pairs[name] = value
    return pairs


def _parse_copy(keyword: str, args: str, line: int, workdir: str, env: dict[str, str]) -> Copy:
    chown = chmod = None
    text = args.strip()
    if text.startswith("["):
        try:
            tokens = json.loads(text)
        except json.JSONDecodeError as e:
            raise DockerfileError(f"line {line}: cannot read {keyword}: {e}") from e
        if not isinstance(tokens, list) or not all(isinstance(t, str) for t in tokens):
            raise DockerfileError(f"line {line}: {keyword} expects a JSON array of strings")
    else:
        try:
            tokens = shlex.split(text)
        except ValueError as e:
            raise DockerfileError(f"line {line}: cannot read {keyword}: {e}") from e
        flags = []
        while tokens and tokens[0].startswith("--"):
            flags.append(tokens.pop(0))
        for flag in flags:
            name, _, value = flag.partition("=")
            if name == "--chown":
                chown = value
            elif name == "--chmod":
                chmod = value
            elif name not in _COPY_FLAGS_IGNORED:
                raise DockerfileError(f"line {line}: {keyword} {name} is not supported")
    if len(tokens) < 2:
        raise DockerfileError(f"line {line}: {keyword} needs a source and a destination")
    sources = [_expand(token, env) for token in tokens[:-1]]
    dest = _expand(tokens[-1], env)
    for source in sources:
        if "://" in source or source.startswith("git@"):
            raise DockerfileError(
                f"line {line}: {keyword} of a remote source {source!r} is not supported"
            )
        if keyword == "ADD" and source.lower().endswith(_ARCHIVE_SUFFIXES):
            raise DockerfileError(
                f"line {line}: ADD would unpack the archive {source!r}, "
                "which is not supported; use COPY"
            )
    resolved = dest if dest.startswith("/") else posixpath.join(workdir, dest)
    if dest.endswith("/") and not resolved.endswith("/"):
        resolved += "/"
    return Copy(tuple(sources), resolved, chown, chmod, line)


def parse(text: str) -> Plan:
    """Read a Dockerfile into a :class:`Plan`.

    Raises :class:`DockerfileError` for what cannot be replayed on the sandbox image.
    """
    base_image = None
    copies: list[Copy] = []
    runs: list[Run] = []
    env: dict[str, str] = {}
    workdir = "/"
    user = None
    for line, instruction in logical_lines(text):
        keyword, _, args = instruction.partition(" ")
        keyword = keyword.upper()
        args = args.strip()
        if "<<" in args and keyword in {"RUN", "COPY", "ADD"}:
            raise DockerfileError(f"line {line}: here-documents are not supported")
        if keyword == "FROM":
            if base_image is not None:
                raise DockerfileError(f"line {line}: multi-stage builds are not supported")
            base_image = args.split()[0] if args else ""
            if base_image.startswith("--"):
                raise DockerfileError(f"line {line}: FROM options are not supported")
        elif base_image is None:
            raise DockerfileError(f"line {line}: the Dockerfile must start with FROM")
        elif keyword == "RUN":
            command = normalize_run(args)
            runs.append(Run(command, run_digest(command), line))
        elif keyword in {"COPY", "ADD"}:
            copies.append(_parse_copy(keyword, args, line, workdir, env))
        elif keyword == "ENV":
            env.update({k: _expand(v, env) for k, v in _parse_env(args, line).items()})
        elif keyword == "WORKDIR":
            target = _expand(args, env)
            workdir = posixpath.normpath(
                target if target.startswith("/") else posixpath.join(workdir, target)
            )
        elif keyword == "USER":
            user = _expand(args, env).split(":")[0]
        elif keyword in _IGNORED_INSTRUCTIONS:
            continue
        elif keyword in _UNSUPPORTED_INSTRUCTIONS:
            raise DockerfileError(f"line {line}: {keyword} is not supported")
        else:
            raise DockerfileError(f"line {line}: unknown instruction {keyword!r}")
    if base_image is None:
        raise DockerfileError("the Dockerfile has no FROM")
    return Plan(base_image, tuple(copies), tuple(runs), env, workdir, user)


def _inside(context: Path, path: Path) -> bool:
    return path == context or context in path.parents


def resolve_copy(copy: Copy, context: Path) -> list[Transfer]:
    """Turn a :class:`Copy` into uploads from the build context. Raises when a source is missing."""
    context = context.resolve()
    matches: list[Path] = []
    for source in copy.sources:
        relative = source.lstrip("/") or "."
        found = sorted(context.glob(relative)) if _GLOB.search(relative) else [context / relative]
        found = [p for p in found if p.exists() or p.is_symlink()]
        if not found:
            raise DockerfileError(
                f"line {copy.line}: COPY source {source!r} is not in the build context"
            )
        for path in found:
            resolved = path.resolve()
            if not _inside(context, resolved):
                raise DockerfileError(
                    f"line {copy.line}: COPY source {source!r} leaves the build context"
                )
            matches.append(resolved)
    into_dir = copy.dest.endswith("/") or len(matches) > 1
    target = copy.dest.rstrip("/") or "/"
    transfers = []
    for path in matches:
        if path.is_dir():
            transfers.append(Transfer(path, target, True, chown=copy.chown, chmod=copy.chmod))
        elif into_dir:
            transfers.append(
                Transfer(
                    path,
                    posixpath.join(target, path.name),
                    False,
                    chown=copy.chown,
                    chmod=copy.chmod,
                )
            )
        else:
            transfers.append(
                Transfer(path, target, False, maybe_dir=True, chown=copy.chown, chmod=copy.chmod)
            )
    return transfers


def _main(argv: list[str]) -> int:
    if len(argv) != 3 or argv[1] != "digests":
        print(
            "usage: python -m harbor_agent_sandbox.dockerfile digests <Dockerfile>", file=sys.stderr
        )
        return 2
    plan = parse(Path(argv[2]).read_text())
    print(f"FROM {plan.base_image}")
    for run in plan.runs:
        print(f'    "{run.digest}": "{_snippet(describe_run(run.command), 60)}",')
    return 0


if __name__ == "__main__":
    raise SystemExit(_main(sys.argv))
