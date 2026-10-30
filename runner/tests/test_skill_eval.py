"""Tests for the runner entrypoint; subprocess calls are replaced by fakes."""

from __future__ import annotations

import json
import subprocess
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
    ):
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

    def oras(self, cmd: list[str], kwargs: dict[str, Any]) -> subprocess.CompletedProcess:
        """Fake the oras sub-commands the runner uses."""
        if cmd[1] == "attach":
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
    for key in ("SKILL_EVAL_LLM_PROVIDER", "SKILL_EVAL_EMBEDDING_PROVIDER", "NVIDIA_API_KEY"):
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
    """main() writes the contract's termination message and exit code."""
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == code, name
    written = termination()
    if code == 0:
        assert written == expected
    else:
        assert expected in written["error"]
        assert set(written) == {"error"}


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


def test_rejected_run_attaches_the_failing_report(termination, monkeypatch):
    """A skipped Tier 3 still attaches the combined report of the gate run."""
    fake = FakeRun(GOOD, gate=CURL_BASH)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    assert fake.bundle == ["BENCHMARK.md", "report.html", "result.json"]
    attach = fake.calls[-1]
    assert "skillci.dev/publication=failed" in attach
    assert not any(a.startswith("skillci.dev/lift") for a in attach)


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
    eks = [evaluator[i + 1] for i, arg in enumerate(evaluator) if arg == "--ek"]
    assert eks == [
        "namespace=skill-ci",
        "registry=registry.digitalocean.com/skill-ci-dirien",
        "image_pull_secret=docr",
        "skip_image_check=false",
    ]


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
    assert fake.attach_cwd.name == "bundle"
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
    assert sum(c[:2] == ["oras", "attach"] for c in fake.calls) == 2
    assert sleeps == [2]


def test_attach_gives_up_after_three_retries(termination, monkeypatch, sleeps):
    """A permanent attach error costs four calls and never fails the eval."""
    fake = FakeRun(GOOD, attach_exit=1)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    assert "report" not in termination()
    assert sum(c[:2] == ["oras", "attach"] for c in fake.calls) == 4
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
