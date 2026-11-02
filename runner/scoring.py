"""The trial score skill-ci gates on: SkillEvaluator's dimensions without Discoverability.

SkillEvaluator 0.6.0 scores a trial as the mean of five dimensions (score policy
``skill-evaluator-dimension-mean-v1``). One of them, Discoverability (metric
``skill_execution``), asks whether the agent loaded the skill. Without a skill installed it is
0 by definition, so it adds up to +0.20 to every lift on its own, whether the skill helps or
not. skill-ci leaves it out: a trial's score is the mean of the other four dimensions,
Security, Correctness, Effectiveness and Efficiency, with SkillEvaluator's weights. CONTRACT.md,
"Statistics", is the specification; the reporting site computes its Overall row the same way.
"""

from __future__ import annotations

import math
from collections.abc import Mapping
from typing import Any

SCORE_POLICY = "skill-ci-task-mean-v1"

# Dimension -> {metric: weight}, as SkillEvaluator's DIMENSION_MAPPING without discoverability.
DIMENSIONS: dict[str, dict[str, float]] = {
    "security": {"security": 1.0},
    "correctness": {"accuracy": 1.0},
    "effectiveness": {"goal_accuracy": 0.5, "behavior_check": 0.5},
    "efficiency": {"skill_efficiency": 1.0},
}


def _metric(value: Any) -> float | None:
    if isinstance(value, bool) or not isinstance(value, int | float):
        return None
    value = float(value)
    return value if math.isfinite(value) and 0.0 <= value <= 1.0 else None


def task_score(metrics: Mapping[str, Any]) -> float | None:
    """Return the mean of the four dimensions from ``metrics``, rounded to four decimals.

    ``metrics`` holds SkillEvaluator's metric scores, from a trial's ``reward.json`` or an arm's
    means in ``result.json``. ``None`` when a metric is missing or outside 0 to 1, as for a
    custom metric set; the caller then keeps SkillEvaluator's own overall score.
    """
    dimensions = []
    for sources in DIMENSIONS.values():
        weighted = 0.0
        for name, weight in sources.items():
            value = _metric(metrics.get(name))
            if value is None:
                return None
            weighted += value * weight
        dimensions.append(weighted / sum(sources.values()))
    return round(sum(dimensions) / len(dimensions), 4)
