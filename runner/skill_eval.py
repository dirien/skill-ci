"""Run one SkillEval: pull a skill with ORAS, check it through all SkillEvaluator tiers.

The runner Job configures this entrypoint through environment variables (see
CONTRACT.md, "Runner Job contract" and "All tiers and publication"). A first
``skillevaluator validate --tiers 1,2`` (static and security checks, deduplication
against the other skills in Harbor) is the cheap gate. Only when neither tier
failed does a second ``skillevaluator validate`` run all three tiers, including
the live Tier 3 evaluation, in one invocation: its report then carries
SkillEvaluator's own publication assessment. On completion the runner writes one
JSON object to the container termination log and exits 0, or ``{"error": ...}``
and exits 1.
"""

from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
from collections import Counter
from collections.abc import Iterable, Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

TERMINATION_LOG = "/dev/termination-log"
MAX_MESSAGE_BYTES = 4000  # Kubernetes caps the termination message at 4096 bytes.
REPORT_TYPE = "application/vnd.skillci.report.v1+json"
MEDIA_TYPES = {".html": "text/html", ".json": "application/json", ".md": "text/markdown"}
# Order of the files in the report referrer. report.html and BENCHMARK.md are the combined
# SkillEvaluator report of the last ``validate`` run (it holds the publication assessment);
# result.json is the Tier 3 result when Tier 3 ran. validate.json (the combined JSON report)
# and tier3.html (the Tier 3 report) are attached only when Tier 3 ran.
REPORT_FILES = ("report.html", "result.json", "BENCHMARK.md", "validate.json", "tier3.html")
SECRET_ENV = (
    "ANTHROPIC_API_KEY",
    "OPENAI_API_KEY",
    "NVIDIA_API_KEY",
    "OLLAMA_API_KEY",
    "OPENROUTER_API_KEY",
)
# The agent CLI release each agent runs when the Job names none (AGENT_VERSION). claude-code and
# codex are the pins SkillEvaluator 0.6.0 ships (tier3/harbor/runner.py: _CLAUDE_CODE_VERSION,
# _CODEX_VERSION; a test compares them). Upstream pins no OpenCode release, so Harbor would
# install the latest one per trial; 1.19.1 (npm opencode-ai, GitHub anomalyco/opencode v1.19.1,
# published 2026-10-15) is the newest release that was at least two weeks old on 2026-10-30.
DEFAULT_AGENT_VERSIONS = {"claude-code": "2.1.292", "codex": "0.161.0", "opencode": "1.19.1"}
# A version reaches `npm install -g pkg@<version>` in every sandbox: letters, digits, dot, dash,
# underscore only, which is also a valid Kubernetes label value.
AGENT_VERSION_PATTERN = re.compile(r"^[A-Za-z0-9]([A-Za-z0-9._-]{0,61}[A-Za-z0-9])?$")
OPENAI_DEFAULT_BASE_URL = "https://api.openai.com/v1"
OPENAI_EMBEDDING_MODEL = "text-embedding-3-small"
# OpenCode providers served through an OpenAI-compatible Chat Completions endpoint: the model
# `<provider>/<model id>` runs with the key from llm-keys and the base URL below (patch hunks 8-11).
GATEWAY_PROVIDERS = {
    "ollama-cloud": ("OLLAMA_API_KEY", "https://ollama.com/v1"),
    "openrouter": ("OPENROUTER_API_KEY", "https://openrouter.ai/api/v1"),
    "nvidia": ("NVIDIA_API_KEY", "https://integrate.api.nvidia.com/v1"),
    "openai": ("OPENAI_API_KEY", OPENAI_DEFAULT_BASE_URL),
}
# The Tier 1 + 2 gate or one catalog build, scanners and embedding calls included.
STATIC_TIMEOUT_SEC = 1200
RETRY_DELAYS = (2, 5, 10)  # seconds to wait before each retry of a failed oras call
MAX_FINDINGS = 5
MAX_FINDING_CHARS = 200
MAX_CATALOG_SKILLS = 200  # other skills compared against in Tier 2
MAX_SAMPLES = 50  # per-trial scores kept per arm; 3 cases x 3 attempts give 9
SEMVER_TAG = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")
SEVERITY_RANK = {"critical": 0, "high": 1, "medium": 2, "low": 3}

# Defaults mirror the runner image; the Job can override any of them.
DEFAULTS = {
    "ATTEMPTS": "1",
    "REGISTRY_CONFIG": "/auth/config.json",
    "REGISTRY_PLAIN_HTTP": "true",
    "SANDBOX_NAMESPACE": "skill-ci",
    "TASK_IMAGE_REGISTRY": "registry.digitalocean.com/skill-ci-dirien",
    "TASK_IMAGE_PULL_SECRET": "docr",
    # Skill tasks write a few files; the 2 CPU / 4 GiB task default starves a small cluster. The
    # size reaches the sandbox as HARBOR_SANDBOX_CPUS / HARBOR_SANDBOX_MEMORY_MB (tool_env).
    "SANDBOX_CPUS": "1",
    "SANDBOX_MEMORY_MB": "2048",
    # Trials running at once per eval; the controller caps how many evals run at once.
    "TRIAL_CONCURRENCY": "2",
    # Every trial installs the agent CLI in its sandbox; on a busy cluster that
    # outlasts Harbor's 360 s agent setup timeout.
    "TIMEOUT_MULTIPLIER": "2",
    # SkillEvaluator validation profile for Tier 1. See runner/README or DEMO.md "Profile".
    "TIER1_PROFILE": "external",
}
REQUIRED = ("SKILL_REF", "SKILL_NAME", "AGENT", "MODEL")


