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

Pattern redaction uses SkillEvaluator's ``redact_sensitive_text``. Without SkillEvaluator
(it is part of the runner image) a ``Redactor`` cannot be created, so no bundle is built.
"""

from __future__ import annotations

import base64
import binascii
import gzip
import json
import math
import re
import sys
from collections.abc import Callable, Iterable, Mapping
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Any
from urllib.parse import quote

try:
    from skillevaluator.utils.redaction import redact_sensitive_text
except ImportError:  # the runner image has it; the CI tests stub it
    redact_sensitive_text: Callable[[str], str] | None = None  # type: ignore[no-redef]

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

# The provider keys of the runner (skill_eval.SECRET_ENV), SkillEvaluator's own keys (the runner
# may copy the judge's key into them) and the Bedrock judge's token. The judge's key may be any
# llm-keys key, so every *_API_KEY variable is a secret too.
SECRET_ENV = (
    "ANTHROPIC_API_KEY",
    "OPENAI_API_KEY",
    "NVIDIA_API_KEY",
    "OLLAMA_API_KEY",
    "OPENROUTER_API_KEY",
    "SKILL_EVAL_EMBEDDING_API_KEY",
    "SKILL_EVAL_LLM_API_KEY",
    "AWS_BEARER_TOKEN_BEDROCK",  # the bedrock judge's key
)
# A HARBOR_* variable is a credential when its name holds one of these (HARBOR_PASSWORD, not
# HARBOR_SANDBOX_IMAGE); the env-dump rule uses the same words.
CREDENTIAL_WORDS = ("KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "AUTH", "CREDENTIAL")
# Fields of a registry entry in /auth/config.json that hold a credential, and their names.
REGISTRY_FIELDS = {
    "auth": "REGISTRY_AUTH",
    "password": "REGISTRY_PASSWORD",
    "identitytoken": "REGISTRY_IDENTITYTOKEN",
    "registrytoken": "REGISTRY_REGISTRYTOKEN",
}
OFF_VALUES = ("off", "false", "0", "no", "disabled")
MAX_LAYER_BYTES = 16 << 20  # the controller's blob limit; no layer may be larger
ERROR_MESSAGE_CHARS = 300
CASE_QUESTION_CHARS = 300
PHASES = ("environment_setup", "agent_setup", "agent_execution", "verifier")
VERDICTS = ("FORWARDED", "DROPPED", "ERROR", "AUDIT")
NETWORK_STATES = ("collected", "partial", "unavailable", "off")
DIGEST = re.compile(r"@(sha256:[0-9a-f]{64})$")
VERSION = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+$")
AGENT_VERSION = re.compile(r"^[A-Za-z0-9]([A-Za-z0-9._-]{0,61}[A-Za-z0-9])?$")
TRIAL_ID_RE = re.compile(TRIAL_ID)
# Ollama-style API keys: 32 hex characters, a dot, 24 or more URL-safe base64 characters.
OLLAMA_KEY = re.compile(r"(?<![0-9A-Za-z])[0-9a-f]{32}\.[A-Za-z0-9_-]{24,}")
# One NAME=value line of an environment dump (optional ``export``); the name is checked below.
ENV_LINE = re.compile(
    r"^(?P<lead>[ \t]*(?:export[ \t]+)?(?P<name>[A-Za-z_][A-Za-z0-9_]*)=)(?P<value>[^\r\n]*)$",
    re.MULTILINE,
)
SHORTENED = "\n...[skill-ci: {cut} bytes cut]...\n"


def log(message: str) -> None:
    """Print one line to stderr. Never pass a secret value, only names."""
    print(f"skill-eval: trial bundle: {message}", file=sys.stderr, flush=True)


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
        return {
            "stringBytes": self.string_bytes,
            "outputBytes": self.output_bytes,
            "logTailBytes": self.log_tail_bytes,
            "exceptionBytes": self.exception_bytes,
            "trialBytes": self.trial_bytes,
            "bundleBytes": self.bundle_bytes,
            "trialsPerArm": self.trials_per_arm,
            "flowRows": self.flow_rows,
        }


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
    return (environ.get(ENV_SWITCH) or "on").strip().lower() not in OFF_VALUES


def _positive_int(environ: Mapping[str, str], name: str, minimum: int) -> int | None:
    raw = (environ.get(name) or "").strip()
    if not raw:
        return None
    try:
        value = int(raw)
    except ValueError:
        value = 0
    if value < max(1, minimum):
        log(f"ignoring {name}={raw!r}: not an integer of at least {max(1, minimum)}")
        return None
    return value


def limits_from_env(environ: Mapping[str, str]) -> Limits:
    """Return the limits with ``TRIAL_BUNDLE_MAX_BYTES`` and ``TRIAL_OUTPUT_MAX_BYTES`` applied.

    A value that is not a positive integer is ignored (the default stays) and logged, and so
    is one under the minimum of the index schema (1024 and 256 bytes).
    """
    limits = Limits()
    bundle = _positive_int(environ, ENV_MAX_BYTES, 1024)
    output = _positive_int(environ, ENV_OUTPUT_MAX_BYTES, 256)
    return Limits(
        string_bytes=limits.string_bytes,
        output_bytes=output or limits.output_bytes,
        log_tail_bytes=limits.log_tail_bytes,
        exception_bytes=limits.exception_bytes,
        trial_bytes=limits.trial_bytes,
        bundle_bytes=bundle or limits.bundle_bytes,
        trials_per_arm=limits.trials_per_arm,
        flow_rows=limits.flow_rows,
    )


def _is_credential_name(name: str) -> bool:
    upper = name.upper()
    return any(word in upper for word in CREDENTIAL_WORDS)


def _registry_secrets(auth_config: Path) -> list[tuple[str, str]]:
    try:
        text = auth_config.read_text(encoding="utf-8")
    except FileNotFoundError:
        return []
    except OSError as err:
        log(f"cannot read the registry config {auth_config}: {type(err).__name__}")
        return []
    try:
        config = json.loads(text)
    except ValueError:
        log(f"the registry config {auth_config} is not JSON; its credentials are not redacted")
        return []
    auths = config.get("auths") if isinstance(config, dict) else None
    found: list[tuple[str, str]] = []
    for entry in (auths or {}).values() if isinstance(auths, dict) else ():
        if not isinstance(entry, dict):
            continue
        for key, name in REGISTRY_FIELDS.items():
            value = entry.get(key)
            if isinstance(value, str) and value:
                found.append((name, value))
        auth = entry.get("auth")
        if isinstance(auth, str) and auth:
            try:
                decoded = base64.b64decode(auth + "=" * (-len(auth) % 4)).decode("utf-8")
            except (binascii.Error, ValueError):
                continue
            password = decoded.partition(":")[2]
            if password:
                found.append(("REGISTRY_PASSWORD", password))
    return found


def secret_values(environ: Mapping[str, str], auth_config: Path) -> dict[str, str]:
    """Return the exact secret values to redact, by name.

    The values of ``SECRET_ENV`` (skill_eval.py), ``SKILL_EVAL_EMBEDDING_API_KEY``,
    ``SKILL_EVAL_LLM_API_KEY`` and ``AWS_BEARER_TOKEN_BEDROCK``, every other ``*_API_KEY``
    (the judge's key may be any llm-keys key), every ``HARBOR_*`` credential, and the
    ``auth``, ``password`` and ``identitytoken`` values of the registry config at
    ``auth_config`` (``/auth/config.json``) together with the password decoded from each
    ``auth``. Values shorter than ``MIN_SECRET_CHARS`` are left out.
    A name that occurs twice (two registries) gets ``_2``, ``_3`` and so on.
    """
    names = list(SECRET_ENV)
    names += sorted(
        name
        for name in environ
        if name not in SECRET_ENV
        and (
            name.endswith("_API_KEY") or (name.startswith("HARBOR_") and _is_credential_name(name))
        )
    )
    pairs = [(name, environ.get(name) or "") for name in names]
    pairs += _registry_secrets(auth_config)
    values: dict[str, str] = {}
    for name, value in pairs:
        if len(value) < MIN_SECRET_CHARS or value in values.values():
            continue
        key, n = name, 1
        while key in values:
            n += 1
            key = f"{name}_{n}"
        values[key] = value
    return values


def secret_forms(value: str) -> list[str]:
    """Return the forms of one secret that are replaced and scanned for, longest first.

    The raw value, standard base64 with and without padding, URL-safe base64 with and without
    padding, the URL-encoded form (every reserved character escaped) and the JSON-escaped form
    (ASCII and UTF-8).
    """
    raw = value.encode("utf-8", "replace")
    std = base64.b64encode(raw).decode("ascii")
    url = base64.urlsafe_b64encode(raw).decode("ascii")
    forms = {
        value,
        std,
        std.rstrip("="),
        url,
        url.rstrip("="),
        quote(value, safe=""),
        json.dumps(value)[1:-1],
        json.dumps(value, ensure_ascii=False)[1:-1],
    }
    return sorted((f for f in forms if f), key=lambda f: (-len(f), f))


def _already_redacted(value: str) -> bool:
    return "[redacted" in value or "<redacted>" in value


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
        """Prepare the forms of ``secrets`` (name to value).

        Raises RuntimeError when SkillEvaluator's ``redact_sensitive_text`` is not importable:
        without the pattern rule there is no redaction v1, so nothing may be uploaded.
        """
        if redact_sensitive_text is None:
            raise RuntimeError(
                "skillevaluator.utils.redaction is not importable; redaction v1 needs it"
            )
        self._pattern: Callable[[str], str] = redact_sensitive_text
        names: dict[str, str] = {}
        for name, value in secrets.items():
            if not isinstance(value, str) or len(value) < MIN_SECRET_CHARS:
                continue
            for form in secret_forms(value):
                names.setdefault(form, name)
        forms = sorted(names, key=lambda f: (-len(f), f))
        self._names = names
        self._exact = re.compile("|".join(re.escape(f) for f in forms)) if forms else None
        self._bytes = [(f.encode("utf-8", "replace"), names[f]) for f in forms]
        self._counts = RedactionCounts()

    @property
    def counts(self) -> RedactionCounts:
        """Return the counts since the last ``reset``."""
        return RedactionCounts(self._counts.exact, self._counts.pattern, self._counts.env_dump)

    def reset(self) -> None:
        """Start counting from zero (once per trial document)."""
        self._counts = RedactionCounts()

    def _env_dump(self, match: re.Match[str]) -> str:
        value = match.group("value")
        if not value or not _is_credential_name(match.group("name")) or _already_redacted(value):
            return match.group(0)
        return match.group("lead") + REDACTED

    def text(self, value: str) -> str:
        """Return ``value`` with every rule applied."""
        out = value
        if self._exact is not None:
            out = self._exact.sub(lambda m: f"[redacted:{self._names[m.group(0)]}]", out)
            if out != value:
                self._counts.exact += 1
        before = out
        out = OLLAMA_KEY.sub(REDACTED, self._pattern(out))
        if out != before:
            self._counts.pattern += 1
        before = out
        if "=" in out:
            out = ENV_LINE.sub(self._env_dump, out)
        if out != before:
            self._counts.env_dump += 1
        return out

    def walk(self, doc: Any) -> Any:
        """Return a copy of a parsed JSON value with ``text`` applied to every string.

        Keys are kept; only values change, so JSON pointers stay valid. A float that JSON cannot
        hold (NaN, infinity) becomes null.
        """
        if isinstance(doc, str):
            return self.text(doc)
        if isinstance(doc, dict):
            return {key: self.walk(value) for key, value in doc.items()}
        if isinstance(doc, list):
            return [self.walk(value) for value in doc]
        if isinstance(doc, float) and not math.isfinite(doc):
            return None
        return doc

    def leaks(self, data: bytes) -> list[str]:
        """Return the names of the secrets any of whose forms occur in ``data``.

        The fail-closed check over the final serialized bytes (before gzip).
        """
        return sorted({name for form, name in self._bytes if form in data})


def _escape(key: str) -> str:
    return key.replace("~", "~0").replace("/", "~1")


def _utf8(value: str) -> bytes:
    return value.encode("utf-8", "replace")


def _cut(value: str, limit: int, original: int | None = None) -> str:
    data = _utf8(value)
    head_n = limit * 3 // 4
    head = data[:head_n].decode("utf-8", "ignore")
    tail = data[len(data) - (limit - head_n) :].decode("utf-8", "ignore")
    cut = (original if original is not None else len(data)) - len(_utf8(head)) - len(_utf8(tail))
    return head + SHORTENED.format(cut=cut) + tail


def _shorten(
    doc: Any, limit: int, pointer: str, originals: Mapping[str, int], out: list[dict[str, Any]]
) -> Any:
    if isinstance(doc, str):
        size = len(_utf8(doc))
        if size <= limit:
            return doc
        original = originals.get(pointer, size)
        out.append({"pointer": pointer, "originalBytes": original})
        return _cut(doc, limit, original)
    if isinstance(doc, dict):
        return {
            k: _shorten(v, limit, f"{pointer}/{_escape(k)}", originals, out) for k, v in doc.items()
        }
    if isinstance(doc, list):
        return [_shorten(v, limit, f"{pointer}/{i}", originals, out) for i, v in enumerate(doc)]
    return doc


def shorten(doc: Any, limit: int, pointer: str = "") -> tuple[Any, list[dict[str, Any]]]:
    """Shorten every string over ``limit`` UTF-8 bytes in place.

    Keeps the first 3/4 and the last 1/4 of ``limit`` and puts
    ``\\n...[skill-ci: <n> bytes cut]...\\n`` between them. Returns the new document and one
    ``{"pointer", "originalBytes"}`` per shortened string; pointers are RFC 6901, relative to
    the trial document (``/trajectory/steps/4/observation/results/0/content``).
    """
    out: list[dict[str, Any]] = []
    return _shorten(doc, limit, pointer, {}, out), out


def _arm_key(arm: str) -> str:
    return arm.replace("-", "_")


def _case_of(trial_id: str) -> str:
    return trial_id.rsplit("__", 1)[0] if "__" in trial_id else trial_id


class _Attempts:
    """Number the attempts of trials no result entry names, per arm and case."""

    def __init__(self, mapping: Mapping[tuple[str, str], tuple[str, int]]) -> None:
        self.last: dict[tuple[str, str], int] = {}
        for (arm, _), (case, attempt) in mapping.items():
            self.last[(arm, case)] = max(self.last.get((arm, case), 0), attempt)

    def next(self, arm: str, trial_id: str) -> tuple[str, int]:
        case = _case_of(trial_id)
        n = self.last.get((arm, case), 0) + 1
        self.last[(arm, case)] = n
        return case, n


def trial_map(result: Mapping[str, Any], agent: str) -> dict[tuple[str, str], tuple[str, int]]:
    """Map (arm, trial id) to (case, attempt).

    From ``agents.<agent>.pass_at_k.<with_skill|without_skill>.cases.<case>.attempts[]``;
    trials only in ``trial_failures`` (no attempt recorded) get the case before the last
    ``__`` of their id and attempts numbered in order of appearance, after the recorded ones.
    """
    entry = (result.get("agents") or {}).get(agent) or {}
    if not isinstance(entry, Mapping):
        return {}
    mapping: dict[tuple[str, str], tuple[str, int]] = {}
    pass_at_k = entry.get("pass_at_k") or {}
    for arm in ARMS:
        cases = (
            (pass_at_k.get(_arm_key(arm)) or {}) if isinstance(pass_at_k, Mapping) else {}
        ).get("cases") or {}
        for case, detail in cases.items() if isinstance(cases, Mapping) else ():
            attempts = detail.get("attempts") if isinstance(detail, Mapping) else None
            for i, attempt in enumerate(attempts or [], start=1):
                if not isinstance(attempt, Mapping) or not isinstance(attempt.get("trial"), str):
                    continue
                n = attempt.get("attempt")
                n = n if isinstance(n, int) and not isinstance(n, bool) and n >= 1 else i
                mapping[(arm, attempt["trial"])] = (str(case), n)
    numbering = _Attempts(mapping)
    failures = entry.get("trial_failures") or {}
    for arm in ARMS:
        rows = failures.get(_arm_key(arm)) if isinstance(failures, Mapping) else None
        for row in rows or []:
            trial = row.get("trial") if isinstance(row, Mapping) else None
            if isinstance(trial, str) and trial and (arm, trial) not in mapping:
                mapping[(arm, trial)] = numbering.next(arm, trial)
    return mapping


def _read_json(path: Path) -> Any:
    """Return the parsed file, or None when it is missing or not JSON."""
    try:
        return json.loads(path.read_text(encoding="utf-8", errors="replace"))
    except FileNotFoundError:
        return None
    except (OSError, ValueError) as err:
        log(f"cannot read {path.name}: {type(err).__name__}")
        return None


def _tail(path: Path, limit: int) -> tuple[str, int, bool] | None:
    """Return the last ``limit`` bytes of a text file as text, its size and whether it was cut."""
    try:
        data = path.read_bytes()
    except FileNotFoundError:
        return None
    except OSError as err:
        log(f"cannot read {path.name}: {type(err).__name__}")
        return None
    size = len(data)
    part = data[size - limit :] if size > limit else data
    if limit <= 0:
        part = b""
    while part and part[0] & 0xC0 == 0x80:  # start on a character boundary
        part = part[1:]
    return part.decode("utf-8", "replace"), size, size > limit


def _time(value: Any) -> str | None:
    """Return an RFC 3339 time string (``+00:00`` written as ``Z``), or None."""
    if not isinstance(value, str) or not value:
        return None
    try:
        datetime.fromisoformat(value)
    except ValueError:
        return None
    return value[:-6] + "Z" if value.endswith("+00:00") else value


def _seconds(start: str | None, end: str | None) -> float | None:
    if not start or not end:
        return None
    try:
        delta = (datetime.fromisoformat(end) - datetime.fromisoformat(start)).total_seconds()
    except (TypeError, ValueError):
        return None
    return round(delta, 1) if delta >= 0 else None


def _count(value: Any) -> int | None:
    if isinstance(value, bool) or not isinstance(value, int | float):
        return None
    if isinstance(value, float) and (not math.isfinite(value) or not value.is_integer()):
        return None
    return int(value) if value >= 0 else None


def _money(value: Any) -> float | None:
    if isinstance(value, bool) or not isinstance(value, int | float):
        return None
    if not math.isfinite(value) or value < 0:
        return None
    return value


def _meta(result: Mapping[str, Any], agent: str, exception: Path, limits: Limits) -> dict:
    meta: dict[str, Any] = {}
    if isinstance(result.get("task_name"), str):
        meta["taskName"] = result["task_name"]
    phases = []
    for name in PHASES:
        phase = result.get(name)
        if isinstance(phase, Mapping):
            start, end = _time(phase.get("started_at")), _time(phase.get("finished_at"))
            if start and end:
                phases.append({"name": name, "startedAt": start, "finishedAt": end})
    meta["phases"] = phases
    info = result.get("agent_info") if isinstance(result.get("agent_info"), Mapping) else {}
    model = info.get("model_info") if isinstance(info.get("model_info"), Mapping) else {}
    who: dict[str, str] = {"name": info.get("name") if isinstance(info.get("name"), str) else agent}
    for key, value in (
        ("version", info.get("version")),
        ("model", model.get("name")),
        ("provider", model.get("provider")),
    ):
        if isinstance(value, str) and value:
            who[key] = value
    meta["agent"] = who
    for key, src in (("startedAt", "started_at"), ("finishedAt", "finished_at")):
        if value := _time(result.get(src)):
            meta[key] = value
    agent_result = result.get("agent_result")
    if isinstance(agent_result, Mapping):
        totals: dict[str, Any] = {}
        for key, src in (
            ("tokensIn", "n_input_tokens"),
            ("tokensCached", "n_cache_tokens"),
            ("tokensOut", "n_output_tokens"),
        ):
            if (n := _count(agent_result.get(src))) is not None:
                totals[key] = n
        if (cost := _money(agent_result.get("cost_usd"))) is not None:
            totals["costUsd"] = cost
        if totals:
            meta["agentResult"] = totals
    info = result.get("exception_info")
    if isinstance(info, Mapping):
        exc: dict[str, str] = {
            "type": str(info.get("exception_type") or "Exception"),
            "message": str(info.get("exception_message") or ""),
        }
        if occurred := _time(info.get("occurred_at")):
            exc["occurredAt"] = occurred
        tail = _tail(exception, limits.exception_bytes)
        if tail is not None:
            exc["traceback"] = tail[0]
        meta["exception"] = exc
    return meta


def _message_text(message: Any) -> str | None:
    if isinstance(message, str):
        return message
    if isinstance(message, list):
        parts = [
            part if isinstance(part, str) else part.get("text")
            for part in message
            if isinstance(part, str | Mapping)
        ]
        texts = [p for p in parts if isinstance(p, str)]
        return "".join(texts) if texts else None
    return None


def _prompt(trajectory: Mapping[str, Any] | None, question: str) -> str:
    for step in (trajectory or {}).get("steps") or []:
        if isinstance(step, Mapping) and step.get("source") == "user":
            text = _message_text(step.get("message"))
            if text is not None:
                return text
    return question


def _omitted_reason(trial_dir: Path, exists: bool) -> str:
    if exists:
        return "trajectory.json is not an ATIF trajectory"
    manifest = _read_json(trial_dir / "artifact_manifest.json")
    for row in (manifest or {}).get("skipped") or [] if isinstance(manifest, Mapping) else []:
        if isinstance(row, Mapping) and row.get("name") == "trajectory.json":
            return f"SkillEvaluator kept no trajectory: {row.get('reason') or 'no reason given'}"
    return "SkillEvaluator kept no trajectory"


def _criteria(reward: Mapping[str, Any]) -> dict[str, int] | None:
    details = reward.get("details")
    check = details.get("behavior_check") if isinstance(details, Mapping) else None
    results = check.get("results") if isinstance(check, Mapping) else None
    if not isinstance(results, list):
        return None
    rows = [r for r in results if isinstance(r, Mapping)]
    return {"passed": sum(1 for r in rows if r.get("passed") is True), "total": len(rows)}


def _network_counts(network: Mapping[str, Any]) -> dict[str, int]:
    flows = [f for f in network.get("flows") or [] if isinstance(f, Mapping)]
    return {
        "forwarded": sum(1 for f in flows if f.get("verdict") == "FORWARDED"),
        "dropped": sum(1 for f in flows if f.get("verdict") == "DROPPED"),
        "dns": len(network.get("dns") or []),
    }


def _entry(doc: Mapping[str, Any], pass_threshold: float) -> dict[str, Any]:
    """Return the index entry of a finished trial document (sizes are filled in later)."""
    trial_id = doc["id"]
    entry: dict[str, Any] = {
        "id": trial_id,
        "layer": f"trial-{trial_id}.json.gz",
        "arm": doc["arm"],
        "case": doc["case"],
        "attempt": doc["attempt"],
        "status": doc["status"],
    }
    meta = doc["meta"]
    reward = doc.get("reward") if isinstance(doc.get("reward"), Mapping) else None
    if doc["status"] == "scored" and reward is not None:
        score = _money(reward.get("overall"))
        entry["score"] = min(score, 1.0) if score is not None else 0.0
        entry["passed"] = entry["score"] >= pass_threshold
    else:
        exc = meta.get("exception")
        failure = doc.get("failure") if isinstance(doc.get("failure"), Mapping) else {}
        error = failure.get("error") if isinstance(failure.get("error"), Mapping) else {}
        kind = (exc or {}).get("type") or error.get("type")
        message = (exc or {}).get("message") or error.get("message")
        if isinstance(kind, str) and kind:
            entry["errorType"] = kind
        if isinstance(message, str) and message:
            entry["errorMessage"] = message[:ERROR_MESSAGE_CHARS]
    if meta.get("startedAt"):
        entry["startedAt"] = meta["startedAt"]
    if (duration := _seconds(meta.get("startedAt"), meta.get("finishedAt"))) is not None:
        entry["durationSec"] = duration
    for phase in meta["phases"]:
        seconds = _seconds(phase["startedAt"], phase["finishedAt"])
        if phase["name"] == "agent_execution" and seconds is not None:
            entry["agentSec"] = seconds
    steps = (doc.get("trajectory") or {}).get("steps") or []
    entry["steps"] = len(steps)
    entry["toolCalls"] = sum(
        len(s.get("tool_calls") or []) for s in steps if isinstance(s, Mapping)
    )
    final = (doc.get("trajectory") or {}).get("final_metrics")
    totals = meta.get("agentResult") or {}
    if isinstance(final, Mapping):
        tokens = {
            "tokensIn": _count(final.get("total_prompt_tokens")),
            "tokensOut": _count(final.get("total_completion_tokens")),
            "tokensCached": _count(final.get("total_cached_tokens")),
        }
        cost = _money(final.get("total_cost_usd"))
        extra = final.get("extra") if isinstance(final.get("extra"), Mapping) else {}
        source = "estimate" if extra.get("cost_source") == "litellm_estimate" else "reported"
    else:
        tokens = {k: totals.get(k) for k in ("tokensIn", "tokensOut", "tokensCached")}
        cost, source = totals.get("costUsd"), "reported"
    entry.update({k: v for k, v in tokens.items() if v is not None})
    if cost is not None:
        entry["costUsd"] = round(cost, 6)
        entry["costSource"] = source
    if reward is not None:
        if (criteria := _criteria(reward)) is not None:
            entry["criteria"] = criteria
        if isinstance(reward.get("skill_invoked"), bool):
            entry["skillInvoked"] = reward["skill_invoked"]
    entry["bytes"] = 0
    entry["rawBytes"] = 0
    entry["truncated"] = False
    network = doc.get("network")
    if isinstance(network, Mapping) and network.get("state") in ("collected", "partial"):
        entry["network"] = _network_counts(network)
    return entry


def _trial_doc(
    trial_id: str, arm: str, case: str, attempt: int, status: str, meta: dict[str, Any]
) -> dict[str, Any]:
    return {
        "schema": SCHEMA_TRIAL,
        "id": trial_id,
        "arm": arm,
        "case": case,
        "attempt": attempt,
        "status": status,
        "meta": meta,
    }


def _cap_network(network: Mapping[str, Any], rows: int) -> dict[str, Any]:
    section = dict(network)
    truncated = bool(section.get("truncated"))
    for key in ("dns", "flows"):
        items = list(section.get(key) or [])
        if len(items) > rows:
            items, truncated = items[:rows], True
        section[key] = items
    section["truncated"] = truncated
    section.setdefault("lost", 0)
    if truncated and section.get("state") == "collected":
        section["state"] = "partial"
    return section


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
    pass_threshold: float = 0.5,
) -> tuple[dict[str, Any], dict[str, Any]]:
    """Return the trial document and its index entry for one trial directory.

    Reads ``result.json`` (meta: task name, times, phases, agent, agent result, exception with
    ``exception.txt`` as traceback, last ``exception_bytes``), ``trajectory.json``,
    ``reward.json``, ``failure.json``, ``artifact_manifest.json`` (why a trajectory is
    missing) and the agent log (last ``log_tail_bytes``). Redacts, then shortens, then fills
    ``truncations``, ``redaction`` and ``network``. The index entry carries the counts the grid
    needs (score, passed against ``pass_threshold``, criteria, tokens, cost and its source,
    durations rounded to 0.1 s, sizes). The caller compresses the document and enforces
    ``trial_bytes``. A trial whose ``result.json`` cannot be read gets status ``error`` and
    only ``meta``. The agent log is not shortened: its own cap is ``log_tail_bytes``.
    """
    trial_id = trial_dir.name
    redactor.reset()
    result = _read_json(trial_dir / "result.json")
    parts: dict[str, Any] = {}
    if not isinstance(result, Mapping):
        status = "error"
        meta: dict[str, Any] = {"phases": [], "agent": {"name": agent}}
        parts["prompt"] = dataset_question
        parts["trajectoryOmitted"] = "the runner could not read the trial (result.json)"
    else:
        meta = _meta(result, agent, trial_dir / "exception.txt", limits)
        trajectory = _read_json(trial_dir / "trajectory.json")
        if not (isinstance(trajectory, dict) and isinstance(trajectory.get("steps"), list)):
            trajectory = None
        reward = _read_json(trial_dir / "reward.json")
        failure = _read_json(trial_dir / "failure.json")
        status = "scored" if isinstance(reward, dict) else "unscored"
        parts["prompt"] = _prompt(trajectory, dataset_question)
        if trajectory is not None:
            parts["trajectory"] = trajectory
        else:
            exists = (trial_dir / "trajectory.json").exists()
            parts["trajectoryOmitted"] = _omitted_reason(trial_dir, exists)
        if isinstance(reward, dict):
            parts["reward"] = reward
        if isinstance(failure, dict):
            parts["failure"] = failure
        log_file = AGENT_LOGS.get(agent)
        tail = _tail(trial_dir / log_file, limits.log_tail_bytes) if log_file else None
        if tail is not None:
            text, size, cut = tail
            parts["agentLog"] = {"file": log_file, "text": text, "bytes": size, "truncated": cut}
    if network is not None:
        parts["network"] = _cap_network(network, limits.flow_rows)
    meta = redactor.walk(meta)
    parts = redactor.walk(parts)
    counts = redactor.counts
    agent_log = parts.pop("agentLog", None)
    section = parts.pop("network", None)
    doc = _trial_doc(trial_id, arm, case, attempt, status, meta)
    truncations: list[dict[str, Any]] = []
    doc["meta"] = _shorten(meta, limits.string_bytes, "/meta", {}, truncations)
    for key, value in parts.items():
        doc[key] = _shorten(value, limits.string_bytes, f"/{key}", {}, truncations)
    if agent_log is not None:
        doc["agentLog"] = agent_log
    doc["truncations"] = truncations
    doc["redaction"] = {
        "version": REDACTION_VERSION,
        "exact": counts.exact,
        "pattern": counts.pattern,
        "envDump": counts.env_dump,
    }
    if section is not None:
        doc["network"] = section
    if status == "error":
        entry = _entry(doc, pass_threshold)
        entry["errorType"] = "Unreadable"
        entry["errorMessage"] = doc["trajectoryOmitted"]
        return doc, entry
    return doc, _entry(doc, pass_threshold)


def _serialize(doc: Any) -> bytes:
    """Return compact UTF-8 JSON, the form every layer holds."""
    text = json.dumps(doc, ensure_ascii=False, separators=(",", ":"), allow_nan=False)
    return text.encode("utf-8", "replace")


def _gzip(data: bytes) -> bytes:
    return gzip.compress(data, compresslevel=9, mtime=0)


def _cap_observations(doc: dict[str, Any], limit: int) -> None:
    """Cut every string of every observation to ``limit``, keeping the original sizes."""
    trajectory = doc.get("trajectory")
    if not isinstance(trajectory, dict):
        return
    originals = {t["pointer"]: t["originalBytes"] for t in doc["truncations"]}
    found: list[dict[str, Any]] = []
    for i, step in enumerate(trajectory.get("steps") or []):
        if isinstance(step, dict) and "observation" in step:
            pointer = f"/trajectory/steps/{i}/observation"
            step["observation"] = _shorten(step["observation"], limit, pointer, originals, found)
    pointers = {t["pointer"] for t in found}
    doc["truncations"] = [t for t in doc["truncations"] if t["pointer"] not in pointers] + found


def _omit_trajectory(doc: dict[str, Any], limits: Limits) -> None:
    doc.pop("trajectory", None)
    doc["trajectoryOmitted"] = (
        f"left out: the trial layer was over {limits.trial_bytes} bytes compressed"
    )
    doc["truncations"] = [
        t for t in doc["truncations"] if not t["pointer"].startswith("/trajectory/")
    ]


def _fit(doc: dict[str, Any], limits: Limits) -> tuple[dict[str, Any], bytes, bytes, int] | None:
    """Apply the trial cap in its order.

    Returns the document, its JSON, its gzip and how many cap steps applied, or None when the
    trial is still over the limit.
    """
    limit = min(limits.trial_bytes, MAX_LAYER_BYTES)
    steps: list[Callable[[dict[str, Any]], None]] = [
        lambda d: d.pop("agentLog", None),
        lambda d: _cap_observations(d, limits.output_bytes),
        lambda d: _omit_trajectory(d, limits),
    ]
    raw = _serialize(doc)
    packed = _gzip(raw)
    applied = 0
    for step in steps:
        if len(packed) <= limit:
            break
        step(doc)
        applied += 1
        raw = _serialize(doc)
        packed = _gzip(raw)
    if len(packed) > limit:
        return None
    return doc, raw, packed, applied


def _cases(snapshot: Any, redactor: Redactor) -> tuple[list[dict[str, Any]], dict[str, str]]:
    """Return the index's cases and each case's full question."""
    rows = snapshot.get("dataset") if isinstance(snapshot, Mapping) else None
    cases: list[dict[str, Any]] = []
    questions: dict[str, str] = {}
    for row in rows or []:
        if not isinstance(row, Mapping) or not isinstance(row.get("id"), str) or not row["id"]:
            continue
        if row["id"] in questions:
            continue
        question = row.get("question") if isinstance(row.get("question"), str) else None
        if question is None:
            question = row.get("prompt") if isinstance(row.get("prompt"), str) else ""
        behaviors = row.get("expected_behavior")
        if not isinstance(behaviors, list):
            behaviors = row.get("assertions") if isinstance(row.get("assertions"), list) else []
        case: dict[str, Any] = {
            "id": row["id"],
            "question": question[:CASE_QUESTION_CHARS],
            "expectedBehaviors": [b for b in behaviors if isinstance(b, str)],
        }
        if isinstance(row.get("expected_skill"), str) and row["expected_skill"]:
            case["expectedSkill"] = row["expected_skill"]
        questions[row["id"]] = question
        cases.append(case)
    cases = redactor.walk(cases)
    questions = redactor.walk(questions)
    redactor.reset()
    return cases, questions


def _network_state(network: Mapping[str, Any]) -> dict[str, Any]:
    state = network.get("state")
    out: dict[str, Any] = {"state": state if state in NETWORK_STATES else "unavailable"}
    if state not in NETWORK_STATES:
        out["reason"] = "the flow collector reported no known state"
    elif isinstance(network.get("reason"), str) and network["reason"]:
        out["reason"] = network["reason"]
    return out


def _trial_dirs(run_dir: Path, agent: str) -> list[tuple[str, Path]]:
    found = []
    for arm in ARMS:
        base = run_dir / agent / arm / "trials"
        try:
            children = sorted(p for p in base.iterdir() if p.is_dir())
        except OSError:
            continue
        found += [(arm, p) for p in children]
    return found


def _withheld(arm: str, trial_id: str, case: str, attempt: int, reason: str) -> dict[str, Any]:
    return {"id": trial_id, "arm": arm, "case": case, "attempt": attempt, "reason": reason}


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
    run-level network state from flows.py and ``trial_networks`` the per-trial sections by
    trial id. Writes ``index.json`` and one ``trial-<id>.json.gz`` per bundled trial (compact
    UTF-8 JSON, gzip), enforces every limit, runs the fail-closed scan, and returns the Bundle,
    or NotBuilt when the switch is off, there are no trials, the index itself leaks, or the
    index alone is over the bundle limit. Raises ValueError for a config the index cannot hold
    (no digest in ``SKILL_REF``, unknown agent) and RuntimeError without SkillEvaluator; the
    caller reports both as ``build-failed``.
    """
    if not enabled(environ):
        return NotBuilt(WITHHELD_DISABLED, f"{ENV_SWITCH}=off")
    agent = config.get("AGENT") or ""
    if agent not in AGENT_LOGS:
        raise ValueError(f"unknown agent {agent!r}")
    digest = DIGEST.search(config.get("SKILL_REF") or "")
    if digest is None:
        raise ValueError("SKILL_REF is not pinned to a sha256 digest")
    eval_name = config.get("SKILLEVAL_NAME") or ""
    if not eval_name or len(eval_name) > 63:
        raise ValueError("SKILLEVAL_NAME is empty or longer than 63 characters")
    skill, model = config.get("SKILL_NAME") or "", config.get("MODEL") or ""
    if not skill or not model:
        raise ValueError("SKILL_NAME and MODEL are required")
    dirs = _trial_dirs(run_dir, agent)
    if not dirs:
        return NotBuilt(WITHHELD_NO_TRIALS, f"no trial directory under {agent}/<arm>/trials")

    limits = limits_from_env(environ)
    redactor = Redactor(secret_values(environ, auth_config))
    result = _read_json(run_dir / "result.json")
    result = result if isinstance(result, Mapping) else {}
    cases, questions = _cases(_read_json(run_dir / "dataset_snapshot.json"), redactor)
    mapping = trial_map(result, agent)
    numbering = _Attempts(mapping)
    order = {case["id"]: i for i, case in enumerate(cases)}

    trials: list[tuple[tuple, str, Path, str, int]] = []
    withheld: list[tuple[tuple, dict[str, Any]]] = []
    seen: set[str] = set()
    for arm, path in dirs:
        trial_id = path.name
        if not TRIAL_ID_RE.match(trial_id):
            # A withheld entry needs a valid id too, so this trial cannot even be listed.
            log(f"leaving out a {arm} trial whose directory name is not a valid trial id")
            continue
        case, attempt = mapping.get((arm, trial_id)) or numbering.next(arm, trial_id)
        order.setdefault(case, len(order))
        key = (ARMS.index(arm), order[case], attempt, trial_id)
        if trial_id in seen:
            withheld.append((key, _withheld(arm, trial_id, case, attempt, WITHHELD_UNREADABLE)))
            continue
        seen.add(trial_id)
        trials.append((key, arm, path, case, attempt))
    trials.sort(key=lambda t: t[0])

    agent_entry = (result.get("agents") or {}).get(agent) or {}
    pass_at_k = agent_entry.get("pass_at_k") if isinstance(agent_entry, Mapping) else None
    run_state = _network_state(network)  # what a trial without its own section gets
    state = dict(run_state)  # the index's, partial once a row cap applies
    built: list[tuple[tuple, dict[str, Any], bytes]] = []
    per_arm: dict[str, int] = {}
    for key, arm, path, case, attempt in trials:
        per_arm[arm] = per_arm.get(arm, 0) + 1
        if per_arm[arm] > limits.trials_per_arm:
            withheld.append((key, _withheld(arm, path.name, case, attempt, WITHHELD_LIMIT)))
            continue
        section: Mapping[str, Any] | None = None
        if run_state["state"] != "off":
            section = (trial_networks or {}).get(path.name)
            if section is None:
                section = {**run_state, "dns": [], "flows": [], "truncated": False, "lost": 0}
        arm_detail = (
            (pass_at_k or {}).get(_arm_key(arm)) if isinstance(pass_at_k, Mapping) else None
        )
        threshold = (
            (arm_detail or {}).get("pass_threshold") if isinstance(arm_detail, Mapping) else None
        )
        threshold = threshold if _money(threshold) is not None else 0.5
        try:
            doc, entry = read_trial(
                path,
                arm=arm,
                case=case,
                attempt=attempt,
                agent=agent,
                dataset_question=questions.get(case, ""),
                limits=limits,
                redactor=redactor,
                network=section,
                pass_threshold=threshold,
            )
            fitted = _fit(doc, limits)
        except Exception as err:  # one unreadable trial never stops the bundle
            log(f"cannot read trial {path.name}: {type(err).__name__}")
            withheld.append((key, _withheld(arm, path.name, case, attempt, WITHHELD_UNREADABLE)))
            continue
        if fitted is None:
            log(f"withholding trial {path.name}: over {limits.trial_bytes} bytes compressed")
            withheld.append((key, _withheld(arm, path.name, case, attempt, WITHHELD_SIZE)))
            continue
        doc, raw, packed, applied = fitted
        if names := redactor.leaks(raw):
            log(f"withholding trial {path.name}: the scan found {', '.join(names)}")
            withheld.append((key, _withheld(arm, path.name, case, attempt, WITHHELD_SCAN)))
            continue
        # steps, tool calls and tokens describe the trial even when its trajectory was left out
        entry["bytes"] = len(packed)
        entry["rawBytes"] = len(raw)
        log_cut = bool((doc.get("agentLog") or {}).get("truncated"))
        entry["truncated"] = bool(doc["truncations"]) or log_cut or applied > 0
        if (
            section is not None
            and doc["network"].get("truncated")
            and state["state"] == "collected"
        ):
            state = {"state": "partial", "reason": "a flow or DNS row cap applied"}
        built.append((key, entry, packed))

    index: dict[str, Any] = {"schema": SCHEMA_INDEX, "skill": skill}
    version = config.get("SKILL_VERSION") or ""
    if VERSION.match(version):
        index["version"] = version
    elif version:
        log("leaving out SKILL_VERSION: not vX.Y.Z")
    index.update({"digest": digest.group(1), "eval": eval_name, "agent": agent})
    if AGENT_VERSION.match(agent_version or ""):
        index["agentVersion"] = agent_version
        entry_name = f"{agent}@{agent_version}/{model}"
    else:
        entry_name = f"{agent}/{model}"
    index.update({"model": model, "entry": entry_name})
    index["runId"] = str(result.get("run_id") or run_dir.name)
    if evaluated := _time(result.get("evaluated_at")):
        index["evaluatedAt"] = evaluated
    evaluator = {
        key: result[src]
        for key, src in (
            ("version", "evaluator_version"),
            ("metricSet", "metric_set"),
            ("scorePolicy", "score_policy"),
        )
        if isinstance(result.get(src), str)
    }
    try:
        attempts = int(config.get("ATTEMPTS") or 0)
    except ValueError:
        attempts = 0
    if attempts < 1:
        policy = (
            result.get("attempt_policy")
            if isinstance(result.get("attempt_policy"), Mapping)
            else {}
        )
        attempts = _count(policy.get("max_attempts")) or 1
    index.update(
        {
            "evaluator": evaluator,
            "attemptsPerCase": attempts,
            "redaction": REDACTION_VERSION,
            "credentials": [{"role": c.role, "env": c.env, "host": c.host} for c in credentials],
            "limits": limits.as_index(),
            "cases": cases,
        }
    )

    # The bundle cap: the last attempts go first, until index and trials fit.
    def assemble() -> bytes:
        index["trials"] = [entry for _, entry, _ in built]
        index["withheld"] = [w for _, w in sorted(withheld, key=lambda w: w[0])]
        index["network"] = state
        return _serialize(index)

    data = assemble()
    removal = sorted(built, key=lambda b: (b[0][2], b[0][1], b[0][0], b[0][3]))
    while built and len(data) + sum(len(b[2]) for b in built) > limits.bundle_bytes:
        key, entry, _ = removal.pop()
        built = [b for b in built if b[0] != key]
        log(f"withholding trial {entry['id']}: the bundle is over {limits.bundle_bytes} bytes")
        withheld.append(
            (
                key,
                _withheld(
                    entry["arm"], entry["id"], entry["case"], entry["attempt"], WITHHELD_SIZE
                ),
            )
        )
        data = assemble()
    if len(data) > min(limits.bundle_bytes, MAX_LAYER_BYTES):
        return NotBuilt(WITHHELD_SIZE, "the index alone is over the bundle limit")
    if names := redactor.leaks(data):
        log(f"withholding the bundle: the index scan found {', '.join(names)}")
        return NotBuilt(WITHHELD_SCAN, f"the index contains {', '.join(names)}")

    out.mkdir(parents=True, exist_ok=True)
    for stale in [out / INDEX_FILE, *out.glob("trial-*.json.gz")]:
        stale.unlink(missing_ok=True)
    files = [INDEX_FILE]
    for _, entry, packed in built:
        (out / entry["layer"]).write_bytes(packed)
        files.append(entry["layer"])
    (out / INDEX_FILE).write_bytes(data)
    annotations = {"skillci.dev/eval": eval_name, "skillci.dev/agent": agent}
    if "agentVersion" in index:
        annotations["skillci.dev/agent-version"] = agent_version
    annotations.update(
        {
            "skillci.dev/model": model,
            "skillci.dev/trials-schema": SCHEMA_MAJOR,
            "skillci.dev/redaction": REDACTION_VERSION,
        }
    )
    return Bundle(out, files, index, annotations, list(index["withheld"]))


def oras_files(bundle: Bundle) -> list[str]:
    """Return the ``<file>:<media type>`` arguments of ``oras attach``, index first."""
    return [
        f"{name}:{INDEX_MEDIA_TYPE if name == INDEX_FILE else TRIAL_MEDIA_TYPE}"
        for name in bundle.files
    ]


def redact_report_files(directory: Path, files: Iterable[str], redactor: Redactor) -> list[str]:
    """Apply the same redaction to the report files before they are attached.

    JSON files are redacted as parsed documents, HTML and Markdown as text. Returns the files
    that may be attached: a file that still leaks after redaction is removed from the list
    (and logged), never attached. A file that does not exist is left out too.
    """
    keep: list[str] = []
    for name in files:
        path = directory / name
        try:
            text = path.read_text(encoding="utf-8", errors="replace")
        except FileNotFoundError:
            continue
        except OSError as err:
            log(f"leaving out report file {name}: {type(err).__name__}")
            continue
        redacted = None
        if name.endswith(".json"):
            try:
                doc = json.loads(text)
            except ValueError:
                doc = None
            if doc is not None:
                clean = redactor.walk(doc)
                redacted = text if clean == doc else json.dumps(clean, ensure_ascii=False, indent=2)
        if redacted is None:
            redacted = redactor.text(text)
        data = redacted.encode("utf-8", "replace")
        if names := redactor.leaks(data):
            log(f"leaving out report file {name}: the scan found {', '.join(names)}")
            continue
        if redacted != text:
            path.write_bytes(data)
        keep.append(name)
    redactor.reset()
    return keep
