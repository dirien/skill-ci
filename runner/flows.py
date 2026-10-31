"""Collect the network flows of a run's trial pods from Hubble Relay, per trial.

CONTRACT.md, "Trial bundles" (network section) and "Runner Job contract" (``HUBBLE_SERVER``).
When the Job sets ``HUBBLE_SERVER`` (the SkillCIConfig ``spec.observability.hubbleRelay``),
the runner first asks the relay for its status (``hubble status``, 10 s), then starts
``hubble observe`` in the background for the length of Tier 3:

    hubble observe --server $HUBBLE_SERVER --timeout 10s --follow --output jsonpb \\
        --label sandbox.users.io/skillci-eval=<SKILLEVAL_NAME>

These are the flags of the hubble CLI pinned in runner/Dockerfile (v1.20.2): ``--server``,
``--timeout`` (dial timeout), ``-f/--follow``, ``-o/--output jsonpb`` and ``-l/--label``, which
matches flows whose source or destination endpoint carries the label. ``jsonpb`` writes one
``GetFlowsResponse`` per line on stdout (``flow`` or ``lost_events``) in proto3 JSON with proto
field names; node errors (``node_status``) go to stderr.

Every trial pod also carries ``sandbox.users.io/skillci-trial=<trial_label(trial id)>``
(harbor-agent-sandbox, which owns :func:`trial_label`), which maps a flow to its trial.
Flows are summarized per trial into the bundle's ``network`` section: DNS names asked, and
FORWARDED, DROPPED, ERROR and AUDIT connections grouped by verdict, destination (DNS name,
else IP), port, protocol and drop reason, with counts (distinct connections, so retransmits
and the several events Cilium reports per connection count once) and first and last times,
capped at ``max_rows`` rows each.

Flows never fail a run. No ``HUBBLE_SERVER`` gives state ``off``; a relay that does not
answer within 10 s, or a CLI that fails before it delivered a flow, gives ``unavailable``
with the reason; lost events, unavailable nodes, a CLI that died after delivering flows, or
caps give ``partial``.
"""

from __future__ import annotations

import ipaddress
import json
import logging
import os
import shutil
import signal
import subprocess
from collections.abc import Iterator, Mapping
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path
from typing import IO, Any

logger = logging.getLogger(__name__)

ENV_SERVER = "HUBBLE_SERVER"
HUBBLE_BIN = "hubble"
EVAL_LABEL = "sandbox.users.io/skillci-eval"
TRIAL_LABEL = "sandbox.users.io/skillci-trial"
STATE_COLLECTED = "collected"
STATE_PARTIAL = "partial"
STATE_UNAVAILABLE = "unavailable"
STATE_OFF = "off"
CONNECT_TIMEOUT_SEC = 10  # a relay that does not answer within this is "unavailable"
STOP_GRACE_SEC = 5  # SIGTERM, then SIGKILL after this
MAX_LABEL_CHARS = 63
FLOWS_FILE = "hubble-flows.jsonl"
OFF_REASON = "no Hubble Relay configured (SkillCIConfig spec.observability.hubbleRelay is empty)"
MAX_REASON_CHARS = 300

VERDICTS = ("FORWARDED", "DROPPED", "ERROR", "AUDIT")
# Rows a cap keeps first: what a reader looks for is what was refused.
_VERDICT_RANK = {"DROPPED": 0, "ERROR": 1, "AUDIT": 2, "FORWARDED": 3}
PROTOCOLS = ("TCP", "UDP", "ICMPv4", "ICMPv6", "SCTP")
DNS_RCODES = {
    0: "NOERROR",
    1: "FORMERR",
    2: "SERVFAIL",
    3: "NXDOMAIN",
    4: "NOTIMP",
    5: "REFUSED",
    6: "YXDOMAIN",
    7: "YXRRSET",
    8: "NXRRSET",
    9: "NOTAUTH",
    10: "NOTZONE",
}
_KUBE_DNS_LABEL = "k8s-app=kube-dns"
_DNS_PORT = 53


def trial_label(trial_id: str) -> str:
    """Return the value of the ``sandbox.users.io/skillci-trial`` label for a trial id.

    The Harbor trial name (``rn-basic__sryqofs``) when it is a valid label value of at most 63
    characters; otherwise its first 52 characters, ``-`` and the first 10 hex characters of
    its SHA-256 (the rule of the controller's policy names). harbor-agent-sandbox labels the
    trial pod with this value and owns the implementation; it is imported when first needed,
    so a runner without that package can still import this module.
    """
    from harbor_agent_sandbox.environment import trial_label as label

    return label(trial_id)


