"""Run one SkillEval: pull a skill with ORAS, check it through all SkillEvaluator tiers.

The runner Job configures this entrypoint through environment variables (see
CONTRACT.md, "Runner Job contract" and "All tiers and publication"). Tier 1
(static and security) and Tier 2 (deduplication against the other skills in
Harbor) run first as a cheap gate. Only when neither failed does the live Tier 3
evaluation run. On completion the runner writes one JSON object to the container
termination log and exits 0, or ``{"error": ...}`` and exits 1.
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
from collections.abc import Iterable, Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

TERMINATION_LOG = "/dev/termination-log"
MAX_MESSAGE_BYTES = 4000  # Kubernetes caps the termination message at 4096 bytes.
REPORT_TYPE = "application/vnd.skillci.report.v1+json"
MEDIA_TYPES = {".html": "text/html", ".json": "application/json", ".md": "text/markdown"}
# Order of the files in the report referrer. report.html and result.json come from Tier 3 when it
# ran and from the failing static tier otherwise; the tierN.* files are always the static reports.
REPORT_FILES = (
    "report.html",
    "result.json",
    "BENCHMARK.md",
    "tier1.html",
    "tier1.json",
    "tier2.html",
    "tier2.json",
)
SECRET_ENV = ("ANTHROPIC_API_KEY", "OPENAI_API_KEY", "NVIDIA_API_KEY")
STATIC_TIMEOUT_SEC = 900  # one Tier 1 or Tier 2 command, scanners and embedding calls included
RETRY_DELAYS = (2, 5, 10)  # seconds to wait before each retry of a failed oras call
MAX_FINDINGS = 5
MAX_FINDING_CHARS = 200
MAX_CATALOG_SKILLS = 200  # other skills compared against in Tier 2
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
    # Skill tasks write a few files; the 2 CPU / 4 GiB task default starves a small cluster.
    "SANDBOX_CPUS": "1",
    "SANDBOX_MEMORY_MB": "2048",
    # Trials running at once per eval; the controller caps how many evals run at once.
    "TRIAL_CONCURRENCY": "2",
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
    for name in ("ATTEMPTS", "SANDBOX_CPUS", "SANDBOX_MEMORY_MB", "TRIAL_CONCURRENCY"):
        if not config[name].isdigit() or int(config[name]) < 1:
            raise EvalError(f"{name} must be a positive integer, got {config[name]!r}")
    return config


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
    """Outcome of one static tier: its status, short findings and report files."""

    status: str  # passed | failed | incomplete
    findings: list[str] = field(default_factory=list)
    json_path: Path | None = None
    html_path: Path | None = None


def tool_env(environ: Mapping[str, str]) -> dict[str, str]:
    """Return the environment for every SkillEvaluator command (all three tiers).

    SkillEvaluator refuses to guess when several provider keys exist, so the
    chat and embeddings providers are named here. Anthropic has no embeddings
    API: Tier 2 embeds through OpenAI (or NVIDIA Build) when that key is present.
    A value already in the environment wins.
    """
    env = dict(environ)
    if not env.get("SKILL_EVAL_LLM_PROVIDER"):
        if env.get("ANTHROPIC_API_KEY"):
            env["SKILL_EVAL_LLM_PROVIDER"] = "anthropic"
        elif env.get("OPENAI_API_KEY"):
            env["SKILL_EVAL_LLM_PROVIDER"] = "openai"
    if not env.get("SKILL_EVAL_EMBEDDING_PROVIDER"):
        if env.get("OPENAI_API_KEY"):
            env["SKILL_EVAL_EMBEDDING_PROVIDER"] = "openai"
        elif env.get("NVIDIA_API_KEY"):
            env["SKILL_EVAL_EMBEDDING_PROVIDER"] = "nv_build"
    return env


def tier1_command(config: Mapping[str, str], skill_dir: Path, out: Path) -> list[str]:
    """Build the Tier 1 command: static, security and quality checks, no LLM calls.

    ``validate --tiers 1`` runs schema, version, security (SkillSpector), pii,
    license, code-integrity (Bandit, Semgrep, Gitleaks), unicode, quality and
    lint, and keeps going after the first failure so the report lists everything.
    """
    return [
        "skillevaluator", "validate", str(skill_dir),
        "--tiers", "1",
        "--no-llm",
        "--profile", config["TIER1_PROFILE"],
        "--continue-on-failure",
        "-r", "json,html",
        "-o", str(out),
    ]  # fmt: skip


def tier2_command(skill_dir: Path, out: Path, catalog: Path | None) -> list[str]:
    """Build the Tier 2 command: intra-skill dedup, plus a catalog comparison when given."""
    cmd = ["skillevaluator", "tier2", str(skill_dir)]
    if catalog is not None:
        cmd += ["--catalog", str(catalog)]
    return [*cmd, "-r", "json,html", "-o", str(out)]


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


def report_findings(report: Mapping[str, Any], tier: str) -> list[str]:
    """Extract short findings from a SkillEvaluator JSON report.

    Reads ``results[]``: every validator whose ``status`` is not ``passed`` yields
    its high and critical ``findings[]`` (rule name, message, file and line) or,
    when it has none, its blocking ``legacy.errors``. ``publication_evidence.check_id``
    names the check (``security``, ``pii``, ``similarity`` ...).
    """
    out: list[str] = []
    for result in report.get("results", []):
        if result.get("status") in ("passed", "skipped") or result.get("skipped"):
            continue
        evidence = result.get("publication_evidence") or {}
        check = evidence.get("check_id") or str(result.get("validator", "check")).lower()
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


def read_tier(
    out: Path, pattern: str, tier: str, proc: subprocess.CompletedProcess | None
) -> TierResult:
    """Turn the report a tier command wrote under ``out`` into a :class:`TierResult`."""
    json_path = newest(out, pattern)
    html_path = newest(out, pattern.replace(".json", ".html"))
    if json_path is None:
        reason = "timed out" if proc is None else (proc.stderr or proc.stdout).strip()
        last = reason.splitlines()[-1] if reason else f"exited {proc.returncode} without a report"
        return TierResult("incomplete", [finding_text(tier, "run", last)], None, html_path)
    try:
        report = json.loads(json_path.read_text())
    except (OSError, ValueError) as err:
        return TierResult("incomplete", [finding_text(tier, "run", f"unreadable report: {err}")])
    status = report.get("overall_status")
    if status not in ("passed", "failed", "incomplete"):
        status = "passed" if report.get("overall_passed") is True else "failed"
    findings = report_findings(report, tier) if status != "passed" else []
    return TierResult(status, findings, json_path, html_path)


def run_tier1(
    config: Mapping[str, str], skill_dir: Path, work: Path, env: Mapping[str, str]
) -> TierResult:
    """Run Tier 1 on the pulled skill and read its verdict."""
    out = work / "tier1"
    proc = run_static(tier1_command(config, skill_dir, out), env)
    return read_tier(out, "skillevaluator-output-*.json", "tier1", proc)


def run_tier2(
    config: Mapping[str, str], skill_dir: Path, work: Path, env: Mapping[str, str]
) -> TierResult:
    """Run Tier 2: compare the skill with itself and with the other skills in Harbor."""
    if not env.get("SKILL_EVAL_EMBEDDING_PROVIDER"):
        reason = "no embeddings provider: Anthropic has none, set OPENAI_API_KEY in llm-keys"
        return TierResult("incomplete", [finding_text("tier2", "embeddings", reason)])
    catalog_file: Path | None = None
    catalog_dir = work / "catalog" / "skills"
    others = pull_catalog(config, catalog_dir)
    if others:
        catalog_file = work / "catalog.json"
        proc = run_static(
            catalog_build_command(catalog_dir, catalog_file, work / "catalog-report"), env
        )
        if not catalog_file.is_file():
            detail = "timed out" if proc is None else (proc.stderr or proc.stdout).strip()
            last = detail.splitlines()[-1] if detail else f"exited {proc.returncode}"
            return TierResult("incomplete", [finding_text("tier2", "catalog", last)])
    else:
        print(
            "skill-eval: no other skills to compare with; Tier 2 checks the skill alone", flush=True
        )
    out = work / "tier2"
    proc = run_static(tier2_command(skill_dir, out, catalog_file), env)
    return read_tier(out, "skillevaluator-tier2.json", "tier2", proc)


def evaluate_command(config: Mapping[str, str], skill_dir: Path, results: Path) -> list[str]:
    """Build the ``skillevaluator tier3 evaluate`` command line."""
    agent = config["AGENT"]
    return [
        "skillevaluator", "tier3", "evaluate", str(skill_dir),
        "--agents", agent,
        "--env-mode", "ack",
        "--ek", f"namespace={config['SANDBOX_NAMESPACE']}",
        "--ek", f"registry={config['TASK_IMAGE_REGISTRY']}",
        "--ek", f"image_pull_secret={config['TASK_IMAGE_PULL_SECRET']}",
        "--ek", "skip_image_check=false",
        "--agent-model", f"{agent}={config['MODEL']}",
        "--n-attempts", config["ATTEMPTS"],
        "--override-cpus", config["SANDBOX_CPUS"],
        "--override-memory-mb", config["SANDBOX_MEMORY_MB"],
        "--n-concurrent", config["TRIAL_CONCURRENCY"],
        "--results-dir", str(results),
        "--progress", "plain",
    ]  # fmt: skip


def newest_result(results: Path) -> Path:
    """Return the newest ``result.json`` under ``results``."""
    candidates = sorted(results.glob("*/*/result.json"), key=lambda p: p.stat().st_mtime)
    if not candidates:
        raise EvalError("SkillEvaluator wrote no result.json")
    return candidates[-1]


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
    return {
        "withSkill": with_skill,
        "withoutSkill": without_skill,
        "lift": round(with_skill - without_skill, 2),
        "agent": agent,
        "model": entry.get("model", ""),
    }


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


def publication_status(tiers: Mapping[str, str]) -> str:
    """Combine the tier statuses: ``failed`` if any failed, ``passed`` only if all passed.

    SkillEvaluator's own assessment needs one command that covers every tier, and
    ``validate`` can neither size the sandboxes nor compare against a catalog. The
    runner therefore applies the same rule to the per-tier statuses.
    """
    values = set(tiers.values())
    if "failed" in values:
        return "failed"
    return "passed" if values == {"passed"} else "incomplete"


def build_bundle(
    bundle: Path, tier1: TierResult, tier2: TierResult, run_dir: Path | None
) -> list[str]:
    """Copy the reports of every tier that ran into ``bundle`` and return the file names.

    ``report.html`` and ``result.json`` are the Tier 3 report when Tier 3 ran; when
    it was skipped they are the report of the first tier that did not pass.
    """
    bundle.mkdir(parents=True, exist_ok=True)
    if run_dir is not None:
        for name in ("report.html", "result.json", "BENCHMARK.md"):
            if (run_dir / name).is_file():
                shutil.copy(run_dir / name, bundle / name)
    else:
        lead = next((t for t in (tier1, tier2) if t.status != "passed"), tier1)
        for src, name in ((lead.html_path, "report.html"), (lead.json_path, "result.json")):
            if src is not None:
                shutil.copy(src, bundle / name)
    for label, tier in (("tier1", tier1), ("tier2", tier2)):
        for src, ext in ((tier.html_path, ".html"), (tier.json_path, ".json")):
            if src is not None:
                shutil.copy(src, bundle / f"{label}{ext}")
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


def evaluate(environ: Mapping[str, str]) -> dict[str, Any]:
    """Pull the skill, run Tiers 1 to 3 and return the termination payload."""
    config = load_config(environ)
    with tempfile.TemporaryDirectory(prefix="skill-eval-") as tmp:
        work = Path(tmp)
        # skills/<name> is the layout SkillEvaluator expects for a skill.
        skill_dir = pull_skill(config, work / "skills")
        env = tool_env(environ)
        tier1 = run_tier1(config, skill_dir, work, env)
        tier2 = run_tier2(config, skill_dir, work, env)
        tiers = {"tier1": tier1.status, "tier2": tier2.status, "tier3": "skipped"}
        findings = tier1.findings + tier2.findings
        payload: dict[str, Any] = {"agent": config["AGENT"], "model": config["MODEL"]}
        run_dir: Path | None = None
        if "failed" in (tier1.status, tier2.status):
            print("skill-eval: Tier 1 or Tier 2 failed, skipping Tier 3", flush=True)
        else:
            results = work / "results"
            # Output streams straight to the container log. A non-zero exit is not
            # fatal: a failed trial can still leave scores for the other arm.
            cmd = evaluate_command(config, skill_dir, results)
            proc = subprocess.run(cmd, check=False, env=env)
            try:
                payload = read_scores(results, config["AGENT"])
            except EvalError as err:
                raise EvalError(f"skillevaluator exited {proc.returncode}: {err}") from err
            run_dir = newest_result(results).parent
            tier3 = tier3_tier(results)
            tiers["tier3"] = tier3.status
            findings += tier3.findings
        publication = publication_status(tiers)
        annotations = {"skillci.dev/model": config["MODEL"], "skillci.dev/agent": config["AGENT"]}
        if "lift" in payload:
            annotations["skillci.dev/lift"] = f"{payload['lift']:+.2f}"
        annotations["skillci.dev/publication"] = publication
        bundle = work / "bundle"
        files = build_bundle(bundle, tier1, tier2, run_dir)
        if report := attach_report(config, bundle, files, annotations):
            payload["report"] = report
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