class EvalError(Exception):
    """A failure that ends the run with an ``{"error": ...}`` termination message."""


def load_config(environ: Mapping[str, str]) -> dict[str, str]:
    """Return the runner configuration from the environment, applying defaults."""
    missing = [name for name in REQUIRED if not environ.get(name)]
    if missing:
        raise EvalError(f"missing required env: {', '.join(missing)}")
    config = {name: environ[name] for name in REQUIRED}
    for name, default in DEFAULTS.items():
        config[name] = environ.get(name) or default
    config["AGENT_VERSION"] = agent_version(environ)
    if config["AGENT_VERSION"] and not AGENT_VERSION_PATTERN.match(config["AGENT_VERSION"]):
        raise EvalError(
            f"AGENT_VERSION must be a release like 2.1.292, got {config['AGENT_VERSION']!r}"
        )
    config["AGENT_MODEL"] = agent_route(config["AGENT"], config["MODEL"], environ).model
    for name in ("ATTEMPTS", "SANDBOX_CPUS", "SANDBOX_MEMORY_MB", "TRIAL_CONCURRENCY"):
        if not config[name].isdigit() or int(config[name]) < 1:
            raise EvalError(f"{name} must be a positive integer, got {config[name]!r}")
    try:
        multiplier = float(config["TIMEOUT_MULTIPLIER"])
    except ValueError:
        multiplier = 0.0
    if multiplier <= 0:
        raise EvalError(
            f"TIMEOUT_MULTIPLIER must be a positive number, got {config['TIMEOUT_MULTIPLIER']!r}"
        )
    return config


def agent_version(environ: Mapping[str, str]) -> str:
    """Return the agent CLI release to run: ``AGENT_VERSION``, else the default pin of ``AGENT``."""
    return (environ.get("AGENT_VERSION") or "").strip() or DEFAULT_AGENT_VERSIONS.get(
        environ.get("AGENT", ""), ""
    )


@dataclass(frozen=True)
class AgentRoute:
    """How the agent under test reaches its model.

    ``model`` is the value for ``--agent-model``; ``env`` the variables that replace the Job's
    for this run. ``gateway`` is true for OpenCode behind an OpenAI-compatible endpoint.
    """

    model: str
    env: Mapping[str, str] = field(default_factory=dict)
    gateway: bool = False


def agent_route(agent: str, model: str, environ: Mapping[str, str]) -> AgentRoute:
    """Choose the credential route of ``agent`` running ``model``.

    Claude Code and Codex use the keys of the Job as they are (Codex needs ``OPENAI_API_KEY``
    and ``OPENAI_BASE_URL``). OpenCode models are ``<provider>/<model>``: ``anthropic/...`` runs
    natively with the Anthropic key; a provider from ``GATEWAY_PROVIDERS`` runs through its
    OpenAI-compatible endpoint, with that provider's key presented as ``OPENAI_API_KEY`` to the
    agent only. The judge and Tier 2 keep their own providers (see ``tool_env``).
    """
    if agent != "opencode":
        return AgentRoute(model)
    provider, _, model_id = model.partition("/")
    if not model_id:
        raise EvalError(f"opencode needs a <provider>/<model> model, got {model!r}")
    if provider == "anthropic":
        return AgentRoute(model)
    if provider not in GATEWAY_PROVIDERS:
        known = ", ".join(["anthropic", *GATEWAY_PROVIDERS])
        raise EvalError(f"opencode provider {provider!r} is not supported, use one of: {known}")
    key_env, base_url = GATEWAY_PROVIDERS[provider]
    key = environ.get(key_env, "").strip()
    if not key:
        raise EvalError(f"opencode model {model!r} needs {key_env} (llm-keys) in the Job")
    if provider == "openai":
        base_url = environ.get("OPENAI_BASE_URL") or base_url
    return AgentRoute(
        f"openai/{model_id}",
        {
            "OPENAI_API_KEY": key,
            "OPENAI_BASE_URL": base_url,
            "SKILLEVALUATOR_OPENCODE_GATEWAY": "1",
        },
        gateway=True,
    )


def registry_flags(config: Mapping[str, str]) -> list[str]:
    """Return the oras flags that select the registry credentials and transport."""
    flags = []
    if Path(config["REGISTRY_CONFIG"]).is_file():
        flags += ["--registry-config", config["REGISTRY_CONFIG"]]
    if config["REGISTRY_PLAIN_HTTP"].lower() == "true":
        flags.append("--plain-http")
    return flags


