"""Tests for flows.py: per-trial network sections from recorded ``hubble observe -o jsonpb``.

The flow lines follow what the pinned hubble CLI (v1.20.2) printed against the reference
cluster's Hubble Relay (Cilium 1.19.8): proto3 JSON with proto field names, one
``GetFlowsResponse`` per line. ``RECORDED_REPLY`` and ``RECORDED_EGRESS`` are real lines
(abridged: no ``node_labels``, documentation IPs for the public ones); the trial flows are
built from the egress line with the labels, flags and L7 parts a trial pod's flows carry.
"""

from __future__ import annotations

import copy
import json
import os
import signal
import stat
import subprocess
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

import pytest

import flows
from harbor_agent_sandbox import environment

SERVER = "hubble-relay.kube-system.svc.cluster.local:80"
EVAL = "acme-release-notes-v1-1-0-claude-code-2-1-292-claude-opus-5-5"
TRIAL_A = "rn-basic__sryqofs"
TRIAL_B = "rn-basic__BQbpoR9"
WINDOWS = {
    TRIAL_A: flows.Window(
        datetime(2026, 10, 30, 15, 0, 2, 110000, tzinfo=UTC),
        datetime(2026, 10, 30, 15, 3, 14, 551000, tzinfo=UTC),
    ),
    TRIAL_B: flows.Window(
        datetime(2026, 10, 30, 15, 4, 0, tzinfo=UTC),
        datetime(2026, 10, 30, 15, 6, 0, tzinfo=UTC),
    ),
}

# A reply of the skill-ci controller to Prometheus, recorded in namespace skill-ci.
RECORDED_REPLY = {
    "flow": {
        "time": "2026-10-31T19:44:40.308667774Z",
        "uuid": "3f12e80d-cb06-410e-9c69-e1837b2d6a80",
        "emitter": {"name": "Hubble", "version": "1.19.8+g5791d208"},
        "verdict": "FORWARDED",
        "ethernet": {"source": "9e:b1:4b:68:b2:89", "destination": "72:7e:c7:50:8a:2c"},
        "IP": {"source": "10.114.5.195", "destination": "10.114.4.11", "ipVersion": "IPv4"},
        "l4": {
            "TCP": {
                "source_port": 8081,
                "destination_port": 49092,
                "flags": {"PSH": True, "ACK": True},
            }
        },
        "source": {
            "identity": 23827,
            "cluster_name": "default",
            "namespace": "skill-ci",
            "labels": [
                "k8s:app.kubernetes.io/instance=skill-ci",
                "k8s:app.kubernetes.io/name=skill-ci-controller",
                "k8s:io.kubernetes.pod.namespace=skill-ci",
            ],
            "pod_name": "skill-ci-controller-6fb59bc9fc-mr6md",
        },
        "destination": {
            "ID": 1352,
            "identity": 49716,
            "cluster_name": "default",
            "namespace": "monitoring",
            "labels": ["k8s:app.kubernetes.io/name=prometheus"],
            "pod_name": "prometheus-kps-kube-prometheus-stack-prometheus-0",
        },
        "Type": "L3_L4",
        "node_name": "default-3eccr2",
        "reply": True,
        "event_type": {"type": 4},
        "traffic_direction": "EGRESS",
        "trace_observation_point": "TO_ENDPOINT",
        "trace_reason": "REPLY",
        "is_reply": True,
        "interface": {"index": 109, "name": "lxccebf5b901c99"},
        "Summary": "TCP Flags: ACK, PSH",
    },
    "node_name": "default-3eccr2",
    "time": "2026-10-31T19:44:40.308667774Z",
}

