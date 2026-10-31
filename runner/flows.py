"""Collect the network flows of a run's trial pods from Hubble Relay, per trial.

CONTRACT.md, "Trial bundles" (network section) and "Runner Job contract" (``HUBBLE_SERVER``).
When the Job sets ``HUBBLE_SERVER`` (the SkillCIConfig ``spec.observability.hubbleRelay``),
the runner starts ``hubble observe`` in the background for the length of Tier 3:

    hubble observe --server $HUBBLE_SERVER --follow --output jsonpb \\
        --label sandbox.users.io/skillci-eval=<SKILLEVAL_NAME>

(the exact flags are those of the hubble CLI pinned in runner/Dockerfile; check them there).
Every trial pod also carries ``sandbox.users.io/skillci-trial=<trial_label(trial id)>``
(harbor-agent-sandbox), which maps a flow to its trial. Flows are summarized per trial into
the bundle's ``network`` section: DNS names asked, and FORWARDED, DROPPED, ERROR and AUDIT
connections grouped by verdict, destination (DNS name, else IP), port and protocol, with
counts and first and last times, capped at ``Limits.flow_rows`` rows each.

Flows never fail a run. No ``HUBBLE_SERVER`` gives state ``off``; a relay that cannot be
reached, or a collector that dies, gives ``unavailable`` with the reason; lost events or caps
give ``partial``.

Status: phase 0 stub. Every function raises NotImplementedError until work package 8 builds it.
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any

ENV_SERVER = "HUBBLE_SERVER"
HUBBLE_BIN = "hubble"
EVAL_LABEL = "sandbox.users.io/skillci-eval"
TRIAL_LABEL = "sandbox.users.io/skillci-trial"
STATE_COLLECTED = "collected"
STATE_PARTIAL = "partial"
STATE_UNAVAILABLE = "unavailable"
STATE_OFF = "off"
CONNECT_TIMEOUT_SEC = 10  # a relay that does not answer within this is "unavailable"
MAX_LABEL_CHARS = 63


def trial_label(trial_id: str) -> str:
    """Return the value of the ``sandbox.users.io/skillci-trial`` label for a trial id.

    The Harbor trial name (``rn-basic__sryqofs``) when it is a valid label value of at most 63
    characters; otherwise its first 52 characters, ``-`` and the first 10 hex characters of
    its SHA-256 (the rule of the controller's policy names). harbor-agent-sandbox must label
    the trial pod with exactly this value.
    """
    raise NotImplementedError


@dataclass(frozen=True)
class Window:
    """When one trial ran: ``start`` and ``end`` of its Harbor trial (result.json)."""

    start: datetime
    end: datetime


@dataclass
class Collection:
    """The outcome of a collector: ``state`` and ``reason`` for the index, the raw flow file."""

    state: str
    reason: str
    path: Path | None
    lost: int = 0


class FlowCollector:
    """Run ``hubble observe`` in the background and write its JSON lines to a file."""

    def __init__(self, server: str, eval_name: str, out: Path) -> None:
        """Prepare a collector for the trial pods of ``eval_name`` against ``server``."""
        raise NotImplementedError

    def start(self) -> None:
        """Start the CLI. A CLI that cannot start or connect is recorded, never raised."""
        raise NotImplementedError

    def stop(self) -> Collection:
        """Stop the CLI (SIGTERM, then SIGKILL after 5 s) and return what was collected."""
        raise NotImplementedError


def start_from_env(environ: Mapping[str, str], eval_name: str, work: Path) -> FlowCollector | None:
    """Return a started collector when ``HUBBLE_SERVER`` is set, else ``None`` (state off)."""
    raise NotImplementedError


def summarize(
    collection: Collection,
    trials: Mapping[str, Window],
    *,
    max_rows: int,
) -> tuple[dict[str, Any], dict[str, dict[str, Any]]]:
    """Return the index's run-level ``network`` object and one ``network`` section per trial.

    A flow belongs to the trial whose ``trial_label`` its source pod carries; DNS rows come
    from L7 DNS flows (query name without the trailing dot, rcode when seen). A trial without
    a label match gets an empty section with the run's state. Sections follow
    docs/schemas/skillci.trial.v1.json (``network``).
    """
    raise NotImplementedError