def run_oras(cmd: list[str], **kwargs: Any) -> subprocess.CompletedProcess:
    """Run an oras command, retrying up to three times after a non-zero exit.

    Harbor sits behind an nginx that now and then answers 502; a short backoff
    (see ``RETRY_DELAYS``) rides that out. Returns the last result.
    """
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False, **kwargs)
    for delay in RETRY_DELAYS:
        if proc.returncode == 0:
            break
        print(
            f"skill-eval: oras {cmd[1]} failed ({proc.returncode}), retrying in {delay}s",
            file=sys.stderr,
        )
        time.sleep(delay)
        proc = subprocess.run(cmd, capture_output=True, text=True, check=False, **kwargs)
    return proc


def pull_skill(config: Mapping[str, str], dest: Path) -> Path:
    """Pull the skill artifact into ``dest`` and return the skill directory."""
    cmd = ["oras", "pull", config["SKILL_REF"], "--output", str(dest), *registry_flags(config)]
    proc = run_oras(cmd)
    if proc.returncode != 0:
        raise EvalError(f"oras pull failed ({proc.returncode}): {proc.stderr.strip()}")
    skill_dir = dest / config["SKILL_NAME"]
    if not skill_dir.is_dir():
        raise EvalError(f"artifact has no directory named {config['SKILL_NAME']!r}")
    return skill_dir


@dataclass
class TierResult:
    """Outcome of one tier: its status (passed | failed | incomplete) and short findings."""

    status: str
    findings: list[str] = field(default_factory=list)


@dataclass
class ValidateReport:
    """What one ``skillevaluator validate`` run reported.

    ``tiers`` holds the Tier 1 and Tier 2 statuses; ``publication`` is SkillEvaluator's own
    publication assessment mapped to ``passed | failed | incomplete``.
    """

    tiers: dict[str, str]
    findings: list[str]
    publication: str
    json_path: Path | None = None
    html_path: Path | None = None
    benchmark_path: Path | None = None


def tool_env(environ: Mapping[str, str]) -> dict[str, str]:
    """Return the environment for every SkillEvaluator command (all three tiers).

    SkillEvaluator refuses to guess when several provider keys exist, so the
    chat and embeddings providers are named here. Anthropic has no embeddings
    API: Tier 2 embeds through OpenAI (or NVIDIA Build) when that key is present.
    A value already in the environment wins. The sandbox size travels the same way:
    ``validate`` has no ``--override-cpus``, so the harbor_agent_sandbox environment reads
    ``HARBOR_SANDBOX_CPUS`` and ``HARBOR_SANDBOX_MEMORY_MB`` (patch hunk 4 lets them through).

    The judge stays on Anthropic whatever agent runs: ``SKILL_EVAL_LLM_PROVIDER`` is
    ``anthropic`` whenever the Job has an Anthropic key. Codex needs ``OPENAI_API_KEY`` together
    with ``OPENAI_BASE_URL`` (the OpenAI endpoint unless the Job says otherwise). OpenCode behind
    a gateway (``agent_route``) gets the gateway key as ``OPENAI_API_KEY``, so Tier 2 embeddings
    keep the real OpenAI key through ``SKILL_EVAL_EMBEDDING_*``. The requested agent release
    reaches SkillEvaluator as ``SKILLEVALUATOR_AGENT_VERSION`` (patch hunk 7).
    """
    env = dict(environ)
    route = (
        agent_route(env.get("AGENT", ""), env.get("MODEL", ""), env) if env.get("AGENT") else None
    )
    if not env.get("SKILL_EVAL_EMBEDDING_PROVIDER"):
        # Chosen from the Job's own keys, before a gateway key takes the OPENAI_API_KEY name.
        if env.get("OPENAI_API_KEY"):
            env["SKILL_EVAL_EMBEDDING_PROVIDER"] = "openai"
            if route and route.gateway:
                env["SKILL_EVAL_EMBEDDING_PROVIDER"] = "openai-compatible"
                env["SKILL_EVAL_EMBEDDING_API_KEY"] = env["OPENAI_API_KEY"]
                env["SKILL_EVAL_EMBEDDING_BASE_URL"] = (
                    env.get("OPENAI_BASE_URL") or OPENAI_DEFAULT_BASE_URL
                )
                env["SKILL_EVAL_EMBEDDING_MODEL"] = OPENAI_EMBEDDING_MODEL
        elif env.get("NVIDIA_API_KEY"):
            env["SKILL_EVAL_EMBEDDING_PROVIDER"] = "nv_build"
    if route and route.gateway:
        env.update(route.env)
    elif env.get("OPENAI_API_KEY") and not env.get("OPENAI_BASE_URL"):
        env["OPENAI_BASE_URL"] = OPENAI_DEFAULT_BASE_URL
    if version := agent_version(env):
        env["SKILLEVALUATOR_AGENT_VERSION"] = version
    for name, source in (
        ("HARBOR_SANDBOX_CPUS", "SANDBOX_CPUS"),
        ("HARBOR_SANDBOX_MEMORY_MB", "SANDBOX_MEMORY_MB"),
    ):
        if not env.get(name):
            env[name] = env.get(source) or DEFAULTS[source]
    if not env.get("SKILL_EVAL_LLM_PROVIDER"):
        if env.get("ANTHROPIC_API_KEY"):
            env["SKILL_EVAL_LLM_PROVIDER"] = "anthropic"
        elif environ.get("OPENAI_API_KEY"):
            env["SKILL_EVAL_LLM_PROVIDER"] = "openai"
    return env


