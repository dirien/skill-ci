"""Run one SkillEval: pull a skill with ORAS, score it with SkillEvaluator.

The runner Job configures this entrypoint through environment variables (see
CONTRACT.md, "Runner Job contract"). On completion it writes one JSON object to
the container termination log and exits 0, or ``{"error": ...}`` and exits 1.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import tempfile
from collections.abc import Mapping
from pathlib import Path
from typing import Any

TERMINATION_LOG = "/dev/termination-log"
MAX_MESSAGE_BYTES = 4000  # Kubernetes caps the termination message at 4096 bytes.
REPORT_TYPE = "application/vnd.skillci.report.v1+json"
REPORT_FILES = {  # file name -> media type; BENCHMARK.md is optional, the others are expected
    "report.html": "text/html",
    "result.json": "application/json",
    "BENCHMARK.md": "text/markdown",
}
SECRET_ENV = ("ANTHROPIC_API_KEY", "OPENAI_API_KEY")

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


def pull_skill(config: Mapping[str, str], dest: Path) -> Path:
    """Pull the skill artifact into ``dest`` and return the skill directory."""
    cmd = ["oras", "pull", config["SKILL_REF"], "--output", str(dest), *registry_flags(config)]
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        raise EvalError(f"oras pull failed ({proc.returncode}): {proc.stderr.strip()}")
    skill_dir = dest / config["SKILL_NAME"]
    if not skill_dir.is_dir():
        raise EvalError(f"artifact has no directory named {config['SKILL_NAME']!r}")
    return skill_dir


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


def attach_report(
    config: Mapping[str, str], run_dir: Path, scores: Mapping[str, Any]
) -> str | None:
    """Attach the report files of ``run_dir`` to the evaluated skill digest in Harbor.

    SKILL_REF is already pinned to a digest, so the report becomes an OCI referrer
    of exactly the version that was scored. Returns the report digest, or ``None``
    when attaching fails: a missing report must not fail the eval.
    """
    files = [
        f"{name}:{media}" for name, media in REPORT_FILES.items() if (run_dir / name).is_file()
    ]
    annotations = {
        "skillci.dev/model": config["MODEL"],
        "skillci.dev/agent": config["AGENT"],
        "skillci.dev/lift": f"{scores['lift']:+.2f}",
    }
    cmd = ["oras", "attach", "--artifact-type", REPORT_TYPE, *registry_flags(config)]
    for key, value in annotations.items():
        cmd += ["--annotation", f"{key}={value}"]
    cmd += [config["SKILL_REF"], *files]
    try:
        # oras rejects absolute file paths, so run it inside the report directory.
        proc = subprocess.run(cmd, cwd=run_dir, capture_output=True, text=True, check=False)
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
    """Pull the skill, run SkillEvaluator and return the termination payload."""
    config = load_config(environ)
    with tempfile.TemporaryDirectory(prefix="skill-eval-") as tmp:
        work = Path(tmp)
        skill_dir = pull_skill(config, work / "skill")
        results = work / "results"
        # Output streams straight to the container log. A non-zero exit is not
        # fatal: a failed trial can still leave scores for the other arm.
        proc = subprocess.run(evaluate_command(config, skill_dir, results), check=False)
        try:
            payload = read_scores(results, config["AGENT"])
        except EvalError as err:
            raise EvalError(f"skillevaluator exited {proc.returncode}: {err}") from err
        if report := attach_report(config, newest_result(results).parent, payload):
            payload["report"] = report
        return payload


def write_termination(payload: dict[str, Any], path: str | None = None) -> None:
    """Write ``payload`` as JSON to the termination log, within the size limit."""
    path = path or os.environ.get("TERMINATION_LOG", TERMINATION_LOG)
    text = json.dumps(payload)
    while len(text.encode()) > MAX_MESSAGE_BYTES and "error" in payload:
        payload = {"error": payload["error"][: len(payload["error"]) // 2] + "..."}
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
    print(f"skill-eval: {payload}", flush=True)
    write_termination(payload)
    return 0


if __name__ == "__main__":
    sys.exit(main())
