"""Tests for the trial score without Discoverability (``scoring.py``)."""

from __future__ import annotations

import pytest

import scoring

FULL = {
    "security": 1.0,
    "skill_execution": 1.0,
    "skill_efficiency": 0.75,
    "accuracy": 1.0,
    "goal_accuracy": 1.0,
    "behavior_check": 1.0,
}


@pytest.mark.parametrize(
    ("metrics", "expected"),
    [
        (FULL, 0.9375),
        ({**FULL, "skill_execution": 0.0}, 0.9375),  # Discoverability does not count
        ({**FULL, "goal_accuracy": 0.0}, 0.8125),  # Effectiveness is the mean of two metrics
        (
            {
                "security": 1.0,
                "skill_execution": 0.0,
                "skill_efficiency": 0.25,
                "accuracy": 0.4,
                "goal_accuracy": 0.05,
                "behavior_check": 0.25,
            },
            0.45,
        ),
        ({k: 0 for k in FULL}, 0.0),
    ],
    ids=["with skill", "no discoverability", "effectiveness", "without skill", "zeros"],
)
def test_task_score(metrics, expected):
    assert scoring.task_score(metrics) == expected


@pytest.mark.parametrize(
    "metrics",
    [
        {k: v for k, v in FULL.items() if k != "security"},
        {**FULL, "accuracy": None},
        {**FULL, "accuracy": True},
        {**FULL, "accuracy": 1.5},
        {**FULL, "accuracy": float("nan")},
        {"overall": 0.9},  # a custom metric set
    ],
    ids=["missing", "null", "bool", "above 1", "nan", "custom"],
)
def test_task_score_needs_every_metric(metrics):
    assert scoring.task_score(metrics) is None