def validate_command(
    config: Mapping[str, str],
    skill_dir: Path,
    out: Path,
    *,
    tiers: str,
    catalog: Path | None = None,
    results: Path | None = None,
) -> list[str]:
    """Build a ``skillevaluator validate`` command line.

    ``tiers`` is ``1,2`` for the gate (static, security, quality, lint and deduplication; no
    LLM calls in Tier 1, no sandboxes) and ``1,2,3`` for the full run that adds the live Tier 3
    evaluation, with ``results`` as the Harbor results directory. ``--no-autopilot`` keeps the
    skill's own ``evals/`` and never generates a dataset, exactly as ``tier3 evaluate`` did.
    ``catalog`` makes Tier 2 compare the skill with the other skills in Harbor
    (patch hunks 1 to 3). ``--continue-on-failure`` keeps the reports complete.
    """
    cmd = [
        "skillevaluator", "validate", str(skill_dir),
        "--tiers", tiers,
        "--no-llm",
        "--profile", config["TIER1_PROFILE"],
        "--continue-on-failure",
        "-r", "json,html",
        "-o", str(out),
    ]  # fmt: skip
    if catalog is not None:
        cmd += ["--catalog", str(catalog)]
    if "3" in tiers.split(","):
        if results is None:
            raise ValueError("a run with Tier 3 needs a results directory")
        cmd += [
            "--no-autopilot",
            "--agents", config["AGENT"],
            "--env-mode", "ack",
            "--ek", f"namespace={config['SANDBOX_NAMESPACE']}",
            "--ek", f"registry={config['TASK_IMAGE_REGISTRY']}",
            "--ek", f"image_pull_secret={config['TASK_IMAGE_PULL_SECRET']}",
            "--ek", "skip_image_check=false",
            "--agent-model", f"{config['AGENT']}={config['AGENT_MODEL']}",
            "--n-attempts", config["ATTEMPTS"],
            "--n-concurrent", config["TRIAL_CONCURRENCY"],
            "--timeout-multiplier", config["TIMEOUT_MULTIPLIER"],
            "--results-dir", str(results),
        ]  # fmt: skip
    return cmd


def catalog_build_command(catalog_dir: Path, catalog: Path, out: Path) -> list[str]:
    """Build the command that embeds the other skills into a Tier 2 catalog file."""
    return [
        "skillevaluator", "similarity-check", str(catalog_dir),
        "--type", "skill",
        "--save-catalog", str(catalog),
        "-r", "json",
        "-o", str(out),
    ]  # fmt: skip


def semver_key(tag: str) -> tuple[int, int, int] | None:
    """Return the sort key of a ``vX.Y.Z`` tag, or ``None`` for any other tag."""
    found = SEMVER_TAG.match(tag)
    return (int(found[1]), int(found[2]), int(found[3])) if found else None


def pick_tag(tags: Iterable[str]) -> str | None:
    """Choose the version of a skill to compare against: ``stable``, else the newest ``vX.Y.Z``."""
    tags = list(tags)
    if "stable" in tags:
        return "stable"
    versions = [(key, tag) for tag in tags if (key := semver_key(tag))]
    return max(versions)[1] if versions else None


def oras_lines(cmd: list[str]) -> list[str] | None:
    """Run an oras listing command and return its output lines, or ``None`` on failure."""
    proc = run_oras(cmd)
    if proc.returncode != 0:
        print(f"skill-eval: {' '.join(cmd[:3])} failed: {proc.stderr.strip()}", file=sys.stderr)
        return None
    return [line.strip() for line in proc.stdout.splitlines() if line.strip()]


def catalog_repositories(config: Mapping[str, str]) -> list[str]:
    """List the other skill repositories in the Harbor project of ``SKILL_REF``.

    Returns an empty list when the registry cannot list repositories: Tier 2 then
    compares the skill only with itself and the log says so.
    """
    host, project, *_ = [*config["SKILL_REF"].split("/"), "", ""]
    lines = oras_lines(["oras", "repo", "ls", f"{host}/{project}", *registry_flags(config)])
    if lines is None:
        return []
    # Harbor lists the repositories of the namespace relative to it ("acme-release-notes");
    # other registries may return "skills/acme-release-notes". Accept both.
    names = {
        line.removeprefix(f"{project}/")
        for line in lines
        if "/" not in line.removeprefix(f"{project}/")
    }
    names.discard(config["SKILL_NAME"])
    return sorted(names)[:MAX_CATALOG_SKILLS]


def pull_catalog(config: Mapping[str, str], dest: Path) -> list[str]:
    """Pull every other skill (``stable``, else newest ``vX.Y.Z``) into ``dest``.

    Returns the names of the skills that are now in ``dest``; skills that cannot
    be listed or pulled are skipped with a log line.
    """
    host, project, *_ = [*config["SKILL_REF"].split("/"), "", ""]
    pulled = []
    for name in catalog_repositories(config):
        repo = f"{host}/{project}/{name}"
        tags = oras_lines(["oras", "repo", "tags", repo, *registry_flags(config)])
        if not tags or not (tag := pick_tag(tags)):
            print(f"skill-eval: catalog: no stable or vX.Y.Z tag for {name}", file=sys.stderr)
            continue
        cmd = ["oras", "pull", f"{repo}:{tag}", "--output", str(dest), *registry_flags(config)]
        proc = run_oras(cmd)
        if proc.returncode != 0 or not (dest / name / "SKILL.md").is_file():
            print(f"skill-eval: catalog: cannot pull {name}:{tag}", file=sys.stderr)
            continue
        pulled.append(name)
    return pulled