# A node's egress to the world on 443, recorded cluster-wide (public IPs replaced).
RECORDED_EGRESS = {
    "flow": {
        "time": "2026-10-31T19:45:01.136631061Z",
        "uuid": "1c2968c2-cae0-4523-8af9-7aa0688284c5",
        "emitter": {"name": "Hubble", "version": "1.19.8+g5791d208"},
        "verdict": "FORWARDED",
        "ethernet": {"source": "06:1f:fe:0b:3b:d0", "destination": "fe:00:00:00:01:01"},
        "IP": {"source": "192.0.2.1", "destination": "198.51.100.1", "ipVersion": "IPv4"},
        "l4": {"TCP": {"source_port": 42830, "destination_port": 443, "flags": {"ACK": True}}},
        "source": {"identity": 1, "labels": ["reserved:host"]},
        "destination": {"identity": 2, "labels": ["reserved:world"]},
        "Type": "L3_L4",
        "node_name": "default-3emfih",
        "event_type": {"type": 4, "sub_type": 11},
        "traffic_direction": "EGRESS",
        "trace_observation_point": "TO_NETWORK",
        "trace_reason": "ESTABLISHED",
        "is_reply": False,
        "interface": {"index": 2, "name": "eth0"},
        "Summary": "TCP Flags: ACK",
    },
    "node_name": "default-3emfih",
    "time": "2026-10-31T19:45:01.136631061Z",
}

KUBE_DNS = {
    "identity": 4512,
    "namespace": "kube-system",
    "labels": ["k8s:k8s-app=kube-dns", "k8s:io.kubernetes.pod.namespace=kube-system"],
    "pod_name": "coredns-7c8f9d4b6-x2kqp",
}
WORLD = {"identity": 2, "labels": ["reserved:world"]}


def pod(trial: str | None, ip: str = "10.114.5.40") -> dict[str, Any]:
    """Return a trial pod endpoint as Hubble shows it: identity labels with the k8s: prefix."""
    labels = [
        f"k8s:sandbox.users.io/skillci-eval={EVAL}",
        "k8s:sandbox.users.io/skillci-role=trial",
        "k8s:io.kubernetes.pod.namespace=skill-ci",
    ]
    if trial is not None:
        labels.append(f"k8s:sandbox.users.io/skillci-trial={environment.trial_label(trial)}")
    return {
        "identity": 61234,
        "namespace": "skill-ci",
        "labels": labels,
        "pod_name": "default-x7k2p",
        "_ip": ip,
    }


def at(clock: str) -> str:
    """Return a Hubble time (nanoseconds) on the test day."""
    return f"2026-10-30T{clock}123456Z"


def tcp(
    trial: str | None,
    clock: str,
    *,
    dst_ip: str,
    names: list[str] | None = None,
    port: int = 443,
    src_port: int = 40000,
    flags: dict[str, bool] | None = None,
    verdict: str = "FORWARDED",
    drop: str | None = None,
    reply: bool = False,
) -> dict[str, Any]:
    """Build a trial pod's TCP flow from the recorded egress line."""
    response = copy.deepcopy(RECORDED_EGRESS)
    flow = response["flow"]
    source = pod(trial)
    src_ip = source.pop("_ip")
    flow["time"] = response["time"] = at(clock)
    flow["verdict"] = verdict
    flow["IP"] = {"source": src_ip, "destination": dst_ip, "ipVersion": "IPv4"}
    flow["l4"] = {
        "TCP": {
            "source_port": src_port,
            "destination_port": port,
            "flags": flags or {"SYN": True},
        }
    }
    flow["source"], flow["destination"] = source, copy.deepcopy(WORLD)
    if reply:
        flow["source"], flow["destination"] = flow["destination"], flow["source"]
        flow["is_reply"] = True
    if names:
        flow["destination_names"] = names
    if drop:
        flow["drop_reason"] = 133
        flow["drop_reason_desc"] = drop
        flow["event_type"] = {"type": 1, "sub_type": 133}
        del flow["is_reply"]
    return response


