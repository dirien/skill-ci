"""Render the skill-ci run report: one self-contained HTML page.

Inputs are what the demo scripts collect from the cluster and Harbor:

- ``kubectl get skillevals -o json`` output,
- a JSON list of ``{"skill", "digest", "tags"}`` from the Harbor API,
- a directory of SkillEvaluator reports pulled with ``demo/report.sh``
  (``<skill>-<version>-<model>/report.html`` and ``result.json``).

SkillEvaluator's own reports are inlined, so the page works offline and
inside sandboxed viewers that refuse to open sibling files.

Usage::

    python3 tools/report_page.py --skillevals se.json --tags tags.json \\
        --reports reports/ --out reports/index.html
"""

from __future__ import annotations

import argparse
import json
import re
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

TEMPLATE = Path(__file__).with_name("report_page.html")
SEMVER = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")


@dataclass(frozen=True)
class Eval:
    """One SkillEval as the page needs it."""

    skill: str
    version: str
    model: str
    required: bool
    phase: str
    with_skill: float | None
    without_skill: float | None
    lift: float | None
    verdict: str
    regressed: str
    minutes: float | None


def _num(value: str | None) -> float | None:
    return float(value) if value not in (None, "") else None


def _time(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def load_evals(doc: dict[str, Any]) -> list[Eval]:
    """Turn ``kubectl get skillevals -o json`` output into :class:`Eval` rows."""
    evals = []
    for item in doc.get("items", []):
        spec, status = item["spec"], item.get("status", {})
        regressed = next(
            (
                c.get("message", "")
                for c in status.get("conditions", [])
                if c["type"] == "Regressed" and c["status"] == "True"
            ),
            "",
        )
        minutes = None
        if status.get("completedAt"):
            elapsed = _time(status["completedAt"]) - _time(item["metadata"]["creationTimestamp"])
            minutes = round(elapsed.total_seconds() / 60, 1)
        evals.append(
            Eval(
                skill=spec["skill"],
                version=spec["version"],
                model=spec["model"],
                required=spec.get("required", True),
                phase=status.get("phase", "Pending"),
                with_skill=_num(status.get("withSkill")),
                without_skill=_num(status.get("withoutSkill")),
                lift=_num(status.get("lift")),
                verdict=status.get("verdict", ""),
                regressed=regressed,
                minutes=minutes,
            )
        )
    return evals


def version_key(version: str) -> tuple[int, ...]:
    """Sort key for ``vX.Y.Z`` tags; anything else sorts first."""
    match = SEMVER.match(version)
    return tuple(int(part) for part in match.groups()) if match else (-1,)


def channels(tags: list[dict[str, Any]]) -> dict[tuple[str, str], list[str]]:
    """Map ``(skill, version)`` to its channel tags (everything but the version tag)."""
    out: dict[tuple[str, str], list[str]] = {}
    for artifact in tags:
        versions = [t for t in artifact["tags"] if SEMVER.match(t)]
        for version in versions:
            out[(artifact["skill"], version)] = sorted(
                t for t in artifact["tags"] if not SEMVER.match(t)
            )
    return out


def decide(evals: list[Eval], tags: list[str], promote_threshold: float) -> tuple[str, str]:
    """Explain the policy outcome for one skill version as ``(kind, sentence)``.

    ``kind`` is ``good``, ``bad`` or ``wait`` and only picks the page styling.
    """
    required = [e for e in evals if e.required]
    if any(e.phase not in ("Succeeded", "Failed") for e in required):
        return "wait", "Waiting for evals to finish."
    if any(e.phase == "Failed" for e in required):
        failed = ", ".join(e.model for e in required if e.phase == "Failed")
        return "bad", f"An eval failed ({failed}), so the policy did not decide."
    if regressed := next((e.regressed for e in required if e.regressed), ""):
        return "bad", f"Held back as a regression. {regressed}."
    lifts = [(e.lift, e.model) for e in required if e.lift is not None]
    if "stable" in tags:
        lowest, model = min(lifts)
        return (
            "good",
            f"On stable. Lift is {lowest:+.2f} or more on every required model ({model} lowest).",
        )
    below = [(lift, model) for lift, model in lifts if lift < promote_threshold]
    if below:
        detail = ", ".join(f"{model} {lift:+.2f}" for lift, model in sorted(below))
        return (
            "bad",
            f"Not promoted: needs {promote_threshold:+.2f} on every required model ({detail}).",
        )
    return "good", "Meets the promotion threshold; another version holds stable."


def build_data(
    evals: list[Eval],
    tags: list[dict[str, Any]],
    reports_dir: Path | None,
    promote_threshold: float,
) -> dict[str, Any]:
    """Collect everything the page renders into one JSON-serialisable dict."""
    chan = channels(tags)
    versions = sorted(
        {(e.skill, e.version) for e in evals}, key=lambda sv: (sv[0], version_key(sv[1]))
    )
    rows = []
    for skill, version in versions:
        group = [e for e in evals if (e.skill, e.version) == (skill, version)]
        kind, sentence = decide(group, chan.get((skill, version), []), promote_threshold)
        rows.append(
            {
                "skill": skill,
                "version": version,
                "tags": chan.get((skill, version), []),
                "decision": {"kind": kind, "text": sentence},
                "evals": [e.__dict__ for e in group],
            }
        )

    reports = []
    for report in sorted(reports_dir.glob("*/report.html")) if reports_dir else []:
        result_path = report.with_name("result.json")
        dims: dict[str, list[float]] = {}
        model = ""
        if result_path.is_file():
            agents = json.loads(result_path.read_text()).get("agents", {})
            for agent in agents.values():
                model = agent.get("model", "")
                lift = agent.get("lift", {})
                dims = {
                    name: [scores["with_skill"], scores["without_skill"]]
                    for name, scores in lift.items()
                    if isinstance(scores, dict) and "with_skill" in scores and name != "overall"
                }
        reports.append(
            {"name": report.parent.name, "model": model, "dims": dims, "html": report.read_text()}
        )

    models = sorted(
        {e.model for e in evals}, key=lambda m: ("haiku" not in m, "sonnet" not in m, m)
    )
    return {
        "generated": datetime.now(UTC).strftime("%Y-%m-%d %H:%M UTC"),
        "promoteThreshold": promote_threshold,
        "models": models,
        "versions": rows,
        "reports": reports,
    }


def render(data: dict[str, Any], template: str) -> str:
    """Insert the data into the page template as an inert JSON script block."""
    payload = json.dumps(data).replace(
        "</", "<\\/"
    )  # keep inlined report HTML from closing the script tag
    return template.replace("__DATA__", payload)


def main(argv: list[str] | None = None) -> int:
    """Command-line entry point."""
    parser = argparse.ArgumentParser(description="Render the skill-ci run report as one HTML page.")
    parser.add_argument("--skillevals", type=Path, required=True)
    parser.add_argument("--tags", type=Path, required=True)
    parser.add_argument("--reports", type=Path)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--promote-threshold", type=float, default=0.10)
    args = parser.parse_args(argv)

    evals = load_evals(json.loads(args.skillevals.read_text()))
    tags = json.loads(args.tags.read_text())
    data = build_data(evals, tags, args.reports, args.promote_threshold)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(render(data, TEMPLATE.read_text()))
    print(
        f"wrote {args.out} ({len(data['versions'])} skill versions, {len(data['reports'])} reports)"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
