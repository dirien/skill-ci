"""Build the trial bundle of one run: every trial's session, redacted, as an OCI referrer.

CONTRACT.md, "Trial bundles", is the specification; ``testdata/trials/`` holds the golden
input (``run-dir/``) and output (``bundle/``), ``docs/schemas/`` the JSON schemas. The bundle
is built only from SkillEvaluator's redacted per-trial copies,
``<run_dir>/<agent>/<with-skill|without-skill>/trials/<trial id>/``, plus the run-level
``result.json`` (case and attempt of each trial) and ``dataset_snapshot.json`` (the cases).
It never reads ``_harbor-jobs``, ``agent/sessions/``, ``config.json`` or ``lock.json``.

Redaction happens here, before anything is uploaded, and fails closed: a trial whose
serialized document still contains a secret is withheld, and an index that does is withheld
with the whole bundle. ``skill_eval.py`` attaches the bundle before the report and puts its
digest on the report (``skillci.dev/trials``), or the reason it has none
(``skillci.dev/trials-withheld``). A bundle that cannot be built or attached never fails the
eval.

Status: phase 0 stub. Every function raises NotImplementedError until work package 1 builds it.
"""

from __future__ import annotations

from collections.abc import Iterable, Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

ARTIFACT_TYPE = "application/vnd.skillci.trials.v1+json"
INDEX_MEDIA_TYPE = "application/vnd.skillci.trials.index.v1+json"
TRIAL_MEDIA_TYPE = "application/vnd.skillci.trial.v1+gzip"
INDEX_FILE = "index.json"
SCHEMA_INDEX = "skillci.trials.index.v1"
SCHEMA_TRIAL = "skillci.trial.v1"
SCHEMA_MAJOR = "1"  # annotation skillci.dev/trials-schema
REDACTION_VERSION = "v1"  # annotation skillci.dev/redaction and the index's redaction field
REDACTED = "[redacted]"  # pattern and env-dump replacements; exact values become [redacted:<NAME>]
MIN_SECRET_CHARS = 8  # shorter values are not replaced (they would match ordinary text)
ARMS = ("with-skill", "without-skill")
AGENT_LOGS = {"claude-code": "claude-code.txt", "codex": "codex.txt", "opencode": "opencode.txt"}
TRIAL_ID = r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$"  # the controller refuses any other id
# Reasons a trial (Withheld.reason) or the whole bundle (skillci.dev/trials-withheld) is left out.
WITHHELD_SCAN = "secret-scan"  # the fail-closed scan found a secret form
WITHHELD_SIZE = "size"
WITHHELD_LIMIT = "limit"
WITHHELD_UNREADABLE = "unreadable"
WITHHELD_DISABLED = "disabled"  # TRIAL_BUNDLE=off; bundle level only
WITHHELD_NO_TRIALS = "no-trials"  # Tier 3 left no trial directory; bundle level only

# Environment variables (CONTRACT.md, "Runner Job contract").
ENV_SWITCH = "TRIAL_BUNDLE"  # on (default) | off
ENV_MAX_BYTES = "TRIAL_BUNDLE_MAX_BYTES"  # compressed bytes of the whole bundle
ENV_OUTPUT_MAX_BYTES = "TRIAL_OUTPUT_MAX_BYTES"  # observation cap when a trial is over its limit


@dataclass(frozen=True)
class Limits:
    """The caps of one bundle, recorded in the index (``limits``).

    ``string_bytes``: one string, keeping its first 3/4 and last 1/4 with a marker between.
    ``trial_bytes``: one compressed trial layer. Past it the builder drops the agent log tail,
    then caps every observation at ``output_bytes``, then leaves out the trajectory
    (``trajectoryOmitted``), and only then withholds the trial (reason ``size``).
    ``bundle_bytes``: all compressed layers; trials past it are withheld (``size``), newest
    case and attempt first. ``trials_per_arm``: trials past it are withheld (``limit``).
    """

    string_bytes: int = 32 << 10
    output_bytes: int = 8 << 10
    log_tail_bytes: int = 64 << 10
    exception_bytes: int = 8 << 10
    trial_bytes: int = 1 << 20
    bundle_bytes: int = 24 << 20
    trials_per_arm: int = 50
    flow_rows: int = 200

    def as_index(self) -> dict[str, int]:
        """Return the ``limits`` object of the index (camelCase keys)."""
        raise NotImplementedError