def dns(
    trial: str, clock: str, name: str, *, response: bool = False, rcode: int | None = None
) -> dict[str, Any]:
    """Build an L7 DNS flow of the DNS proxy: a query from the pod, or the answer to it."""
    body: dict[str, Any] = {"query": name + ".", "qtypes": ["A"]}
    if response:
        body.update({"ips": ["192.0.2.10"], "ttl": 30, "rrtypes": ["A"]})
        if rcode:  # proto3 JSON leaves out 0 (NOERROR)
            body["rcode"] = rcode
    source = pod(trial)
    source.pop("_ip")
    flow: dict[str, Any] = {
        "time": at(clock),
        "verdict": "FORWARDED",
        "IP": {"source": "10.114.5.40", "destination": "10.114.0.10", "ipVersion": "IPv4"},
        "l4": {"UDP": {"source_port": 53001, "destination_port": 53}},
        "source": source,
        "destination": copy.deepcopy(KUBE_DNS),
        "Type": "L7",
        "node_name": "default-3emfih",
        "event_type": {"type": 129},
        "traffic_direction": "EGRESS",
        "is_reply": False,
        "l7": {"type": "REQUEST", "dns": body},
        "Summary": f"DNS Query {name}. A",
    }
    if response:
        flow["source"], flow["destination"] = flow["destination"], flow["source"]
        flow["is_reply"] = True
        flow["l7"] = {"type": "RESPONSE", "latency_ns": "812345", "dns": body}
    return {"flow": flow, "node_name": "default-3emfih", "time": flow["time"]}


def dns_hop(trial: str, clock: str) -> dict[str, Any]:
    """The L3/L4 flow of a DNS query to kube-dns, which Cilium reports next to the L7 one."""
    response = dns(trial, clock, "api.anthropic.com")
    flow = response["flow"]
    del flow["l7"]
    flow.update({"Type": "L3_L4", "event_type": {"type": 4}, "Summary": "UDP"})
    return response


def lost(clock: str, count: int) -> dict[str, Any]:
    """A lost-events line; ``num_events_lost`` is a uint64, so a string in proto3 JSON."""
    return {
        "lost_events": {
            "source": "HUBBLE_RING_BUFFER",
            "num_events_lost": str(count),
            "cpu": {"value": 1},
        },
        "node_name": "default-3emfih",
        "time": at(clock),
    }


def write(path: Path, lines: list[Any]) -> Path:
    path.write_text("".join((x if isinstance(x, str) else json.dumps(x)) + "\n" for x in lines))
    return path


def collected(tmp_path: Path, lines: list[Any], **kwargs: Any) -> flows.Collection:
    path = write(tmp_path / "hubble-flows.jsonl", lines)
    _, lost_events = flows._scan(path)
    total = sum(n for _, n in lost_events)
    return flows.Collection(
        kwargs.pop("state", flows.STATE_COLLECTED),
        kwargs.pop("reason", ""),
        path,
        total,
        lost_events,
    )


def a_run() -> list[Any]:
    """Trial A: two connections to api.anthropic.com, a denied pypi.org, DNS for both."""
    anthropic = {"dst_ip": "203.0.113.40", "names": ["api.anthropic.com"]}
    pypi = {"dst_ip": "198.51.100.23", "names": ["pypi.org"]}
    return [
        RECORDED_REPLY,  # another pod in the namespace: not a trial
        RECORDED_EGRESS,  # the node itself: not a trial
        dns(TRIAL_A, "15:01:00.", "api.anthropic.com"),
        dns(TRIAL_A, "15:01:00.", "api.anthropic.com", response=True),
        # Cilium reports the DNS hop at L3/L4 too; the DNS rows already show it.
        dns_hop(TRIAL_A, "15:01:00."),
        # One connection reported three times (SYN, ACK, PSH): it counts once.
        tcp(TRIAL_A, "15:01:01.", src_port=40001, **anthropic),
        tcp(TRIAL_A, "15:01:01.", src_port=40001, flags={"ACK": True}, **anthropic),
        tcp(TRIAL_A, "15:01:05.", src_port=40001, flags={"PSH": True, "ACK": True}, **anthropic),
        # The server's answers are replies: not a row of their own.
        tcp(
            TRIAL_A,
            "15:01:02.",
            src_port=40001,
            reply=True,
            flags={"SYN": True, "ACK": True},
            **anthropic,
        ),
        tcp(TRIAL_A, "15:02:30.", src_port=40002, **anthropic),
        dns(TRIAL_A, "15:02:00.", "pypi.org"),
        dns(TRIAL_A, "15:02:00.", "pypi.org", response=True),
        # Denied twice: a SYN and its retransmit (one connection), then a new attempt.
        tcp(TRIAL_A, "15:02:01.", src_port=40100, verdict="DROPPED", drop="POLICY_DENIED", **pypi),
        tcp(TRIAL_A, "15:02:02.", src_port=40100, verdict="DROPPED", drop="POLICY_DENIED", **pypi),
        tcp(TRIAL_A, "15:02:10.", src_port=40101, verdict="DROPPED", drop="POLICY_DENIED", **pypi),
        dns(TRIAL_A, "15:02:40.", "no-such-host.example"),
        dns(TRIAL_A, "15:02:40.", "no-such-host.example", response=True, rcode=3),
        # Trial B, and a trial pod without the trial label (an older sandbox package).
        tcp(TRIAL_B, "15:04:10.", src_port=41000, **anthropic),
        tcp(None, "15:04:11.", src_port=41001, dst_ip="192.0.2.99"),
        "not json",
        tcp(TRIAL_A, "15:02:50.", src_port=40200, verdict="TRACED", dst_ip="192.0.2.50"),
    ]


