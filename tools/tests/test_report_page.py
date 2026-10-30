"""Tests for the run report generator."""

from __future__ import annotations

import json

import pytest
import report_page
from report_page import Eval, decide


def ev(
    model: str,
    lift: float | None,
    *,
    phase: str = "Succeeded",
    regressed: str = "",
    required: bool = True,
) -> Eval:
    """Build a SkillEval row with only the fields the policy text looks at."""
    return Eval(
        skill="acme",
        version="v1.0.0",
        model=model,
        required=required,
        phase=phase,
        with_skill=0.9,
        without_skill=None if lift is None else round(0.9 - lift, 2),
        lift=lift,
        verdict="",
        regressed=regressed,
        minutes=None,
    )


@pytest.mark.parametrize(
    ("evals", "tags", "kind", "text"),
    [
        ([ev("haiku", 0.6), ev("sonnet", None, phase="Running")], [], "wait", "Waiting"),
        (
            [ev("haiku", 0.6), ev("sonnet", None, phase="Failed")],
            ["beta"],
            "bad",
            "An eval failed (sonnet)",
        ),
        (
            [
                ev(
                    "haiku",
                    0.33,
                    regressed="haiku scores 0.69 with v1.2.0, stable v1.0.0 scored 0.95",
                ),
                ev("sonnet", 0.39),
            ],
            ["beta"],
            "bad",
            "Held back as a regression. haiku scores 0.69",
        ),
        ([ev("haiku", 0.6), ev("sonnet", 0.63)], ["beta", "stable"], "good", "+0.60 or more"),
        ([ev("haiku", 0.03), ev("sonnet", 0.43)], ["beta"], "bad", "haiku +0.03"),
        (
            [ev("haiku", 0.6), ev("sonnet", 0.02, required=False)],
            [],
            "good",
            "another version holds stable",
        ),
    ],
)
def test_decide(evals, tags, kind, text):
    """The decision sentence follows the controller's policy for each case."""
    got_kind, got_text = decide(evals, tags, promote_threshold=0.10)
    assert got_kind == kind
    assert text in got_text


def test_page_inlines_reports_safely(tmp_path):
    """Inlined report HTML cannot close the data script tag, and the page lists every version."""
    item = {
        "metadata": {"creationTimestamp": "2026-10-30T17:00:00Z"},
        "spec": {
            "skill": "acme",
            "version": "v1.1.0",
            "model": "claude-haiku-5-5",
            "required": True,
        },
        "status": {
            "phase": "Succeeded",
            "withSkill": "0.95",
            "withoutSkill": "0.35",
            "lift": "+0.60",
            "verdict": "Helps",
            "completedAt": "2026-10-30T17:12:00Z",
        },
    }
    (tmp_path / "se.json").write_text(json.dumps({"items": [item]}))
    (tmp_path / "tags.json").write_text(
        json.dumps([{"skill": "acme", "digest": "sha256:a", "tags": ["v1.1.0", "stable"]}])
    )
    report = tmp_path / "reports" / "acme-v1.1.0-claude-haiku-5-5"
    report.mkdir(parents=True)
    (report / "report.html").write_text("<html><script>x()</script></html>")
    (report / "result.json").write_text(
        json.dumps(
            {
                "agents": {
                    "claude-code": {
                        "model": "claude-haiku-5-5",
                        "lift": {"accuracy": {"with_skill": 1.0, "without_skill": 0.4}},
                    }
                }
            }
        )
    )
    out = tmp_path / "index.html"

    args = ["--skillevals", tmp_path / "se.json", "--tags", tmp_path / "tags.json"]
    args += ["--reports", tmp_path / "reports", "--out", out]
    assert report_page.main([str(a) for a in args]) == 0

    page = out.read_text()
    data_block = page.split('<script id="data" type="application/json">', 1)[1].split(
        "</script>", 1
    )[0]
    data = json.loads(data_block)
    assert data["versions"][0]["tags"] == ["stable"]
    assert data["versions"][0]["evals"][0]["minutes"] == 12.0
    assert data["reports"][0]["dims"] == {"accuracy": [1.0, 0.4]}
    assert "</script>" not in data_block