@dataclass(frozen=True)
class Credential:
    """One provider route of the run, by name: ``role`` agent or judge, ``env``, ``host``."""

    role: str
    env: str
    host: str


@dataclass
class RedactionCounts:
    """How many JSON string values each rule changed (never the values)."""

    exact: int = 0
    pattern: int = 0
    env_dump: int = 0


@dataclass
class Bundle:
    """A built bundle in ``directory``, ready for ``oras attach``.

    ``files`` are the layer file names in attach order (``index.json`` first), ``index`` the
    index document, ``annotations`` the manifest annotations, ``withheld`` the trials left out.
    """

    directory: Path
    files: list[str]
    index: dict[str, Any]
    annotations: dict[str, str]
    withheld: list[dict[str, Any]] = field(default_factory=list)


@dataclass(frozen=True)
class NotBuilt:
    """Why there is no bundle: the value of the report's ``skillci.dev/trials-withheld``."""

    reason: str
    detail: str = ""


def enabled(environ: Mapping[str, str]) -> bool:
    """Return whether ``TRIAL_BUNDLE`` allows a bundle (``on`` by default; ``off`` disables)."""
    raise NotImplementedError


def limits_from_env(environ: Mapping[str, str]) -> Limits:
    """Return the limits with ``TRIAL_BUNDLE_MAX_BYTES`` and ``TRIAL_OUTPUT_MAX_BYTES`` applied.

    A value that is not a positive integer is ignored (the default stays) and logged.
    """
    raise NotImplementedError


def secret_values(environ: Mapping[str, str], auth_config: Path) -> dict[str, str]:
    """Return the exact secret values to redact, by name.

    The values of ``SECRET_ENV`` (skill_eval.py), ``SKILL_EVAL_EMBEDDING_API_KEY`` and
    ``SKILL_EVAL_LLM_API_KEY``, every ``HARBOR_*`` credential, and the ``auth``, ``password``
    and ``identitytoken`` values of the registry config at ``auth_config`` (``/auth/config.json``)
    together with the password decoded from each ``auth``. Values shorter than
    ``MIN_SECRET_CHARS`` are left out.
    """
    raise NotImplementedError


def secret_forms(value: str) -> list[str]:
    """Return the forms of one secret that are replaced and scanned for, longest first.

    The raw value, standard base64 with and without padding, URL-safe base64 without padding,
    the URL-encoded form (every reserved character escaped) and the JSON-escaped form.
    """
    raise NotImplementedError


class Redactor:
    """Redaction v1 over every string of a parsed JSON document.

    Rules, in order, each counted when it changes a string: ``exact`` replaces every form of
    every secret value with ``[redacted:<NAME>]``; ``pattern`` applies SkillEvaluator's
    ``redact_sensitive_text`` and replaces Ollama-style keys (32 hex characters, a dot, 24 or
    more URL-safe base64 characters) with ``[redacted]``; ``envDump`` replaces the value of
    every ``NAME=value`` line whose NAME contains KEY, TOKEN, SECRET, PASSWORD, PASSWD, AUTH or
    CREDENTIAL (any case, optional ``export``) with ``[redacted]``, unless it is redacted already.
    """

    def __init__(self, secrets: Mapping[str, str]) -> None:
        """Prepare the forms of ``secrets`` (name to value)."""
        raise NotImplementedError

    @property
    def counts(self) -> RedactionCounts:
        """Return the counts since the last ``reset``."""
        raise NotImplementedError

    def reset(self) -> None:
        """Start counting from zero (once per trial document)."""
        raise NotImplementedError

    def text(self, value: str) -> str:
        """Return ``value`` with every rule applied."""
        raise NotImplementedError

    def walk(self, doc: Any) -> Any:
        """Return a copy of a parsed JSON value with ``text`` applied to every string.

        Keys are kept; only values change, so JSON pointers stay valid.
        """
        raise NotImplementedError

    def leaks(self, data: bytes) -> list[str]:
        """Return the names of the secrets any of whose forms occur in ``data``.

        The fail-closed check over the final serialized bytes (before gzip).
        """
        raise NotImplementedError