def validate(section: dict[str, Any]) -> None:
    """Validate against the trial schema's ``network`` when jsonschema is installed."""
    try:
        import jsonschema
    except ImportError:
        return
    schema_path = Path(__file__).parents[2] / "docs" / "schemas" / "skillci.trial.v1.json"
    schema = json.loads(schema_path.read_text())
    jsonschema.validate(section, {"$ref": "#/$defs/network", "$defs": schema["$defs"]})


# trial_label


@pytest.mark.parametrize(
    ("trial_id", "expected"),
    [
        ("rn-basic__sryqofs", "rn-basic__sryqofs"),
        ("a" * 63, "a" * 63),
    ],
)
def test_trial_label_keeps_a_valid_name(trial_id, expected):
    assert flows.trial_label(trial_id) == expected


def test_trial_label_shortens_like_the_policy_names():
    """Over 63 characters: the first 52, "-" and 10 hex characters of the SHA-256."""
    import hashlib

    trial_id = "release-notes-from-a-very-long-case-name-indeed-xyz__sryqofs1"
    trial_id += "q" * (70 - len(trial_id))
    label = flows.trial_label(trial_id)
    assert label == trial_id[:52] + "-" + hashlib.sha256(trial_id.encode()).hexdigest()[:10]
    assert len(label) == 63
    assert environment.LABEL_VALUE.match(label)


@pytest.mark.parametrize("trial_id", ["_odd__x", "with/slash__x", "x" * 52 + "/" * 30, "///"])
def test_trial_label_is_always_a_label_value(trial_id):
    assert environment.LABEL_VALUE.match(flows.trial_label(trial_id))


def test_trial_label_is_the_sandbox_implementation():
    """One implementation: the pod label and the flow mapping cannot drift apart."""
    assert flows.TRIAL_LABEL == environment.TRIAL_LABEL
    assert flows.EVAL_LABEL == environment.EVAL_LABEL
    for trial_id in ("rn-basic__sryqofs", "r" * 90):
        assert flows.trial_label(trial_id) == environment.trial_label(trial_id)


# summarize