def run_static(cmd: list[str], env: Mapping[str, str]) -> subprocess.CompletedProcess | None:
    """Run a Tier 1 or Tier 2 command, echo its output to the log, return ``None`` on timeout."""
    try:
        proc = subprocess.run(
            cmd,
            env=dict(env),
            capture_output=True,
            text=True,
            check=False,
            timeout=STATIC_TIMEOUT_SEC,
        )
    except subprocess.TimeoutExpired:
        print(f"skill-eval: {cmd[1]} timed out after {STATIC_TIMEOUT_SEC}s", file=sys.stderr)
        return None
    print(redact(proc.stdout), flush=True)
    print(redact(proc.stderr), file=sys.stderr, flush=True)
    return proc


def newest(directory: Path, pattern: str) -> Path | None:
    """Return the newest file matching ``pattern`` under ``directory``, if any."""
    found = sorted(directory.glob(pattern), key=lambda p: p.stat().st_mtime)
    return found[-1] if found else None


def finding_text(tier: str, check: str, message: str) -> str:
    """Format one finding as ``tier1/security: message`` within the length limit."""
    message = re.sub(r"^\[[A-Z0-9_ -]+\]\s*", "", " ".join(message.split()))
    text = f"{tier}/{check}: {message}"
    return text if len(text) <= MAX_FINDING_CHARS else text[: MAX_FINDING_CHARS - 3] + "..."


def finding_message(finding: Mapping[str, Any]) -> str:
    """Format a report finding as ``rule: message (file:line)``."""
    where = finding.get("file_path")
    if where and finding.get("line_number"):
        where = f"{where}:{finding['line_number']}"
    text = ": ".join(
        str(part) for part in (finding.get("check_name"), finding.get("message")) if part
    )
    return f"{text} ({where})" if where else text


def result_tier(result: Mapping[str, Any]) -> int | None:
    """Return the tier (1, 2 or 3) a ``results[]`` entry belongs to, if the report says."""
    tier = (result.get("gating") or {}).get("tier")
    if tier is None:
        tier = (result.get("publication_evidence") or {}).get("tier")
    return tier if isinstance(tier, int) else None


def report_findings(report: Mapping[str, Any], tier: str) -> list[str]:
    """Extract short findings for ``tier`` (``tier1`` or ``tier2``) from a SkillEvaluator report.

    Reads ``results[]``: every validator of that tier whose ``status`` is not ``passed``
    yields its high and critical ``findings[]`` (rule name, message, file and line) or, when
    it has none, its blocking ``legacy.errors``. ``publication_evidence.check_id`` names the
    check (``security``, ``pii``, ``similarity`` ...). A skipped validator names its skip
    reason instead, because a skipped required check leaves the tier incomplete.
    """
    out: list[str] = []
    for result in report.get("results", []):
        if f"tier{result_tier(result)}" != tier:
            continue
        skipped = result.get("status") == "skipped" or result.get("skipped")
        if result.get("status") == "passed":
            continue
        evidence = result.get("publication_evidence") or {}
        check = evidence.get("check_id") or str(result.get("validator", "check")).lower()
        if skipped:
            reason = str(result.get("skip_reason") or "skipped")
            out.append(finding_text(tier, check, reason.splitlines()[0]))
            continue
        serious = [
            f
            for f in result.get("findings", [])
            if SEVERITY_RANK.get(str(f.get("severity")).lower(), 9) <= SEVERITY_RANK["high"]
        ]
        serious.sort(key=lambda f: SEVERITY_RANK.get(str(f.get("severity")).lower(), 9))
        messages = [finding_message(f) for f in serious] or list(
            result.get("legacy", {}).get("errors", [])
        )
        if not messages:
            scans = ", ".join(result.get("incomplete_scans", []))
            messages = [f"{result.get('status', 'not passed')}{f' ({scans})' if scans else ''}"]
        out += [finding_text(tier, check, m) for m in messages]
    return out


def tier_status(report: Mapping[str, Any], tier: int) -> str:
    """Return ``passed``, ``failed`` or ``incomplete`` for one tier of a ``validate`` report.

    Failed beats incomplete. A tier with no results, or whose results were all skipped
    (Tier 2 without an embeddings provider), is incomplete: SkillEvaluator requires it.
    """
    results = [r for r in report.get("results", []) if result_tier(r) == tier]
    statuses = {r.get("status") for r in results}
    if "failed" in statuses:
        return "failed"
    if (
        not results
        or "incomplete" in statuses
        or any(r.get("incomplete_scans") for r in results)
        or statuses <= {"skipped"}
    ):
        return "incomplete"
    return "passed"