def shorten(doc: Any, limit: int, pointer: str = "") -> tuple[Any, list[dict[str, Any]]]:
    """Shorten every string over ``limit`` UTF-8 bytes in place.

    Keeps the first 3/4 and the last 1/4 of ``limit`` and puts
    ``\\n...[skill-ci: <n> bytes cut]...\\n`` between them. Returns the new document and one
    ``{"pointer", "originalBytes"}`` per shortened string; pointers are RFC 6901, relative to
    the trial document (``/trajectory/steps/4/observation/results/0/content``).
    """
    raise NotImplementedError


def trial_map(result: Mapping[str, Any], agent: str) -> dict[tuple[str, str], tuple[str, int]]:
    """Map (arm, trial id) to (case, attempt).

    From ``agents.<agent>.pass_at_k.<with_skill|without_skill>.cases.<case>.attempts[]``;
    trials only in ``trial_failures`` (no attempt recorded) get the case before the last
    ``__`` of their id and attempts numbered in order of appearance.
    """
    raise NotImplementedError


def read_trial(
    trial_dir: Path,
    *,
    arm: str,
    case: str,
    attempt: int,
    agent: str,
    dataset_question: str,
    limits: Limits,
    redactor: Redactor,
    network: Mapping[str, Any] | None,
) -> tuple[dict[str, Any], dict[str, Any]]:
    """Return the trial document and its index entry for one trial directory.

    Reads ``result.json`` (meta: task name, times, phases, agent, agent result, exception with
    ``exception.txt`` as traceback, last ``exception_bytes``), ``trajectory.json``,
    ``reward.json``, ``failure.json``, ``artifact_manifest.json`` (why a trajectory is
    missing) and the agent log (last ``log_tail_bytes``). Redacts, then shortens, then fills
    ``truncations``, ``redaction`` and ``network``. The index entry carries the counts the grid
    needs (score, passed, criteria, tokens, cost and its source, durations rounded to 0.1 s,
    sizes). The caller compresses the document and enforces ``trial_bytes``.
    """
    raise NotImplementedError


def build(
    run_dir: Path,
    out: Path,
    *,
    environ: Mapping[str, str],
    config: Mapping[str, str],
    agent_version: str,
    credentials: Iterable[Credential],
    network: Mapping[str, Any],
    trial_networks: Mapping[str, Mapping[str, Any]] | None = None,
    auth_config: Path = Path("/auth/config.json"),
) -> Bundle | NotBuilt:
    """Build the bundle of one Tier 3 run into ``out``.

    ``run_dir`` is SkillEvaluator's run directory (``newest_result(results).parent``),
    ``config`` the runner config (``SKILL_NAME``, ``SKILL_REF``, ``AGENT``, ``MODEL``,
    ``ATTEMPTS``, ``SKILLEVAL_NAME``, optional ``SKILL_VERSION``), ``network`` the index's
    run-level network state from flows.py and ``trial_networks`` the per-trial sections.
    Writes ``index.json`` and one ``trial-<id>.json.gz`` per bundled trial (compact UTF-8 JSON,
    gzip), enforces every limit, runs the fail-closed scan, and returns the Bundle, or NotBuilt
    when the switch is off, there are no trials, or the index itself leaks.
    """
    raise NotImplementedError


def oras_files(bundle: Bundle) -> list[str]:
    """Return the ``<file>:<media type>`` arguments of ``oras attach``, index first."""
    raise NotImplementedError


def redact_report_files(directory: Path, files: Iterable[str], redactor: Redactor) -> list[str]:
    """Apply the same redaction to the report files before they are attached.

    JSON files are redacted as parsed documents, HTML and Markdown as text. Returns the files
    that may be attached: a file that still leaks after redaction is removed from the list
    (and logged), never attached.
    """
    raise NotImplementedError