def test_summarize_groups_flows_and_dns_per_trial(tmp_path):
    run, sections = flows.summarize(collected(tmp_path, a_run()), WINDOWS, max_rows=200)

    assert run == {"state": "collected"}
    a = sections[TRIAL_A]
    validate(a)
    assert a["state"] == "collected"
    assert "reason" not in a
    assert (a["source"], a["from"], a["to"]) == (
        "hubble",
        "2026-10-30T15:00:02.110Z",
        "2026-10-30T15:03:14.551Z",
    )
    assert a["dns"] == [
        {
            "name": "api.anthropic.com",
            "count": 1,
            "firstAt": "2026-10-30T15:01:00.123Z",
            "lastAt": "2026-10-30T15:01:00.123Z",
            "rcode": "NOERROR",
        },
        {
            "name": "pypi.org",
            "count": 1,
            "firstAt": "2026-10-30T15:02:00.123Z",
            "lastAt": "2026-10-30T15:02:00.123Z",
            "rcode": "NOERROR",
        },
        {
            "name": "no-such-host.example",
            "count": 1,
            "firstAt": "2026-10-30T15:02:40.123Z",
            "lastAt": "2026-10-30T15:02:40.123Z",
            "rcode": "NXDOMAIN",
        },
    ]
    assert a["flows"] == [
        {
            "verdict": "FORWARDED",
            "destination": "api.anthropic.com",
            "ip": "203.0.113.40",
            "port": 443,
            "protocol": "TCP",
            "count": 2,
            "firstAt": "2026-10-30T15:01:01.123Z",
            "lastAt": "2026-10-30T15:02:30.123Z",
        },
        {
            "verdict": "DROPPED",
            "destination": "pypi.org",
            "ip": "198.51.100.23",
            "port": 443,
            "protocol": "TCP",
            "count": 2,
            "firstAt": "2026-10-30T15:02:01.123Z",
            "lastAt": "2026-10-30T15:02:10.123Z",
            "reason": "POLICY_DENIED",
        },
    ]
    assert (a["truncated"], a["lost"]) == (False, 0)

    b = sections[TRIAL_B]
    validate(b)
    assert [(f["destination"], f["count"]) for f in b["flows"]] == [("api.anthropic.com", 1)]
    assert b["dns"] == []


def test_destination_without_a_dns_name_is_its_ip(tmp_path):
    lines = [tcp(TRIAL_A, "15:01:00.", dst_ip="192.0.2.77", port=8443)]
    _, sections = flows.summarize(collected(tmp_path, lines), WINDOWS, max_rows=200)
    (row,) = sections[TRIAL_A]["flows"]
    assert (row["destination"], row["ip"], row["port"]) == ("192.0.2.77", "192.0.2.77", 8443)


def test_a_trial_without_flows_gets_an_empty_section_with_the_run_state(tmp_path):
    _, sections = flows.summarize(collected(tmp_path, []), WINDOWS, max_rows=200)
    for section in sections.values():
        validate(section)
        assert section["state"] == "collected"
        assert (section["dns"], section["flows"], section["truncated"]) == ([], [], False)


def test_lost_events_make_the_run_partial_and_count_per_window(tmp_path):
    lines = [*a_run(), lost("15:01:30.", 7), lost("15:05:00.", 5), lost("15:09:00.", 1)]
    collector = flows.FlowCollector(SERVER, EVAL, tmp_path / "hubble-flows.jsonl")
    write(collector.out, lines)
    collector._proc = _Exited(None)
    collection = collector.stop()
    assert collection.state == "partial"
    assert collection.lost == 13
    assert "13 lost events" in collection.reason

    run, sections = flows.summarize(collection, WINDOWS, max_rows=200)
    assert run["state"] == "partial"
    assert "lost events" in run["reason"]
    assert sections[TRIAL_A]["lost"] == 7
    assert sections[TRIAL_B]["lost"] == 5
    assert all(s["state"] == "partial" for s in sections.values())
    validate(sections[TRIAL_A])