def publication_of(report: Mapping[str, Any]) -> str:
    """Map SkillEvaluator's publication assessment to ``passed``, ``failed`` or ``incomplete``.

    The report's ``publication.status`` is ``pass``, ``fail``, ``incomplete`` or ``neutral``
    (Tier 3 ran, but the skill does not clearly beat the baseline). ``neutral`` maps to
    ``passed``: the evidence is complete and nothing failed; the lift is the controller's
    business (verdict ``Neutral``). Anything unrecognised is ``incomplete``.
    """
    publication = report.get("publication")
    status = publication.get("status") if isinstance(publication, dict) else None
    status = status or report.get("publication_status")
    return {"pass": "passed", "neutral": "passed", "fail": "failed"}.get(str(status), "incomplete")


def read_validate(out: Path, proc: subprocess.CompletedProcess | None) -> ValidateReport:
    """Turn the report a ``validate`` run wrote under ``out`` into a :class:`ValidateReport`."""
    json_path = newest(out, "skillevaluator-output-*.json")
    html_path = newest(out, "skillevaluator-output-*.html")
    benchmark = out / "BENCHMARK.md"
    benchmark_path = benchmark if benchmark.is_file() else None
    incomplete = {"tier1": "incomplete", "tier2": "incomplete"}
    if json_path is None:
        reason = "timed out" if proc is None else (proc.stderr or proc.stdout).strip()
        last = reason.splitlines()[-1] if reason else f"exited {proc.returncode} without a report"
        finding = finding_text("tier1", "run", last)
        return ValidateReport(incomplete, [finding], "incomplete", None, html_path, benchmark_path)
    try:
        report = json.loads(json_path.read_text())
    except (OSError, ValueError) as err:
        finding = finding_text("tier1", "run", f"unreadable report: {err}")
        return ValidateReport(incomplete, [finding], "incomplete", None, html_path, benchmark_path)
    tiers = {f"tier{n}": tier_status(report, n) for n in (1, 2)}
    findings = [
        finding
        for tier, status in tiers.items()
        if status != "passed"
        for finding in (report_findings(report, tier) or [finding_text(tier, "run", status)])
    ]
    return ValidateReport(
        tiers, findings, publication_of(report), json_path, html_path, benchmark_path
    )


def prepare_catalog(
    config: Mapping[str, str], work: Path, env: Mapping[str, str]
) -> tuple[bool, Path | None, list[str]]:
    """Prepare the Tier 2 comparison catalog and say whether Tier 2 can run.

    Returns ``(tier2, catalog, findings)``. ``tier2`` is false when Tier 2 cannot produce
    trustworthy evidence (no embeddings provider, or a catalog that cannot be built); the
    runs then leave Tier 2 out, SkillEvaluator calls the publication incomplete, and the
    finding says why. ``catalog`` is ``None`` when there are no other skills to compare with.
    """
    if not env.get("SKILL_EVAL_EMBEDDING_PROVIDER"):
        reason = "no embeddings provider: Anthropic has none, set OPENAI_API_KEY in llm-keys"
        return False, None, [finding_text("tier2", "embeddings", reason)]
    catalog_dir = work / "catalog" / "skills"
    if not pull_catalog(config, catalog_dir):
        print(
            "skill-eval: no other skills to compare with; Tier 2 checks the skill alone", flush=True
        )
        return True, None, []
    catalog_file = work / "catalog.json"
    proc = run_static(
        catalog_build_command(catalog_dir, catalog_file, work / "catalog-report"), env
    )
    if not catalog_file.is_file():
        detail = "timed out" if proc is None else (proc.stderr or proc.stdout).strip()
        last = detail.splitlines()[-1] if detail else f"exited {proc.returncode}"
        return False, None, [finding_text("tier2", "catalog", last)]
    return True, catalog_file, []


def newest_result(results: Path) -> Path:
    """Return the newest ``result.json`` under ``results``."""
    candidates = sorted(results.glob("*/*/result.json"), key=lambda p: p.stat().st_mtime)
    if not candidates:
        raise EvalError("SkillEvaluator wrote no result.json")
    return candidates[-1]


def trial_scores(entry: Mapping[str, Any], arm: str) -> list[float]:
    """Return the per-trial overall scores of one arm (``with_skill`` or ``without_skill``).

    SkillEvaluator lists every attempt of every case under
    ``pass_at_k.<arm>.cases.<case>.attempts[].score``; the arm's overall score is their mean.
    Scores are rounded to two decimals and capped at ``MAX_SAMPLES``. An older result without
    that block gives an empty list, and the controller then uses its fixed-number rules.
    """
    cases = ((entry.get("pass_at_k") or {}).get(arm) or {}).get("cases") or {}
    scores = [
        round(float(attempt["score"]), 2)
        for case in cases.values()
        for attempt in case.get("attempts") or []
        if isinstance(attempt.get("score"), int | float)
    ]
    return scores[:MAX_SAMPLES]


