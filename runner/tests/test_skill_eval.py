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


GOOD = {"agents": {"claude-code": agent_entry(0.951, 0.364)}}
REPORT = "sha256:" + "c" * 64
GOOD_PAYLOAD = {
    "withSkill": 0.95,
    "withoutSkill": 0.36,
    "lift": 0.59,
    "agent": "claude-code",
    "model": "claude-haiku-5-5",
    "report": REPORT,
}
NO_REPORT = {k: v for k, v in GOOD_PAYLOAD.items() if k != "report"}


class FakeRun:
    """Stands in for subprocess.run: fakes oras and skillevaluator."""

    def __init__(
        self,
        result: Any,
        eval_exit: int = 0,
        pull_exit: int = 0,
        skill_dir: bool = True,
        attach_exit: int = 0,
    ):
        self.result = result
        self.eval_exit = eval_exit
        self.pull_exit = pull_exit
        self.skill_dir = skill_dir
        self.attach_exit = attach_exit
        self.calls: list[list[str]] = []
        self.attach_cwd: Path | None = None

    def __call__(self, cmd: list[str], **kwargs: Any) -> subprocess.CompletedProcess:
        self.calls.append(cmd)
        if cmd[:2] == ["oras", "attach"]:
            self.attach_cwd = kwargs.get("cwd")
            if self.attach_exit:
                return subprocess.CompletedProcess(cmd, self.attach_exit, "", "unauthorized")
            out = f"Attached to [registry] {ENV['SKILL_REF']}\nDigest: {REPORT}\n"
            return subprocess.CompletedProcess(cmd, 0, out, "")
        if cmd[0] == "oras":
            dest = Path(cmd[cmd.index("--output") + 1])
            if self.skill_dir and self.pull_exit == 0:
                (dest / ENV["SKILL_NAME"]).mkdir(parents=True)
            return subprocess.CompletedProcess(
                cmd, self.pull_exit, "", "denied" if self.pull_exit else ""
            )
        if self.result is not None:
            run_dir = Path(cmd[cmd.index("--results-dir") + 1]) / "acme" / "20260101_000000"
            run_dir.mkdir(parents=True)
            text = self.result if isinstance(self.result, str) else json.dumps(self.result)
            (run_dir / "result.json").write_text(text)
            (run_dir / "report.html").write_text("<html></html>")
        return subprocess.CompletedProcess(cmd, self.eval_exit)


@pytest.fixture
def termination(tmp_path, monkeypatch):
    """Point the termination log at a temp file and return a reader for it."""
    path = tmp_path / "termination-log"
    monkeypatch.setenv("TERMINATION_LOG", str(path))
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


def test_commands(termination, monkeypatch):
    """oras and skillevaluator get the arguments the contract promises."""
    fake = FakeRun(GOOD)
    monkeypatch.setattr(skill_eval.subprocess, "run", fake)
    assert skill_eval.main() == 0
    oras, evaluator, _ = fake.calls
    assert oras[:3] == ["oras", "pull", ENV["SKILL_REF"]]
    assert "--plain-http" in oras
    assert evaluator[:3] == ["skillevaluator", "tier3", "evaluate"]
    assert evaluator[3].endswith("/skill/acme")
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
    """The report is attached to the evaluated digest as a typed referrer."""
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
    ]
    subject = attach.index(ENV["SKILL_REF"])
    assert attach[subject + 1 :] == ["report.html:text/html", "result.json:application/json"]
    assert fake.attach_cwd is not None
    assert fake.attach_cwd.name == "20260101_000000"


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