def test_row_caps_keep_the_drops_and_mark_the_section(tmp_path):
    lines = [
        tcp(
            TRIAL_A, "15:01:00.", src_port=40000 + i, dst_ip=f"192.0.2.{i}", names=[f"h{i}.example"]
        )
        for i in range(5)
    ]
    lines.append(
        tcp(
            TRIAL_A,
            "15:02:59.",
            dst_ip="198.51.100.23",
            names=["pypi.org"],
            verdict="DROPPED",
            drop="POLICY_DENIED",
        )
    )
    lines += [dns(TRIAL_A, "15:00:5" + str(i) + ".", f"h{i}.example") for i in range(5)]
    run, sections = flows.summarize(collected(tmp_path, lines), WINDOWS, max_rows=2)

    a = sections[TRIAL_A]
    validate(a)
    assert a["truncated"] is True
    assert a["state"] == "partial"
    assert "rows capped at 2" in a["reason"]
    assert len(a["flows"]) == 2
    assert len(a["dns"]) == 2
    assert a["flows"][-1]["destination"] == "pypi.org"  # the drop survives the cap
    assert run == {"state": "partial", "reason": "rows capped at 2 for 1 trial(s)"}
    assert sections[TRIAL_B]["state"] == "collected"


def test_dns_answer_without_a_query_still_counts(tmp_path):
    lines = [dns(TRIAL_A, "15:01:00.", "api.openai.com", response=True, rcode=2)]
    _, sections = flows.summarize(collected(tmp_path, lines), WINDOWS, max_rows=200)
    assert sections[TRIAL_A]["dns"] == [
        {
            "name": "api.openai.com",
            "count": 1,
            "firstAt": "2026-10-30T15:01:00.123Z",
            "lastAt": "2026-10-30T15:01:00.123Z",
            "rcode": "SERVFAIL",
        }
    ]


def test_summarize_without_a_collector_is_off():
    run, sections = flows.summarize(None, WINDOWS, max_rows=200)
    assert run == {"state": "off", "reason": flows.OFF_REASON}
    assert sections == {}


def test_unavailable_gives_every_trial_the_reason():
    reason = f"Hubble Relay {SERVER} did not answer within 10s"
    collection = flows.Collection("unavailable", reason, None)
    run, sections = flows.summarize(collection, WINDOWS, max_rows=200)
    assert run == {"state": "unavailable", "reason": reason}
    for section in sections.values():
        validate(section)
        assert section == {
            "state": "unavailable",
            "reason": reason,
            "dns": [],
            "flows": [],
            "truncated": False,
            "lost": 0,
        }


def test_naive_windows_are_utc(tmp_path):
    windows = {TRIAL_A: flows.Window(datetime(2026, 10, 30, 15, 0), datetime(2026, 10, 30, 15, 3))}
    collection = collected(tmp_path, [lost("15:01:30.", 2)])
    _, sections = flows.summarize(collection, windows, max_rows=200)
    assert sections[TRIAL_A]["from"] == "2026-10-30T15:00:00.000Z"
    assert sections[TRIAL_A]["lost"] == 2


# collector, against a fake hubble CLI


class _Exited:
    """A finished process: what stop() sees when the CLI is already gone."""

    def __init__(self, code: int | None) -> None:
        self.code = code

    def poll(self) -> int | None:
        return self.code

    def send_signal(self, sig: int) -> None:
        self.code = 0

    def wait(self, timeout: float | None = None) -> int:
        return self.code or 0

    def kill(self) -> None:
        self.code = -9


def fake_hubble(tmp_path: Path, monkeypatch, *, status: str, observe: str) -> Path:
    """Put a fake ``hubble`` on PATH; it records its arguments and environment."""
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    log = tmp_path / "calls.log"
    script = bin_dir / "hubble"
    script.write_text(
        "#!/bin/sh\n"
        f'echo "$@" >> {log}\n'
        f"env >> {tmp_path / 'env.log'}\n"
        'case "$1" in\n'
        f"status)\n{status}\n;;\n"
        f"observe)\n{observe}\n;;\n"
        "esac\n"
    )
    script.chmod(script.stat().st_mode | stat.S_IEXEC)
    monkeypatch.setenv("PATH", f"{bin_dir}{os.pathsep}{os.environ['PATH']}")
    return log


STATUS_OK = "echo 'Healthcheck (via relay): Ok'\nexit 0\n"