def read_scores(results: Path, agent: str) -> dict[str, Any]:
    """Extract the scores for ``agent`` from the newest ``result.json`` under ``results``."""
    path = newest_result(results)
    try:
        result = json.loads(path.read_text())
    except (OSError, ValueError) as err:
        raise EvalError(f"cannot read {path.name}: {err}") from err
    entry = result.get("agents", {}).get(agent)
    if entry is None:
        raise EvalError(f"result.json has no entry for agent {agent!r}")
    with_skill = entry.get("overall_with_skill")
    without_skill = entry.get("overall_without_skill")
    if not all(isinstance(v, int | float) for v in (with_skill, without_skill)):
        errors = "; ".join(map(str, result.get("execution_errors", []))) or result.get("error")
        raise EvalError(f"no scores in result.json: {errors or 'unknown reason'}")
    with_skill, without_skill = round(with_skill, 2), round(without_skill, 2)
    payload: dict[str, Any] = {
        "withSkill": with_skill,
        "withoutSkill": without_skill,
        "lift": round(with_skill - without_skill, 2),
        "agent": agent,
        "model": entry.get("model", ""),
    }
    samples = {"withSkill": trial_scores(entry, "with_skill")}
    samples["withoutSkill"] = trial_scores(entry, "without_skill")
    if all(samples.values()):
        payload["samples"] = samples
    return payload


def used_agent_version(run_dir: Path) -> str | None:
    """Return the agent CLI release the trials ran, from Harbor's trial results.

    Every trial's ``result.json`` under ``_harbor-jobs/<job>/<trial>/`` carries
    ``agent_info.version``: the release Harbor installed (the requested pin), or the one it
    detected afterwards when no pin was given. The most common valid value wins; ``None`` when
    the jobs were not kept or no trial reports a version.
    """
    seen = Counter()
    for path in sorted(run_dir.glob("_harbor-jobs/*/*/result.json")):
        try:
            info = json.loads(path.read_text()).get("agent_info") or {}
        except (OSError, ValueError, AttributeError):
            continue
        version = str(info.get("version") or "").strip()
        if version != "unknown" and AGENT_VERSION_PATTERN.match(version):  # Harbor's "no idea"
            seen[version] += 1
    return seen.most_common(1)[0][0] if seen else None


def tier3_tier(results: Path) -> TierResult:
    """Return the Tier 3 status: ``passed`` when the run succeeded, else ``incomplete``.

    ``result.json`` carries ``execution_status`` (``succeeded`` when every trial
    ran). Scores that are low are still a valid result; the controller reads them
    as a verdict, not as a failed tier.
    """
    result = json.loads(newest_result(results).read_text())
    if result.get("execution_status", "succeeded") == "succeeded":
        return TierResult("passed")
    errors = "; ".join(map(str, result.get("execution_errors", []))) or result["execution_status"]
    return TierResult("incomplete", [finding_text("tier3", "execution", errors)])


def build_bundle(bundle: Path, report: ValidateReport, run_dir: Path | None) -> list[str]:
    """Copy the reports of the last ``validate`` run into ``bundle``; return the file names.

    ``report.html`` and ``BENCHMARK.md`` are the combined SkillEvaluator report (with its
    publication assessment) of that run. With Tier 3, ``result.json`` is the Tier 3 result the
    controller reads its dimension scores from, and ``validate.json`` and ``tier3.html`` are
    the combined JSON and the Tier 3 report; without it ``result.json`` is the combined JSON,
    so a Rejected run shows the failing checks.
    """
    bundle.mkdir(parents=True, exist_ok=True)
    sources: dict[str, Path | None] = {
        "report.html": report.html_path,
        "result.json": report.json_path,
        "BENCHMARK.md": report.benchmark_path,
    }
    if run_dir is not None:
        sources |= {
            "report.html": report.html_path or run_dir / "report.html",
            "result.json": run_dir / "result.json",
            "validate.json": report.json_path,
            "tier3.html": run_dir / "report.html",
        }
    for name, src in sources.items():
        if src is not None and src.is_file():
            shutil.copy(src, bundle / name)
    return [name for name in REPORT_FILES if (bundle / name).is_file()]


def attach_report(
    config: Mapping[str, str], bundle: Path, files: list[str], annotations: Mapping[str, str]
) -> str | None:
    """Attach the report files in ``bundle`` to the evaluated skill digest in Harbor.

    SKILL_REF is already pinned to a digest, so the report becomes an OCI referrer
    of exactly the version that was checked. Returns the report digest, or ``None``
    when attaching fails: a missing report must not fail the eval.
    """
    if not files:
        return None
    cmd = ["oras", "attach", "--artifact-type", REPORT_TYPE, *registry_flags(config)]
    for key, value in annotations.items():
        cmd += ["--annotation", f"{key}={value}"]
    cmd += [config["SKILL_REF"], *(f"{n}:{MEDIA_TYPES[Path(n).suffix]}" for n in files)]
    try:
        # oras rejects absolute file paths, so run it inside the report directory.
        proc = run_oras(cmd, cwd=bundle)
    except OSError as err:
        print(f"skill-eval: report not attached: {err}", file=sys.stderr)
        return None
    found = re.search(r"Digest:\s*(sha256:[0-9a-f]{64})", proc.stdout + proc.stderr)
    if proc.returncode != 0 or not found:
        detail = (proc.stderr or proc.stdout).strip()
        print(f"skill-eval: report not attached ({proc.returncode}): {detail}", file=sys.stderr)
        return None
    return found.group(1)