@dataclass(frozen=True)
class Window:
    """When one trial ran: ``start`` and ``end`` of its Harbor trial (result.json)."""

    start: datetime
    end: datetime


@dataclass
class Collection:
    """The outcome of a collector: ``state`` and ``reason`` for the index, the raw flow file.

    ``lost`` is the number of events Hubble reported lost; ``lost_events`` keeps when each
    loss was reported (``None`` when Hubble gave no time), so a trial only counts the losses
    of its own window.
    """

    state: str
    reason: str
    path: Path | None
    lost: int = 0
    lost_events: list[tuple[datetime | None, int]] = field(default_factory=list)


class FlowCollector:
    """Run ``hubble observe`` in the background and write its JSON lines to a file."""

    def __init__(self, server: str, eval_name: str, out: Path) -> None:
        """Prepare a collector for the trial pods of ``eval_name`` against ``server``."""
        self.server = server.strip()
        self.eval_name = eval_name.strip()
        self.out = out
        self.err = out.with_name(out.name + ".stderr")
        self._proc: subprocess.Popen[bytes] | None = None
        self._stdout: IO[bytes] | None = None
        self._stderr: IO[bytes] | None = None
        self._failed: Collection | None = None
        self._stopped: Collection | None = None

    def _unavailable(self, reason: str) -> None:
        logger.warning("network flows unavailable: %s", reason)
        self._failed = Collection(STATE_UNAVAILABLE, _clip(reason), None)

    def _env(self) -> dict[str, str]:
        """Return a minimal environment for the CLI: no credentials, no ``HUBBLE_*`` settings."""
        env = {"PATH": os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin")}
        env["HOME"] = os.environ.get("HOME", "/tmp")  # noqa: S108
        return env

    def _command(self) -> list[str]:
        return [
            self._bin(),
            "observe",
            "--server",
            self.server,
            "--timeout",
            f"{CONNECT_TIMEOUT_SEC}s",
            "--follow",
            "--output",
            "jsonpb",
            "--label",
            f"{EVAL_LABEL}={self.eval_name}",
        ]

    @staticmethod
    def _bin() -> str:
        return shutil.which(HUBBLE_BIN) or HUBBLE_BIN

    def _probe(self) -> str | None:
        """Ask the relay for its status; return why it is unusable, or None when it answered."""
        command = [
            self._bin(),
            "status",
            "--server",
            self.server,
            "--timeout",
            f"{CONNECT_TIMEOUT_SEC}s",
            "--request-timeout",
            f"{CONNECT_TIMEOUT_SEC}s",
        ]
        try:
            result = subprocess.run(  # noqa: S603
                command,
                capture_output=True,
                timeout=CONNECT_TIMEOUT_SEC + 5,
                env=self._env(),
                check=False,
            )
        except subprocess.TimeoutExpired:
            return self._no_answer()
        except OSError as e:
            return f"hubble CLI cannot run: {e}"
        if result.returncode == 0:
            return None
        message = _last_line(result.stderr) or _last_line(result.stdout)
        if "DeadlineExceeded" in message or "deadline exceeded" in message:
            return self._no_answer()
        return f"hubble status against {self.server} failed (exit {result.returncode}): {message}"

    def _no_answer(self) -> str:
        return f"Hubble Relay {self.server} did not answer within {CONNECT_TIMEOUT_SEC}s"

    def start(self) -> None:
        """Start the CLI. A CLI that cannot start or connect is recorded, never raised."""
        try:
            self._start()
        except Exception as e:  # flows never fail a run
            self._close_files()
            self._unavailable(f"hubble observe could not start: {e}")

    def _start(self) -> None:
        if not self.server:
            self._unavailable("HUBBLE_SERVER is empty")
            return
        if not self.eval_name or len(self.eval_name) > MAX_LABEL_CHARS:
            self._unavailable(f"SkillEval name {self.eval_name!r} is not a label value")
            return
        if shutil.which(HUBBLE_BIN) is None:
            self._unavailable(f"the {HUBBLE_BIN} CLI is not on PATH")
            return
        if reason := self._probe():
            self._unavailable(reason)
            return
        self.out.parent.mkdir(parents=True, exist_ok=True)
        self._stdout = self.out.open("wb")
        self._stderr = self.err.open("wb")
        self._proc = subprocess.Popen(  # noqa: S603
            self._command(),
            stdin=subprocess.DEVNULL,
            stdout=self._stdout,
            stderr=self._stderr,
            env=self._env(),
        )
        logger.info("collecting network flows of %s from %s", self.eval_name, self.server)

    def _close_files(self) -> None:
        for handle in (self._stdout, self._stderr):
            if handle is not None:
                handle.close()
        self._stdout = self._stderr = None

    def stop(self) -> Collection:
        """Stop the CLI (SIGTERM, then SIGKILL after 5 s) and return what was collected."""
        if self._stopped is not None:
            return self._stopped
        try:
            self._stopped = self._stop()
        except Exception as e:  # flows never fail a run
            self._stopped = Collection(
                STATE_UNAVAILABLE, _clip(f"collecting flows failed: {e}"), None
            )
        return self._stopped

    def _stop(self) -> Collection:
        if self._failed is not None:
            return self._failed
        if self._proc is None:
            return Collection(STATE_UNAVAILABLE, "hubble observe was never started", None)
        exited = self._proc.poll()
        if exited is None:
            self._proc.send_signal(signal.SIGTERM)
            try:
                self._proc.wait(timeout=STOP_GRACE_SEC)
            except subprocess.TimeoutExpired:
                self._proc.kill()
                self._proc.wait()
        self._close_files()
        flows, lost_events = _scan(self.out)
        lost = sum(n for _, n in lost_events)
        nodes = _node_errors(self.err)
        if exited is not None:
            message = _last_line(self.err.read_bytes()) if self.err.exists() else ""
            failure = f"hubble observe exited early (exit {exited})"
            if message:
                failure += f": {message}"
            if flows == 0:
                return Collection(STATE_UNAVAILABLE, _clip(failure), self.out, lost, lost_events)
            return Collection(STATE_PARTIAL, _clip(failure), self.out, lost, lost_events)
        reasons = []
        if lost:
            reasons.append(f"Hubble reported {lost} lost events")
        if nodes:
            reasons.append("Hubble nodes unavailable: " + ", ".join(nodes))
        if reasons:
            return Collection(STATE_PARTIAL, _clip("; ".join(reasons)), self.out, lost, lost_events)
        return Collection(STATE_COLLECTED, "", self.out)


def start_from_env(environ: Mapping[str, str], eval_name: str, work: Path) -> FlowCollector | None:
    """Return a started collector when ``HUBBLE_SERVER`` is set, else ``None`` (state off)."""
    server = (environ.get(ENV_SERVER) or "").strip()
    if not server:
        return None
    collector = FlowCollector(server, eval_name, work / FLOWS_FILE)
    collector.start()
    return collector


def summarize(
    collection: Collection | None,
    trials: Mapping[str, Window],
    *,
    max_rows: int,
) -> tuple[dict[str, Any], dict[str, dict[str, Any]]]:
    """Return the index's run-level ``network`` object and one ``network`` section per trial.

    A flow belongs to the trial whose ``trial_label`` its source pod carries (DNS answers and
    other replies: its destination pod); DNS rows come from L7 DNS flows (query name without
    the trailing dot, rcode of the last answer). Connection rows leave out replies, L7 flows
    and the DNS traffic to kube-dns that the DNS rows already show. A trial without a label
    match gets an empty section with the run's state. Sections follow
    docs/schemas/skillci.trial.v1.json (``network``). ``None`` (no collector) is state ``off``:
    the index says why and no trial gets a section.
    """
    if collection is None or collection.state == STATE_OFF:
        reason = collection.reason if collection is not None and collection.reason else OFF_REASON
        return {"state": STATE_OFF, "reason": reason}, {}
    try:
        return _summarize(collection, trials, max(0, max_rows))
    except Exception as e:  # flows never fail a run
        logger.warning("summarizing network flows failed: %s", e)
        reason = _clip(f"summarizing flows failed: {e}")
        return {"state": STATE_UNAVAILABLE, "reason": reason}, {
            trial: _empty(STATE_UNAVAILABLE, reason) for trial in trials
        }


def _summarize(
    collection: Collection, trials: Mapping[str, Window], max_rows: int
) -> tuple[dict[str, Any], dict[str, dict[str, Any]]]:
    if collection.state == STATE_UNAVAILABLE and collection.path is None:
        run = {"state": STATE_UNAVAILABLE, "reason": collection.reason}
        return run, {trial: _empty(STATE_UNAVAILABLE, collection.reason) for trial in trials}

    by_label = {trial_label(trial): trial for trial in trials}
    acc = {trial: _Trial() for trial in trials}
    if collection.path is not None and collection.path.exists():
        for response in _responses(collection.path):
            flow = response.get("flow")
            if isinstance(flow, dict):
                _add_flow(flow, by_label, acc)

    capped = 0
    sections: dict[str, dict[str, Any]] = {}
    for trial, window in trials.items():
        start, end = _aware(window.start), _aware(window.end)
        lost = sum(n for at, n in collection.lost_events if at is None or start <= at <= end)
        dns, dns_cut = _cap(acc[trial].dns_rows(), max_rows, _dns_rank)
        flows, flows_cut = _cap(acc[trial].flow_rows(), max_rows, _flow_rank)
        truncated = dns_cut or flows_cut
        capped += truncated
        state, reason = collection.state, collection.reason
        if state == STATE_COLLECTED and (truncated or lost):
            state = STATE_PARTIAL
        if truncated:
            reason = _join(reason, f"rows capped at {max_rows}")
        if lost and state == STATE_PARTIAL and "lost events" not in reason:
            reason = _join(reason, f"Hubble reported {lost} lost events")
        section: dict[str, Any] = {"state": state}
        if reason:
            section["reason"] = _clip(reason)
        if state in (STATE_COLLECTED, STATE_PARTIAL) or dns or flows:
            section["source"] = "hubble"
            section["from"] = _fmt(window.start)
            section["to"] = _fmt(window.end)
        section.update({"dns": dns, "flows": flows, "truncated": truncated, "lost": lost})
        sections[trial] = section

    run: dict[str, Any] = {"state": collection.state}
    reason = collection.reason
    if capped and collection.state == STATE_COLLECTED:
        run["state"] = STATE_PARTIAL
    if capped:
        reason = _join(reason, f"rows capped at {max_rows} for {capped} trial(s)")
    if reason:
        run["reason"] = _clip(reason)
    return run, sections


@dataclass
class _Row:
    first: datetime
    last: datetime
    ip: str
    keys: set[tuple[Any, ...]] = field(default_factory=set)

    def see(self, at: datetime, key: tuple[Any, ...]) -> None:
        self.first, self.last = min(self.first, at), max(self.last, at)
        self.keys.add(key)


@dataclass
class _Dns:
    first: datetime
    last: datetime
    requests: int = 0
    responses: int = 0
    rcode: str | None = None


@dataclass
class _Trial:
    flows: dict[tuple[str, str, int, str, str], _Row] = field(default_factory=dict)
    dns: dict[str, _Dns] = field(default_factory=dict)

    def flow_rows(self) -> list[dict[str, Any]]:
        rows = []
        for (verdict, destination, port, protocol, reason), row in self.flows.items():
            out: dict[str, Any] = {
                "verdict": verdict,
                "destination": destination,
                "ip": row.ip,
                "port": port,
                "protocol": protocol,
                "count": max(1, len(row.keys)),
                "firstAt": _fmt(row.first),
                "lastAt": _fmt(row.last),
            }
            if reason:
                out["reason"] = reason
            rows.append(out)
        return rows

    def dns_rows(self) -> list[dict[str, Any]]:
        rows = []
        for name, entry in self.dns.items():
            if _search_expansion(name, entry, self.dns):
                continue
            out: dict[str, Any] = {
                "name": name,
                "count": max(1, entry.requests or entry.responses),
                "firstAt": _fmt(entry.first),
                "lastAt": _fmt(entry.last),
            }
            if entry.rcode:
                out["rcode"] = entry.rcode
            rows.append(out)
        return rows


def _add_flow(flow: dict[str, Any], by_label: Mapping[str, str], acc: dict[str, _Trial]) -> None:
    at = _parse_time(flow.get("time"))
    if at is None:
        return
    source = _endpoint(flow, "source")
    destination = _endpoint(flow, "destination")
    reply = flow.get("is_reply") is True
    l7 = flow.get("l7")
    dns = l7.get("dns") if isinstance(l7, dict) else None
    if isinstance(dns, dict):
        response = l7.get("type") == "RESPONSE"
        trial = _trial_of(destination if response else source, by_label)
        if trial is None:
            # A proxy answer can come back without the reply side set; try the other end.
            trial = _trial_of(source if response else destination, by_label)
        if trial is not None:
            _add_dns(acc[trial], dns, at, response)
        return
    if reply or flow.get("Type") == "L7" or l7:
        return
    trial = _trial_of(source, by_label)
    verdict = flow.get("verdict")
    if trial is None or verdict not in VERDICTS:
        return
    protocol, src_port, dst_port, extra = _l4(flow.get("l4"))
    if protocol is None:
        return
    if dst_port == _DNS_PORT and _KUBE_DNS_LABEL in _labels(destination):
        return  # the DNS rows show these
    ip_block = flow.get("IP") if isinstance(flow.get("IP"), dict) else {}
    dst_ip = str(ip_block.get("destination") or "") or "unknown"
    if _local_noise(dst_ip):
        return  # multicast or link-local traffic of the pod's own stack (IPv6 neighbor discovery)
    names = flow.get("destination_names")
    name = (
        next((str(n).rstrip(".") for n in names or [] if n), "") if isinstance(names, list) else ""
    )
    reason = ""
    if verdict == "DROPPED":
        reason = str(flow.get("drop_reason_desc") or "") or "UNKNOWN"
    key = (verdict, name or dst_ip, dst_port, protocol, reason)
    connection = (ip_block.get("source"), src_port, dst_ip, dst_port, protocol, extra)
    row = acc[trial].flows.get(key)
    if row is None:
        row = acc[trial].flows[key] = _Row(at, at, dst_ip)
    row.see(at, connection)


def _local_noise(ip: str) -> bool:
    """Report whether ``ip`` is a multicast, link-local or unspecified address.

    The pod's network stack sends to these on its own (IPv6 router solicitations to ``ff02::2``,
    for example). Cilium drops them, but they say nothing about what the agent tried to reach.
    """
    try:
        address = ipaddress.ip_address(ip)
    except ValueError:
        return False
    return address.is_multicast or address.is_link_local or address.is_unspecified


def _search_expansion(name: str, entry: _Dns, dns: Mapping[str, _Dns]) -> bool:
    """Report whether ``name`` is a resolver search-path expansion of another name in ``dns``.

    The pod's resolver tries ``pypi.org.<ns>.svc.cluster.local`` and the other search domains
    before ``pypi.org``. Such a query that failed is left out when the bare name was asked too.
    """
    if entry.rcode not in (None, "NXDOMAIN"):
        return False
    return any(other != name and name.startswith(other + ".") for other in dns)


def _add_dns(trial: _Trial, dns: dict[str, Any], at: datetime, response: bool) -> None:
    name = str(dns.get("query") or "").rstrip(".")
    if not name:
        return
    entry = trial.dns.get(name)
    if entry is None:
        entry = trial.dns[name] = _Dns(at, at)
    entry.first, entry.last = min(entry.first, at), max(entry.last, at)
    if response:
        entry.responses += 1
        code = dns.get("rcode", 0)  # proto3 JSON leaves out 0, which is NOERROR
        try:
            entry.rcode = DNS_RCODES.get(int(code), str(code))
        except (TypeError, ValueError):
            entry.rcode = str(code)
    else:
        entry.requests += 1


def _endpoint(flow: Mapping[str, Any], side: str) -> Mapping[str, Any]:
    value = flow.get(side)
    return value if isinstance(value, dict) else {}


def _labels(endpoint: Mapping[str, Any]) -> list[str]:
    """Return an endpoint's labels without their source prefix (``k8s:a=b`` is ``a=b``)."""
    labels = endpoint.get("labels")
    if not isinstance(labels, list):
        return []
    return [str(label).split(":", 1)[1] if ":" in str(label) else str(label) for label in labels]


def _trial_of(endpoint: Mapping[str, Any], by_label: Mapping[str, str]) -> str | None:
    prefix = TRIAL_LABEL + "="
    for label in _labels(endpoint):
        if label.startswith(prefix):
            return by_label.get(label[len(prefix) :])
    return None


def _l4(l4: Any) -> tuple[str | None, int, int, Any]:
    """Return protocol, source and destination port, and what else tells connections apart."""
    if not isinstance(l4, dict):
        return None, 0, 0, None
    for protocol in PROTOCOLS:
        body = l4.get(protocol)
        if isinstance(body, dict):
            if protocol.startswith("ICMP"):
                return protocol, 0, 0, (body.get("type"), body.get("code"))
            return (
                protocol,
                _int(body.get("source_port")),
                _int(body.get("destination_port")),
                None,
            )
    return None, 0, 0, None


def _int(value: Any) -> int:
    try:
        return int(value)
    except (TypeError, ValueError):
        return 0


def _responses(path: Path) -> Iterator[dict[str, Any]]:
    """Yield every JSON object line of a ``hubble observe -o jsonpb`` file, skipping junk."""
    with path.open("rb") as handle:
        for raw in handle:
            line = raw.strip()
            if not line:
                continue
            try:
                value = json.loads(line)
            except ValueError:
                continue
            if isinstance(value, dict):
                yield value


def _scan(path: Path) -> tuple[int, list[tuple[datetime | None, int]]]:
    """Return the number of flows and the lost-event reports of a flow file."""
    if not path.exists():
        return 0, []
    flows = 0
    lost: list[tuple[datetime | None, int]] = []
    for response in _responses(path):
        if isinstance(response.get("flow"), dict):
            flows += 1
        events = response.get("lost_events")
        if isinstance(events, dict):
            # uint64 is a string in proto3 JSON.
            count = _int(events.get("num_events_lost"))
            if count > 0:
                lost.append((_parse_time(response.get("time")), count))
    return flows, lost


def _node_errors(path: Path) -> list[str]:
    """Return the nodes the relay reported unavailable or in error (jsonpb on stderr)."""
    if not path.exists():
        return []
    nodes: list[str] = []
    for response in _responses(path):
        status = response.get("node_status")
        if not isinstance(status, dict):
            continue
        if status.get("state_change") in ("NODE_UNAVAILABLE", "NODE_ERROR"):
            for name in status.get("node_names") or []:
                if str(name) not in nodes:
                    nodes.append(str(name))
    return nodes


def _cap(rows: list[dict[str, Any]], max_rows: int, rank: Any) -> tuple[list[dict[str, Any]], bool]:
    """Keep at most ``max_rows`` rows, the most telling first; return them by first time."""
    cut = len(rows) > max_rows
    if cut:
        rows = sorted(rows, key=rank)[:max_rows]
    return sorted(rows, key=lambda r: (r["firstAt"], r.get("name") or r.get("destination"))), cut


def _flow_rank(row: Mapping[str, Any]) -> tuple[int, int, str]:
    return _VERDICT_RANK.get(row["verdict"], 9), -row["count"], row["firstAt"]


def _dns_rank(row: Mapping[str, Any]) -> tuple[int, int, str]:
    return row.get("rcode", "NOERROR") == "NOERROR", -row["count"], row["firstAt"]


def _empty(state: str, reason: str) -> dict[str, Any]:
    section: dict[str, Any] = {"state": state}
    if reason:
        section["reason"] = reason
    section.update({"dns": [], "flows": [], "truncated": False, "lost": 0})
    return section


def _parse_time(value: Any) -> datetime | None:
    """Parse an RFC 3339 time with up to nanoseconds; None when it is not one."""
    if not isinstance(value, str) or not value:
        return None
    text = value.strip().replace("z", "Z")
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    main, sep, rest = text.partition(".")
    if sep:
        digits = len(rest) - len(rest.lstrip("0123456789"))
        text = f"{main}.{rest[:digits][:6].ljust(6, '0')}{rest[digits:]}"
    try:
        parsed = datetime.fromisoformat(text)
    except ValueError:
        return None
    return parsed if parsed.tzinfo else parsed.replace(tzinfo=UTC)


def _aware(value: datetime) -> datetime:
    """Return ``value`` in UTC; a time without a zone is taken as UTC."""
    return value.replace(tzinfo=UTC) if value.tzinfo is None else value.astimezone(UTC)


def _fmt(value: datetime) -> str:
    """Format a time like the bundle does: UTC, milliseconds, ``Z``."""
    value = _aware(value)
    return value.strftime("%Y-%m-%dT%H:%M:%S.") + f"{value.microsecond // 1000:03d}Z"


def _last_line(data: bytes | str | None) -> str:
    if not data:
        return ""
    text = data.decode("utf-8", "replace") if isinstance(data, bytes) else data
    lines = [line.strip() for line in text.splitlines() if line.strip()]
    return _clip(lines[-1]) if lines else ""


def _clip(text: str) -> str:
    return text if len(text) <= MAX_REASON_CHARS else text[: MAX_REASON_CHARS - 3] + "..."


def _join(reason: str, more: str) -> str:
    return f"{reason}; {more}" if reason else more