def test_collector_runs_observe_with_the_pinned_flags(tmp_path, monkeypatch):
    lines = tmp_path / "lines.jsonl"
    write(lines, a_run())
    log = fake_hubble(
        tmp_path,
        monkeypatch,
        status=STATUS_OK,
        observe=f"cat {lines}\ntrap 'exit 0' TERM\nwhile true; do sleep 0.1; done\n",
    )
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-ant-test-0000")
    collector = flows.start_from_env({"HUBBLE_SERVER": f" {SERVER} "}, EVAL, tmp_path / "work")
    assert collector is not None
    _wait_for(lambda: collector.out.exists() and collector.out.stat().st_size > 0)
    collection = collector.stop()

    assert collection.state == "collected"
    assert collection.reason == ""
    assert collection.path == tmp_path / "work" / "hubble-flows.jsonl"
    assert collector.stop() is collection  # idempotent
    calls = log.read_text().splitlines()
    assert calls[0] == f"status --server {SERVER} --timeout 10s --request-timeout 10s"
    assert calls[1] == (
        f"observe --server {SERVER} --timeout 10s --follow --output jsonpb "
        f"--label sandbox.users.io/skillci-eval={EVAL}"
    )
    # The CLI gets no credentials (and no HUBBLE_* settings of its own).
    env_log = (tmp_path / "env.log").read_text()
    assert "sk-ant-test-0000" not in env_log
    assert "HUBBLE_SERVER" not in env_log

    _, sections = flows.summarize(collection, WINDOWS, max_rows=200)
    assert len(sections[TRIAL_A]["flows"]) == 2


def test_start_from_env_without_a_server_is_off(tmp_path):
    assert flows.start_from_env({}, EVAL, tmp_path) is None
    assert flows.start_from_env({"HUBBLE_SERVER": "  "}, EVAL, tmp_path) is None


def test_relay_that_does_not_answer_within_10s(tmp_path, monkeypatch):
    """hubble status runs into its deadline: unavailable, and observe never starts."""
    log = fake_hubble(
        tmp_path,
        monkeypatch,
        status=(
            "echo 'failed getting status: rpc error: code = DeadlineExceeded desc = context "
            "deadline exceeded while waiting for connections to become ready' >&2\nexit 1\n"
        ),
        observe="exit 99\n",
    )
    collector = flows.start_from_env({"HUBBLE_SERVER": SERVER}, EVAL, tmp_path)
    assert collector is not None
    collection = collector.stop()
    assert collection == flows.Collection(
        "unavailable", f"Hubble Relay {SERVER} did not answer within 10s", None
    )
    assert [line.split()[0] for line in log.read_text().splitlines()] == ["status"]


def test_relay_that_hangs_past_the_probe_timeout(tmp_path, monkeypatch):
    fake_hubble(tmp_path, monkeypatch, status=STATUS_OK, observe="exit 99\n")

    def hang(command, **kwargs):
        assert kwargs["timeout"] > flows.CONNECT_TIMEOUT_SEC
        raise subprocess.TimeoutExpired(command, kwargs["timeout"])

    monkeypatch.setattr(flows.subprocess, "run", hang)
    collection = flows.start_from_env({"HUBBLE_SERVER": SERVER}, EVAL, tmp_path).stop()
    assert collection.state == "unavailable"
    assert collection.reason == f"Hubble Relay {SERVER} did not answer within 10s"


def test_status_failure_reports_the_cli_message(tmp_path, monkeypatch):
    fake_hubble(
        tmp_path,
        monkeypatch,
        status="echo 'Unavailable: connection refused' >&2\nexit 2\n",
        observe="exit 99\n",
    )
    collection = flows.start_from_env({"HUBBLE_SERVER": SERVER}, EVAL, tmp_path).stop()
    assert collection.state == "unavailable"
    assert collection.reason == (
        f"hubble status against {SERVER} failed (exit 2): Unavailable: connection refused"
    )


def test_missing_cli_is_unavailable(tmp_path, monkeypatch):
    monkeypatch.setenv("PATH", str(tmp_path / "empty"))
    collection = flows.start_from_env({"HUBBLE_SERVER": SERVER}, EVAL, tmp_path).stop()
    assert collection == flows.Collection("unavailable", "the hubble CLI is not on PATH", None)