def final_publication(report: ValidateReport, tiers: Mapping[str, str]) -> str:
    """Return the ``publication`` of the termination message.

    It is SkillEvaluator's assessment, with two guards in the safe direction: a failed tier
    is a failure (SkillEvaluator reports ``incomplete`` first when something else is missing),
    and ``passed`` needs every tier to have passed.
    """
    if "failed" in tiers.values():
        return "failed"
    if report.publication == "passed" and set(tiers.values()) != {"passed"}:
        return "incomplete"
    return report.publication


def evaluate(environ: Mapping[str, str]) -> dict[str, Any]:
    """Pull the skill, run Tiers 1 to 3 and return the termination payload."""
    config = load_config(environ)
    with tempfile.TemporaryDirectory(prefix="skill-eval-") as tmp:
        work = Path(tmp)
        # skills/<name> is the layout SkillEvaluator expects for a skill.
        skill_dir = pull_skill(config, work / "skills")
        env = tool_env(environ)
        tier2, catalog, notes = prepare_catalog(config, work, env)
        static = "1,2" if tier2 else "1"
        # The gate: Tier 1 and 2 only, so a Rejected skill costs no sandbox and no tokens.
        out = work / "validate-static"
        cmd = validate_command(config, skill_dir, out, tiers=static, catalog=catalog)
        report = read_validate(out, run_static(cmd, env))
        payload: dict[str, Any] = {"agent": config["AGENT"], "model": config["MODEL"]}
        agent_ver = config[
            "AGENT_VERSION"
        ]  # what was asked for; replaced by what the trials report
        run_dir: Path | None = None
        tiers = {**report.tiers, "tier3": "skipped"}
        findings = report.findings
        if "failed" in (report.tiers["tier1"], report.tiers["tier2"]):
            print("skill-eval: Tier 1 or Tier 2 failed, skipping Tier 3", flush=True)
        else:
            # One validate run over every tier: its report carries SkillEvaluator's own
            # publication assessment. Output streams straight to the container log. A non-zero
            # exit is not fatal: a failed trial can still leave scores for the other arm.
            results = work / "results"
            out = work / "validate"
            cmd = validate_command(
                config, skill_dir, out, tiers=f"{static},3", catalog=catalog, results=results
            )
            proc = subprocess.run(cmd, check=False, env=env)
            try:
                payload = read_scores(results, config["AGENT"])
            except EvalError as err:
                raise EvalError(f"skillevaluator exited {proc.returncode}: {err}") from err
            run_dir = newest_result(results).parent
            payload["model"] = config["MODEL"]  # not the gateway form SkillEvaluator ran with
            agent_ver = used_agent_version(run_dir) or agent_ver
            full = read_validate(out, proc)
            if full.json_path is None:  # Tier 3 left scores but no combined report
                full = ValidateReport(report.tiers, full.findings, "incomplete", None, None, None)
            report = full
            tier3 = tier3_tier(results)
            tiers = {**report.tiers, "tier3": tier3.status}
            findings = report.findings + tier3.findings
        if not tier2:
            findings = notes + [f for f in findings if not f.startswith("tier2/")]
        publication = final_publication(report, tiers)
        annotations = {"skillci.dev/model": config["MODEL"], "skillci.dev/agent": config["AGENT"]}
        if agent_ver:
            annotations["skillci.dev/agent-version"] = agent_ver
            payload["agentVersion"] = agent_ver
        if "lift" in payload:
            annotations["skillci.dev/lift"] = f"{payload['lift']:+.2f}"
        annotations["skillci.dev/publication"] = publication
        bundle = work / "bundle"
        files = build_bundle(bundle, report, run_dir)
        if digest := attach_report(config, bundle, files, annotations):
            payload["report"] = digest
        payload |= {"tiers": tiers, "publication": publication, "findings": findings[:MAX_FINDINGS]}
        return payload


def write_termination(payload: dict[str, Any], path: str | None = None) -> None:
    """Write ``payload`` as JSON to the termination log, within the size limit.

    An oversized message loses findings first, then the tail of the last finding;
    an oversized error is cut in half until it fits.
    """
    path = path or os.environ.get("TERMINATION_LOG", TERMINATION_LOG)
    text = json.dumps(payload)
    while len(text.encode()) > MAX_MESSAGE_BYTES and "error" in payload:
        payload = {"error": payload["error"][: len(payload["error"]) // 2] + "..."}
        text = json.dumps(payload)
    while len(text.encode()) > MAX_MESSAGE_BYTES and payload.get("findings"):
        payload = {**payload, "findings": payload["findings"][:-1]}
        text = json.dumps(payload)
    Path(path).write_text(text)


def redact(text: str) -> str:
    """Remove API key values from ``text`` so they never reach logs or the message."""
    for name in SECRET_ENV:
        if value := os.environ.get(name):
            text = text.replace(value, "[redacted]")
    return text


def main() -> int:
    """Entrypoint: returns the process exit code."""
    try:
        payload = evaluate(os.environ)
    except Exception as err:
        message = redact(str(err))
        print(f"skill-eval: {message}", file=sys.stderr)
        write_termination({"error": message})
        return 1
    payload["findings"] = [redact(f) for f in payload.get("findings", [])]
    print(f"skill-eval: {payload}", flush=True)
    write_termination(payload)
    return 0


if __name__ == "__main__":
    sys.exit(main())