def test_observe_that_fails_before_any_flow_is_unavailable(tmp_path, monkeypatch):
    fake_hubble(
        tmp_path,
        monkeypatch,
        status=STATUS_OK,
        observe="echo 'rpc error: code = Unavailable desc = connection error' >&2\nexit 1\n",
    )
    collector = flows.start_from_env({"HUBBLE_SERVER": SERVER}, EVAL, tmp_path)
    _wait_for(lambda: collector._proc.poll() is not None)
    collection = collector.stop()
    assert collection.state == "unavailable"
    assert collection.reason == (
        "hubble observe exited early (exit 1): "
        "rpc error: code = Unavailable desc = connection error"
    )
    _, sections = flows.summarize(collection, WINDOWS, max_rows=200)
    assert sections[TRIAL_A]["state"] == "unavailable"
    validate(sections[TRIAL_A])


def test_observe_that_dies_after_flows_is_partial(tmp_path, monkeypatch):
    lines = write(tmp_path / "lines.jsonl", a_run())
    fake_hubble(
        tmp_path,
        monkeypatch,
        status=STATUS_OK,
        observe=f"cat {lines}\necho 'stream closed' >&2\nexit 1\n",
    )
    collector = flows.start_from_env({"HUBBLE_SERVER": SERVER}, EVAL, tmp_path)
    _wait_for(lambda: collector._proc.poll() is not None)
    collection = collector.stop()
    assert collection.state == "partial"
    assert collection.reason == "hubble observe exited early (exit 1): stream closed"
    run, sections = flows.summarize(collection, WINDOWS, max_rows=200)
    assert run["state"] == "partial"
    assert len(sections[TRIAL_A]["flows"]) == 2


def test_unavailable_nodes_make_the_run_partial(tmp_path, monkeypatch):
    node_status = json.dumps(
        {
            "node_status": {
                "state_change": "NODE_UNAVAILABLE",
                "node_names": ["default-3ecygs"],
                "message": "connection refused",
            },
            "time": at("15:00:01."),
        }
    )
    fake_hubble(
        tmp_path,
        monkeypatch,
        status=STATUS_OK,
        observe=f"echo '{node_status}' >&2\ntrap 'exit 0' TERM\nwhile true; do sleep 0.1; done\n",
    )
    collector = flows.start_from_env({"HUBBLE_SERVER": SERVER}, EVAL, tmp_path)
    _wait_for(lambda: collector.err.exists() and collector.err.stat().st_size > 0)
    collection = collector.stop()
    assert collection.state == "partial"
    assert collection.reason == "Hubble nodes unavailable: default-3ecygs"


def test_stop_kills_a_cli_that_ignores_sigterm(tmp_path, monkeypatch):
    fake_hubble(
        tmp_path,
        monkeypatch,
        status=STATUS_OK,
        observe="trap '' TERM\necho started >&2\nwhile true; do sleep 0.1; done\n",
    )
    monkeypatch.setattr(flows, "STOP_GRACE_SEC", 0.5)
    collector = flows.start_from_env({"HUBBLE_SERVER": SERVER}, EVAL, tmp_path)
    _wait_for(lambda: collector.err.exists() and collector.err.stat().st_size > 0)
    collection = collector.stop()
    assert collector._proc.returncode == -signal.SIGKILL
    assert collection.state == "collected"


def test_an_invalid_eval_name_is_unavailable(tmp_path, monkeypatch):
    fake_hubble(tmp_path, monkeypatch, status=STATUS_OK, observe="exit 99\n")
    collection = flows.start_from_env({"HUBBLE_SERVER": SERVER}, "x" * 64, tmp_path).stop()
    assert collection.state == "unavailable"
    assert "not a label value" in collection.reason


def _wait_for(check, timeout: float = 10.0) -> None:
    import time

    deadline = time.monotonic() + timeout
    while not check():
        if time.monotonic() > deadline:
            raise AssertionError("condition not reached")
        time.sleep(0.05)
