"""Tests for the trial bundle builder, against the golden bundles in testdata/trials."""

from __future__ import annotations

import base64
import gzip
import json
import random
import re
import shutil
import string
from collections.abc import Callable
from pathlib import Path
from typing import Any

import pytest

import trial_bundle
from trial_bundle import Credential, Limits, Redactor

REPO = Path(__file__).resolve().parents[2]
TRIALS = REPO / "testdata" / "trials"
CANARY = REPO / "testdata" / "trials-secrets" / "canary"
SCHEMAS = REPO / "docs" / "schemas"
ANTHROPIC = Credential("agent", "ANTHROPIC_API_KEY", "api.anthropic.com")
JUDGE = Credential("judge", "ANTHROPIC_API_KEY", "api.anthropic.com")
NO_RELAY = {
    "state": "off",
    "reason": "no Hubble Relay configured (SkillCIConfig spec.observability.hubbleRelay is empty)",
}
FAKE_KEY = "sk-ant-test-0000000000000000000000000000000000000000"


def fake_redact(text: str) -> str:
    """Stand in for SkillEvaluator's redact_sensitive_text: a few of its token shapes only.

    No ``key=value`` rule, so the env-dump rule is what catches assignments in CI.
    """
    text = re.sub(r"(?<![A-Za-z0-9])sk-[a-zA-Z0-9_-]{8,}", "sk-<redacted>", text)
    text = re.sub(r"nvapi-[a-zA-Z0-9_-]{8,}", "nvapi-<redacted>", text)
    return re.sub(r"(?i)gh[pour]_[A-Za-z0-9]{36}", "github-token-<redacted>", text)


@pytest.fixture(autouse=True)
def pattern_rule(monkeypatch: pytest.MonkeyPatch) -> Callable[[str], str]:
    """Use SkillEvaluator's pattern rule when it is installed, else the stub."""
    if trial_bundle.redact_sensitive_text is None:
        monkeypatch.setattr(trial_bundle, "redact_sensitive_text", fake_redact)
    return trial_bundle.redact_sensitive_text


def canary_values() -> dict[str, str]:
    """Return the fake values of secrets.env by name."""
    values = {}
    for line in (CANARY / "secrets.env").read_text().splitlines():
        if line and not line.startswith("#"):
            name, _, value = line.partition("=")
            values[name] = value
    return values


def canary_env() -> dict[str, str]:
    """Return the runner environment of the canary: the provider keys and HARBOR_PASSWORD."""
    return {
        k: v
        for k, v in canary_values().items()
        if k.endswith("_API_KEY") or k.startswith("HARBOR_")
    }


def config_for(index: dict[str, Any]) -> dict[str, str]:
    """Return the runner config that produced a golden index."""
    config = {
        "SKILL_NAME": index["skill"],
        "SKILL_REF": f"harbor.harbor.svc.cluster.local/skills/{index['skill']}@{index['digest']}",
        "AGENT": index["agent"],
        "MODEL": index["model"],
        "ATTEMPTS": str(index["attemptsPerCase"]),
        "SKILLEVAL_NAME": index["eval"],
    }
    if "version" in index:
        config["SKILL_VERSION"] = index["version"]
    return config


def run_build(
    run_dir: Path, out: Path, *, environ: dict[str, str] | None = None, **kwargs: Any
) -> trial_bundle.Bundle | trial_bundle.NotBuilt:
    """Build with the doks defaults; keyword arguments override them."""
    args: dict[str, Any] = {
        "environ": environ if environ is not None else {"ANTHROPIC_API_KEY": FAKE_KEY},
        "config": config_for(
            json.loads((TRIALS / "claude-code-doks/bundle/index.json").read_text())
        ),
        "agent_version": "2.1.292",
        "credentials": [ANTHROPIC, JUDGE],
        "network": NO_RELAY,
        "auth_config": out.parent / "no-auth-config.json",
    }
    args.update(kwargs)
    return trial_bundle.build(run_dir, out, **args)


def layers(bundle: trial_bundle.Bundle) -> dict[str, bytes]:
    """Return every layer's bytes as uploaded, trials decompressed."""
    out = {}
    for name in bundle.files:
        data = (bundle.directory / name).read_bytes()
        out[name] = gzip.decompress(data) if name.endswith(".gz") else data
    return out


def trial_docs(bundle: trial_bundle.Bundle) -> dict[str, dict[str, Any]]:
    """Return the trial documents of a bundle by trial id."""
    return {
        json.loads(data)["id"]: json.loads(data)
        for name, data in layers(bundle).items()
        if name != trial_bundle.INDEX_FILE
    }


def validate(bundle: trial_bundle.Bundle) -> None:
    """Validate every layer against its schema when jsonschema is installed."""
    try:
        import jsonschema
    except ImportError:
        return
    index_schema = json.loads((SCHEMAS / "skillci.trials.index.v1.json").read_text())
    trial_schema = json.loads((SCHEMAS / "skillci.trial.v1.json").read_text())
    for name, data in layers(bundle).items():
        schema = index_schema if name == trial_bundle.INDEX_FILE else trial_schema
        jsonschema.Draft202012Validator(schema).validate(json.loads(data))


def resolve(doc: Any, pointer: str) -> Any:
    """Return the value at an RFC 6901 pointer."""
    for raw in pointer.split("/")[1:]:
        key = raw.replace("~1", "/").replace("~0", "~")
        doc = doc[int(key)] if isinstance(doc, list) else doc[key]
    return doc


def noise(n: int, seed: int) -> str:
    """Return ``n`` letters gzip cannot shrink much and no redaction rule matches."""
    return "".join(random.Random(seed).choices(string.ascii_letters, k=n))  # noqa: S311 (test data)


def make_run(
    root: Path,
    trials: list[dict[str, Any]],
    *,
    agent: str = "claude-code",
) -> Path:
    """Write a SkillEvaluator run directory with the given trials.

    Each trial: ``id``, ``arm``, ``case``, ``attempt``, optional ``observation``, ``message``,
    ``steps`` (extra agent steps with that message), ``log`` and ``reward_text``.
    """
    run = root / "run"
    cases: dict[str, dict[str, Any]] = {"with_skill": {}, "without_skill": {}}
    for t in trials:
        arm_key = t["arm"].replace("-", "_")
        attempts = cases[arm_key].setdefault(t["case"], {"attempts": []})["attempts"]
        attempts.append({"attempt": t["attempt"], "trial": t["id"], "score": 0.8, "passed": True})
        trial = run / agent / t["arm"] / "trials" / t["id"]
        trial.mkdir(parents=True)
        result = json.loads(
            (
                CANARY / "run-dir/claude-code/with-skill/trials/canary-env__T3stK3y/result.json"
            ).read_text()
        )
        result["trial_name"] = t["id"]
        (trial / "result.json").write_text(json.dumps(result))
        steps: list[dict[str, Any]] = [
            {"step_id": 1, "source": "user", "message": f"Do {t['case']}.\n"},
            {
                "step_id": 2,
                "source": "agent",
                "message": t.get("message", ""),
                "tool_calls": [{"tool_call_id": "c1", "function_name": "Bash", "arguments": {}}],
                "observation": {
                    "results": [{"source_call_id": "c1", "content": t.get("observation", "ok")}]
                },
            },
        ]
        for i in range(t.get("steps", 0)):
            steps.append({"step_id": 3 + i, "source": "agent", "message": noise(4096, 1000 + i)})
        trajectory = {
            "schema_version": "ATIF-v1.7",
            "session_id": "synthetic-" + t["id"],
            "agent": {"name": agent, "extra": {"fixture": "synthetic"}},
            "steps": steps,
            "final_metrics": {
                "total_prompt_tokens": 10,
                "total_completion_tokens": 5,
                "total_cached_tokens": 0,
            },
        }
        (trial / "trajectory.json").write_text(json.dumps(trajectory))
        reward = {
            "overall": 0.8,
            "skill_invoked": True,
            "details": {"note": t.get("reward_text", "ok")},
        }
        (trial / "reward.json").write_text(json.dumps(reward))
        (trial / trial_bundle.AGENT_LOGS[agent]).write_text(t.get("log", "agent log\n"))
    result = {
        "agents": {
            agent: {"pass_at_k": {k: {"pass_threshold": 0.5, "cases": v} for k, v in cases.items()}}
        },
        "run_id": "synthetic-run",
        "evaluated_at": "2026-10-30T17:00:21+00:00",
    }
    (run / "result.json").write_text(json.dumps(result))
    names = sorted({t["case"] for t in trials})
    dataset = {
        "dataset": [
            {"id": n, "question": f"Do {n}.", "expected_behavior": ["works"]} for n in names
        ]
    }
    (run / "dataset_snapshot.json").write_text(json.dumps(dataset))
    return run


def with_limits(monkeypatch: pytest.MonkeyPatch, **caps: int) -> Limits:
    """Make build use ``Limits(**caps)``."""
    limits = Limits(**caps)
    monkeypatch.setattr(trial_bundle, "limits_from_env", lambda environ: limits)
    return limits


# Golden bundles


@pytest.mark.parametrize("name", ["claude-code-doks", "claude-code-unscored"])
def test_golden_bundle(name: str, tmp_path: Path) -> None:
    golden = TRIALS / name / "bundle"
    want = json.loads((golden / "index.json").read_text())
    out = tmp_path / "bundle"
    bundle = run_build(TRIALS / name / "run-dir", out, config=config_for(want))
    assert isinstance(bundle, trial_bundle.Bundle)
    got = json.loads((out / "index.json").read_text())
    for entry in got["trials"]:
        assert entry.pop("bytes") > 0
        assert entry.pop("rawBytes") > 0
    for entry in want["trials"]:
        entry.pop("bytes")
        entry.pop("rawBytes")
    assert got == want
    assert bundle.index["trials"][0]["layer"] == bundle.files[1]
    for trial_id, doc in trial_docs(bundle).items():
        assert doc == json.loads((golden / f"trial-{trial_id}.json").read_text())
    assert bundle.files == ["index.json"] + [f"trial-{t['id']}.json.gz" for t in want["trials"]]
    assert bundle.withheld == []
    assert bundle.annotations == {
        "skillci.dev/eval": want["eval"],
        "skillci.dev/agent": "claude-code",
        "skillci.dev/agent-version": "2.1.292",
        "skillci.dev/model": "claude-haiku-5-5",
        "skillci.dev/trials-schema": "1",
        "skillci.dev/redaction": "v1",
    }
    validate(bundle)


def test_layers_are_compact_gzip_and_the_index_is_plain_json(tmp_path: Path) -> None:
    bundle = run_build(TRIALS / "claude-code-doks/run-dir", tmp_path / "b")
    index = (tmp_path / "b/index.json").read_bytes()
    assert json.loads(index) == bundle.index
    assert b": " not in index[:200]
    for entry in bundle.index["trials"]:
        packed = (tmp_path / "b" / entry["layer"]).read_bytes()
        assert len(packed) == entry["bytes"]
        assert len(gzip.decompress(packed)) == entry["rawBytes"]


def test_forbidden_sources_never_reach_the_bundle(tmp_path: Path) -> None:
    run = tmp_path / "run"
    shutil.copytree(TRIALS / "claude-code-doks/run-dir", run)
    trial = run / "claude-code/with-skill/trials/rn-basic__sryqofs"
    (trial / "config.json").write_text(json.dumps({"kubeconfig": "MARKER-CONFIG-1"}))
    (trial / "trial.log").write_text("MARKER-TRIAL-LOG-2\n")
    (trial / "lock.json").write_text('{"x": "MARKER-LOCK-3"}')
    sessions = trial / "agent" / "sessions"
    sessions.mkdir(parents=True)
    (sessions / "s.jsonl").write_text('{"user": "MARKER-SESSION-4"}\n')
    jobs = run / "_harbor-jobs" / "job" / "rn-basic__sryqofs"
    jobs.mkdir(parents=True)
    (jobs / "result.json").write_text('{"x": "MARKER-JOBS-5"}')
    bundle = run_build(run, tmp_path / "b")
    assert isinstance(bundle, trial_bundle.Bundle)
    blob = b"".join(layers(bundle).values())
    for marker in (
        b"MARKER-CONFIG",
        b"MARKER-TRIAL-LOG",
        b"MARKER-LOCK",
        b"MARKER-SESSION",
        b"MARKER-JOBS",
    ):
        assert marker not in blob
    # The real config.json of the fixture holds the kubeconfig path.
    assert b"kubeconfig" not in blob


# Redaction


def test_canary_leaves_no_form_of_any_secret(tmp_path: Path, pattern_rule) -> None:
    out = tmp_path / "b"
    config = config_for(json.loads((CANARY / "bundle-unredacted/index.json").read_text()))
    bundle = run_build(
        CANARY / "run-dir",
        out,
        environ=canary_env(),
        config=config,
        auth_config=CANARY / "auth-config.json",
    )
    assert isinstance(bundle, trial_bundle.Bundle)
    assert bundle.withheld == []
    assert [t["id"] for t in bundle.index["trials"]] == ["canary-env__T3stK3y"]
    blob = b"".join(layers(bundle).values())
    values = canary_values()
    password = base64.b64decode(values["REGISTRY_AUTH"]).decode().partition(":")[2]
    for name, value in [*values.items(), ("decoded password", password)]:
        for form in trial_bundle.secret_forms(value):
            assert form.encode() not in blob, f"{name} leaked as {form[:6]}..."
    doc = trial_docs(bundle)["canary-env__T3stK3y"]
    counts = doc["redaction"]
    assert counts["exact"] > 0
    assert counts["pattern"] > 0
    if pattern_rule is fake_redact:
        assert counts["envDump"] > 0  # the stub has no assignment rule; the real one does
    assert "[redacted:ANTHROPIC_API_KEY]" in doc["agentLog"]["text"]
    validate(bundle)


def test_canary_with_skillevaluators_pattern_rule(tmp_path: Path) -> None:
    redaction = pytest.importorskip("skillevaluator.utils.redaction")
    assert trial_bundle.redact_sensitive_text is redaction.redact_sensitive_text
    bundle = run_build(
        CANARY / "run-dir",
        tmp_path / "b",
        environ={},  # no exact values: the pattern and env-dump rules alone
        auth_config=CANARY / "auth-config.json",
    )
    blob = b"".join(layers(bundle).values())
    for name in (
        "ANTHROPIC_API_KEY",
        "NVIDIA_API_KEY",
        "OPENROUTER_API_KEY",
        "GITHUB_TOKEN",
        "SESSION_SECRET",
    ):
        assert canary_values()[name].encode() not in blob, name


def test_a_secret_the_rules_miss_withholds_the_trial(tmp_path: Path) -> None:
    run = tmp_path / "run"
    shutil.copytree(CANARY / "run-dir", run)
    path = run / "claude-code/with-skill/trials/canary-env__T3stK3y/trajectory.json"
    trajectory = json.loads(path.read_text())
    # Keys are never rewritten (pointers must stay valid), so only the scan can catch this.
    trajectory["steps"][1]["extra"] = {canary_values()["OPENAI_API_KEY"]: True}
    path.write_text(json.dumps(trajectory))
    bundle = run_build(
        run, tmp_path / "b", environ=canary_env(), auth_config=CANARY / "auth-config.json"
    )
    assert isinstance(bundle, trial_bundle.Bundle)
    assert bundle.index["trials"] == []
    assert bundle.withheld == [
        {
            "id": "canary-env__T3stK3y",
            "arm": "with-skill",
            "case": "canary-env",
            "attempt": 1,
            "reason": "secret-scan",
        }
    ]
    assert bundle.files == ["index.json"]
    assert not list((tmp_path / "b").glob("trial-*"))
    validate(bundle)


def test_broken_redaction_is_caught_by_the_scan(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr(Redactor, "walk", lambda self, doc: doc)
    bundle = run_build(CANARY / "run-dir", tmp_path / "b", environ=canary_env())
    assert [w["reason"] for w in bundle.withheld] == ["secret-scan"]


def test_an_index_leak_withholds_the_bundle(tmp_path: Path, capsys: pytest.CaptureFixture) -> None:
    run = tmp_path / "run"
    shutil.copytree(CANARY / "run-dir", run)
    result = json.loads((run / "result.json").read_text())
    result["run_id"] = canary_values()["NVIDIA_API_KEY"]
    (run / "result.json").write_text(json.dumps(result))
    out = tmp_path / "b"
    got = run_build(run, out, environ=canary_env())
    assert got == trial_bundle.NotBuilt("secret-scan", "the index contains NVIDIA_API_KEY")
    assert not out.exists() or not list(out.iterdir())
    assert canary_values()["NVIDIA_API_KEY"] not in capsys.readouterr().err


def test_redaction_never_logs_values(tmp_path: Path, capsys: pytest.CaptureFixture) -> None:
    run = tmp_path / "run"
    shutil.copytree(CANARY / "run-dir", run)
    path = run / "claude-code/with-skill/trials/canary-env__T3stK3y/reward.json"
    reward = json.loads(path.read_text())
    reward[canary_values()["ANTHROPIC_API_KEY"]] = 1
    path.write_text(json.dumps(reward))
    run_build(run, tmp_path / "b", environ=canary_env())
    err = capsys.readouterr().err
    assert "ANTHROPIC_API_KEY" in err
    assert canary_values()["ANTHROPIC_API_KEY"] not in err


def test_secret_forms() -> None:
    value = 'a/b+c=d"é0000'
    forms = trial_bundle.secret_forms(value)
    raw = value.encode()
    assert value in forms
    assert base64.b64encode(raw).decode() in forms
    assert base64.b64encode(raw).decode().rstrip("=") in forms
    assert base64.urlsafe_b64encode(raw).decode().rstrip("=") in forms
    assert "a%2Fb%2Bc%3Dd%22%C3%A90000" in forms
    assert 'a/b+c=d\\"\\u00e90000' in forms
    assert [len(f) for f in forms] == sorted((len(f) for f in forms), reverse=True)
    assert len(forms) == len(set(forms))


def test_secret_values(tmp_path: Path) -> None:
    auth = tmp_path / "config.json"
    secret = base64.b64encode(b"robot$ci:Registry-pass-0000").decode()
    auth.write_text(
        json.dumps(
            {
                "auths": {
                    "a.example": {"auth": secret, "identitytoken": "id-token-0000000"},
                    "b.example": {"password": "other-pass-0000"},
                }
            }
        )
    )
    environ = {
        "ANTHROPIC_API_KEY": FAKE_KEY,
        "OPENAI_API_KEY": "short",  # under MIN_SECRET_CHARS
        "GOOGLE_API_KEY": "judge-key-0000000",  # the judge may use any llm-keys key
        "SKILL_EVAL_LLM_API_KEY": FAKE_KEY,  # the same value once
        "SKILL_EVAL_EMBEDDING_API_KEY": "embedding-key-0000",
        "AWS_BEARER_TOKEN_BEDROCK": "bedrock-test-token-0000",
        "HARBOR_PASSWORD": "Harbor-test-pass-0000",
        "HARBOR_SANDBOX_IMAGE": "ghcr.io/dirien/skill-ci-sandbox:0.7.0",
        "OPENAI_BASE_URL": "https://api.openai.com/v1",
    }
    assert trial_bundle.secret_values(environ, auth) == {
        "ANTHROPIC_API_KEY": FAKE_KEY,
        "GOOGLE_API_KEY": "judge-key-0000000",
        "SKILL_EVAL_EMBEDDING_API_KEY": "embedding-key-0000",
        "AWS_BEARER_TOKEN_BEDROCK": "bedrock-test-token-0000",
        "HARBOR_PASSWORD": "Harbor-test-pass-0000",
        "REGISTRY_AUTH": secret,
        "REGISTRY_IDENTITYTOKEN": "id-token-0000000",
        "REGISTRY_PASSWORD": "Registry-pass-0000",
        "REGISTRY_PASSWORD_2": "other-pass-0000",
    }
    assert trial_bundle.secret_values({}, tmp_path / "missing.json") == {}
    (tmp_path / "bad.json").write_text("{not json")
    assert trial_bundle.secret_values({}, tmp_path / "bad.json") == {}


@pytest.mark.parametrize(
    ("text", "want", "counts"),
    [
        (f"key {FAKE_KEY}", "key [redacted:K]", (1, 0, 0)),
        (base64.b64encode(FAKE_KEY.encode()).decode(), "[redacted:K]", (1, 0, 0)),
        ("token nvapi-0000000000000", "token nvapi-<redacted>", (0, 1, 0)),
        (
            "k 0123456789abcdef0123456789abcdef.AAAAAAAAAAAAAAAAAAAAAAAAAA",
            "k [redacted]",
            (0, 1, 0),
        ),
        (
            "HOME=/root\nexport My_Token=abc\nPATH=/bin",
            "HOME=/root\nexport My_Token=[redacted]\nPATH=/bin",
            (0, 0, 1),
        ),
        ("DB_PASSWD=x [redacted:K]", "DB_PASSWD=x [redacted:K]", (0, 0, 0)),
        ("MONKEY_COUNT=", "MONKEY_COUNT=", (0, 0, 0)),
        ("plain text", "plain text", (0, 0, 0)),
    ],
)
def test_redactor_rules(text: str, want: str, counts: tuple[int, int, int], monkeypatch) -> None:
    monkeypatch.setattr(trial_bundle, "redact_sensitive_text", fake_redact)
    redactor = Redactor({"K": FAKE_KEY, "SHORT": "abc"})
    assert redactor.text(text) == want
    c = redactor.counts
    assert (c.exact, c.pattern, c.env_dump) == counts
    redactor.reset()
    assert redactor.counts == trial_bundle.RedactionCounts()


def test_walk_keeps_keys_and_structure() -> None:
    redactor = Redactor({"K": FAKE_KEY})
    doc = {"a": [FAKE_KEY, 1, None, True, {"b": f"x{FAKE_KEY}"}], FAKE_KEY: 2.5, "n": float("nan")}
    assert redactor.walk(doc) == {
        "a": ["[redacted:K]", 1, None, True, {"b": "x[redacted:K]"}],
        FAKE_KEY: 2.5,
        "n": None,
    }
    assert redactor.counts.exact == 2
    assert redactor.leaks(json.dumps(doc).encode()) == ["K"]
    assert redactor.leaks(b"nothing") == []
    assert Redactor({}).leaks(b"anything") == []


def test_no_skillevaluator_means_no_redactor(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(trial_bundle, "redact_sensitive_text", None)
    with pytest.raises(RuntimeError, match="skillevaluator"):
        Redactor({})


def test_redact_report_files(tmp_path: Path) -> None:
    (tmp_path / "result.json").write_text(json.dumps({"note": f"key {FAKE_KEY}", "n": 1}))
    (tmp_path / "report.html").write_text(f"<p>{FAKE_KEY}</p>")
    (tmp_path / "BENCHMARK.md").write_text("# clean\n")
    (tmp_path / "validate.json").write_text(json.dumps({FAKE_KEY: "a key as a key"}))
    redactor = Redactor({"ANTHROPIC_API_KEY": FAKE_KEY})
    files = ["report.html", "result.json", "BENCHMARK.md", "validate.json", "tier3.html"]
    assert trial_bundle.redact_report_files(tmp_path, files, redactor) == [
        "report.html",
        "result.json",
        "BENCHMARK.md",
    ]
    assert (
        json.loads((tmp_path / "result.json").read_text())["note"]
        == "key [redacted:ANTHROPIC_API_KEY]"
    )
    assert (tmp_path / "report.html").read_text() == "<p>[redacted:ANTHROPIC_API_KEY]</p>"
    assert (tmp_path / "BENCHMARK.md").read_text() == "# clean\n"


# Shortening and caps


def test_shorten_marker_and_pointer() -> None:
    long = "a" * 100 + "é" * 50  # 200 bytes
    doc = {"x/y": [{"~k": long}, "short"]}
    got, cuts = trial_bundle.shorten(doc, 64, "/trajectory")
    assert cuts == [{"pointer": "/trajectory/x~1y/0/~0k", "originalBytes": 200}]
    value = resolve({"trajectory": got}, cuts[0]["pointer"])
    assert value.startswith("a" * 48 + "\n...[skill-ci: ")
    head, _, rest = value.partition("\n...[skill-ci: ")
    cut, _, tail = rest.partition(" bytes cut]...\n")
    assert tail == "é" * 8  # the last 16 bytes
    assert int(cut) == 200 - len(head.encode()) - len(tail.encode())
    assert got["x/y"][1] == "short"
    assert doc["x/y"][0]["~k"] == long  # the input is not changed


def test_shorten_cuts_on_character_boundaries() -> None:
    got, cuts = trial_bundle.shorten("€" * 100, 40)
    head, _, rest = got.partition("\n...")
    assert set(head) == {"€"}
    assert len(head.encode()) <= 30
    assert rest.endswith("€" * 3)
    assert cuts[0]["originalBytes"] == 300


def test_long_observation_is_shortened_at_its_pointer(tmp_path: Path) -> None:
    run = make_run(
        tmp_path,
        [
            {
                "id": "c__a",
                "arm": "with-skill",
                "case": "c",
                "attempt": 1,
                "observation": "x" * 40000,
            }
        ],
    )
    bundle = run_build(run, tmp_path / "b")
    doc = trial_docs(bundle)["c__a"]
    pointer = "/trajectory/steps/1/observation/results/0/content"
    assert doc["truncations"] == [{"pointer": pointer, "originalBytes": 40000}]
    value = resolve(doc, pointer)
    assert "[skill-ci: 7232 bytes cut]" in value
    assert len(value.encode()) == 32768 + len("\n...[skill-ci: 7232 bytes cut]...\n")
    assert bundle.index["trials"][0]["truncated"] is True
    validate(bundle)


def test_trial_cap_drops_the_log_first(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    with_limits(monkeypatch, trial_bytes=20 << 10)
    run = make_run(
        tmp_path,
        [{"id": "c__a", "arm": "with-skill", "case": "c", "attempt": 1, "log": noise(60000, 1)}],
    )
    bundle = run_build(run, tmp_path / "b")
    doc = trial_docs(bundle)["c__a"]
    assert "agentLog" not in doc
    assert "trajectory" in doc
    entry = bundle.index["trials"][0]
    assert entry["truncated"] is True
    assert entry["bytes"] <= 20 << 10
    validate(bundle)


def test_trial_cap_then_cuts_observations(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    with_limits(monkeypatch, trial_bytes=20 << 10, output_bytes=2048)
    run = make_run(
        tmp_path,
        [
            {
                "id": "c__a",
                "arm": "with-skill",
                "case": "c",
                "attempt": 1,
                "observation": noise(40000, 2),
            }
        ],
    )
    bundle = run_build(run, tmp_path / "b")
    doc = trial_docs(bundle)["c__a"]
    pointer = "/trajectory/steps/1/observation/results/0/content"
    assert doc["truncations"] == [{"pointer": pointer, "originalBytes": 40000}]
    value = resolve(doc, pointer)
    marker = f"\n...[skill-ci: {40000 - 2048} bytes cut]...\n"
    assert marker in value
    assert len(value.encode()) == 2048 + len(marker)
    assert value.startswith(noise(40000, 2)[:1536])
    assert value.endswith(noise(40000, 2)[-512:])
    validate(bundle)


def test_trial_cap_then_leaves_out_the_trajectory(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    with_limits(monkeypatch, trial_bytes=20 << 10, output_bytes=1024)
    run = make_run(
        tmp_path,
        [
            {
                "id": "c__a",
                "arm": "with-skill",
                "case": "c",
                "attempt": 1,
                "observation": "x" * 50000,
                "steps": 12,
            }
        ],
    )
    bundle = run_build(run, tmp_path / "b")
    doc = trial_docs(bundle)["c__a"]
    assert "trajectory" not in doc
    assert "20480 bytes" in doc["trajectoryOmitted"]
    assert not [t for t in doc["truncations"] if t["pointer"].startswith("/trajectory/")]
    assert doc["prompt"] == "Do c.\n"
    entry = bundle.index["trials"][0]
    assert (entry["steps"], entry["toolCalls"], entry["truncated"]) == (14, 1, True)
    validate(bundle)


def test_trial_cap_finally_withholds(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    with_limits(monkeypatch, trial_bytes=4096)
    run = make_run(
        tmp_path,
        [
            {
                "id": "c__a",
                "arm": "with-skill",
                "case": "c",
                "attempt": 1,
                "reward_text": noise(30000, 3),
            },
            {"id": "c__b", "arm": "without-skill", "case": "c", "attempt": 1},
        ],
    )
    bundle = run_build(run, tmp_path / "b")
    assert [t["id"] for t in bundle.index["trials"]] == ["c__b"]
    assert bundle.withheld == [
        {"id": "c__a", "arm": "with-skill", "case": "c", "attempt": 1, "reason": "size"}
    ]
    validate(bundle)


def test_bundle_cap_withholds_the_last_attempts_first(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    trials = [
        {
            "id": f"{case}__{arm[:5]}{n}",
            "arm": arm,
            "case": case,
            "attempt": n,
            "observation": noise(8000, n * 7 + i),
        }
        for i, (case, arm) in enumerate([(c, a) for c in ("c1", "c2") for a in trial_bundle.ARMS])
        for n in (1, 2)
    ]
    run = make_run(tmp_path, trials)
    full = run_build(run, tmp_path / "full")
    sizes = sum(t["bytes"] for t in full.index["trials"])
    assert len(full.index["trials"]) == 8
    with_limits(monkeypatch, bundle_bytes=sizes - 1000)
    bundle = run_build(run, tmp_path / "b")
    assert len(bundle.withheld) == 1
    assert bundle.withheld[0]["attempt"] == 2
    assert bundle.withheld[0]["case"] == "c2"
    assert bundle.withheld[0]["reason"] == "size"
    total = len((tmp_path / "b/index.json").read_bytes()) + sum(
        t["bytes"] for t in bundle.index["trials"]
    )
    assert total <= sizes - 1000
    with_limits(monkeypatch, bundle_bytes=1024)
    assert run_build(run, tmp_path / "c") == trial_bundle.NotBuilt(
        "size", "the index alone is over the bundle limit"
    )


def test_trials_per_arm(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    with_limits(monkeypatch, trials_per_arm=2)
    trials = [
        {"id": f"{case}__w{n}", "arm": "with-skill", "case": case, "attempt": n}
        for case in ("b", "a")
        for n in (1, 2)
    ]
    run = make_run(tmp_path, trials)
    bundle = run_build(run, tmp_path / "b")
    # Case order follows the dataset (a, b), then the attempt.
    assert [t["id"] for t in bundle.index["trials"]] == ["a__w1", "a__w2"]
    assert [(w["id"], w["reason"]) for w in bundle.withheld] == [
        ("b__w1", "limit"),
        ("b__w2", "limit"),
    ]
    validate(bundle)


# Network


def test_network_sections_and_counts(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    with_limits(monkeypatch, flow_rows=2)
    run = make_run(
        tmp_path,
        [
            {"id": "c__a", "arm": "with-skill", "case": "c", "attempt": 1},
            {"id": "c__b", "arm": "without-skill", "case": "c", "attempt": 1},
        ],
    )
    t = "2026-10-30T15:02:11.420Z"
    dns = [{"name": f"h{i}.example", "count": 1, "firstAt": t, "lastAt": t} for i in range(3)]
    flow = {
        "destination": "api.example",
        "ip": "192.0.2.1",
        "port": 443,
        "protocol": "TCP",
        "count": 1,
        "firstAt": t,
        "lastAt": t,
    }
    section = {
        "state": "collected",
        "source": "hubble",
        "dns": dns,
        "flows": [
            {**flow, "verdict": "FORWARDED"},
            {**flow, "verdict": "DROPPED", "reason": "POLICY_DENIED"},
        ],
        "truncated": False,
        "lost": 0,
    }
    bundle = run_build(
        run, tmp_path / "b", network={"state": "collected"}, trial_networks={"c__a": section}
    )
    docs = trial_docs(bundle)
    assert len(docs["c__a"]["network"]["dns"]) == 2
    assert docs["c__a"]["network"]["truncated"] is True
    assert docs["c__a"]["network"]["state"] == "partial"
    assert docs["c__b"]["network"] == {
        "state": "collected",
        "dns": [],
        "flows": [],
        "truncated": False,
        "lost": 0,
    }
    entries = {e["id"]: e for e in bundle.index["trials"]}
    assert entries["c__a"]["network"] == {"forwarded": 1, "dropped": 1, "dns": 2}
    assert entries["c__b"]["network"] == {"forwarded": 0, "dropped": 0, "dns": 0}
    assert bundle.index["network"]["state"] == "partial"
    validate(bundle)


def test_network_off_and_unavailable(tmp_path: Path) -> None:
    run = make_run(tmp_path, [{"id": "c__a", "arm": "with-skill", "case": "c", "attempt": 1}])
    off = run_build(run, tmp_path / "off")
    assert "network" not in trial_docs(off)["c__a"]
    assert "network" not in off.index["trials"][0]
    down = {"state": "unavailable", "reason": "Hubble Relay r:80 did not answer within 10s"}
    bundle = run_build(run, tmp_path / "down", network=down)
    assert trial_docs(bundle)["c__a"]["network"] == {
        **down,
        "dns": [],
        "flows": [],
        "truncated": False,
        "lost": 0,
    }
    assert "network" not in bundle.index["trials"][0]
    assert bundle.index["network"] == down
    validate(bundle)


# Switch, inputs, mapping


def test_disabled_and_no_trials(tmp_path: Path) -> None:
    assert run_build(
        TRIALS / "claude-code-doks/run-dir", tmp_path / "b", environ={"TRIAL_BUNDLE": "off"}
    ) == (trial_bundle.NotBuilt("disabled", "TRIAL_BUNDLE=off"))
    empty = tmp_path / "empty"
    empty.mkdir()
    got = run_build(empty, tmp_path / "b")
    assert isinstance(got, trial_bundle.NotBuilt)
    assert got.reason == "no-trials"


def test_bad_config_raises(tmp_path: Path) -> None:
    config = config_for(json.loads((TRIALS / "claude-code-doks/bundle/index.json").read_text()))
    with pytest.raises(ValueError, match="digest"):
        run_build(
            TRIALS / "claude-code-doks/run-dir",
            tmp_path / "b",
            config={**config, "SKILL_REF": "h/s:v1"},
        )
    with pytest.raises(ValueError, match="agent"):
        run_build(
            TRIALS / "claude-code-doks/run-dir", tmp_path / "b", config={**config, "AGENT": "x"}
        )


def test_index_without_version_and_agent_version(tmp_path: Path) -> None:
    config = config_for(json.loads((TRIALS / "claude-code-doks/bundle/index.json").read_text()))
    del config["SKILL_VERSION"]
    bundle = run_build(
        TRIALS / "claude-code-doks/run-dir", tmp_path / "b", config=config, agent_version=""
    )
    assert "version" not in bundle.index
    assert "agentVersion" not in bundle.index
    assert bundle.index["entry"] == "claude-code/claude-haiku-5-5"
    assert "skillci.dev/agent-version" not in bundle.annotations
    validate(bundle)


def test_unreadable_trial_result_is_an_error_trial(tmp_path: Path) -> None:
    run = make_run(tmp_path, [{"id": "c__a", "arm": "with-skill", "case": "c", "attempt": 1}])
    (run / "claude-code/with-skill/trials/c__a/result.json").write_text("{broken")
    bundle = run_build(run, tmp_path / "b")
    doc = trial_docs(bundle)["c__a"]
    assert doc["status"] == "error"
    assert doc["prompt"] == "Do c."
    assert set(doc) == {
        "schema",
        "id",
        "arm",
        "case",
        "attempt",
        "status",
        "meta",
        "prompt",
        "trajectoryOmitted",
        "truncations",
        "redaction",
    }
    assert bundle.index["trials"][0]["status"] == "error"
    validate(bundle)


def test_invalid_trial_ids_are_left_out(tmp_path: Path) -> None:
    run = make_run(tmp_path, [{"id": "c__a", "arm": "with-skill", "case": "c", "attempt": 1}])
    shutil.copytree(
        run / "claude-code/with-skill/trials/c__a", run / "claude-code/with-skill/trials/-bad id"
    )
    bundle = run_build(run, tmp_path / "b")
    assert [t["id"] for t in bundle.index["trials"]] == ["c__a"]
    assert bundle.withheld == []


def test_trial_map() -> None:
    result = {
        "agents": {
            "codex": {
                "pass_at_k": {
                    "with_skill": {
                        "cases": {
                            "c": {
                                "attempts": [
                                    {"attempt": 1, "trial": "c__a"},
                                    {"attempt": 2, "trial": "c__b"},
                                ]
                            }
                        }
                    },
                    "without_skill": {"cases": None},
                },
                "trial_failures": {
                    "with_skill": [{"trial": "c__x", "reason": "boom"}, {"trial": "c__a"}],
                    "without_skill": [{"trial": "c__y"}, {"trial": "d__z"}, {"trial": "c__w"}],
                },
            }
        }
    }
    assert trial_bundle.trial_map(result, "codex") == {
        ("with-skill", "c__a"): ("c", 1),
        ("with-skill", "c__b"): ("c", 2),
        ("with-skill", "c__x"): ("c", 3),
        ("without-skill", "c__y"): ("c", 1),
        ("without-skill", "d__z"): ("d", 1),
        ("without-skill", "c__w"): ("c", 2),
    }
    assert trial_bundle.trial_map(result, "opencode") == {}
    assert trial_bundle.trial_map({}, "codex") == {}


@pytest.mark.parametrize(
    ("environ", "bundle", "output"),
    [
        ({}, 24 << 20, 8 << 10),
        ({"TRIAL_BUNDLE_MAX_BYTES": "1048576", "TRIAL_OUTPUT_MAX_BYTES": "4096"}, 1 << 20, 4096),
        ({"TRIAL_BUNDLE_MAX_BYTES": "lots", "TRIAL_OUTPUT_MAX_BYTES": "-1"}, 24 << 20, 8 << 10),
        ({"TRIAL_BUNDLE_MAX_BYTES": "10", "TRIAL_OUTPUT_MAX_BYTES": "0"}, 24 << 20, 8 << 10),
    ],
)
def test_limits_from_env(environ: dict[str, str], bundle: int, output: int) -> None:
    limits = trial_bundle.limits_from_env(environ)
    assert (limits.bundle_bytes, limits.output_bytes) == (bundle, output)
    assert limits.as_index()["bundleBytes"] == bundle
    assert set(limits.as_index()) == {
        "stringBytes",
        "outputBytes",
        "logTailBytes",
        "exceptionBytes",
        "trialBytes",
        "bundleBytes",
        "trialsPerArm",
        "flowRows",
    }


@pytest.mark.parametrize(
    ("value", "want"),
    [(None, True), ("on", True), ("off", False), (" OFF ", False), ("false", False)],
)
def test_enabled(value: str | None, want: bool) -> None:
    assert trial_bundle.enabled({} if value is None else {"TRIAL_BUNDLE": value}) is want


def test_oras_files(tmp_path: Path) -> None:
    bundle = run_build(TRIALS / "claude-code-doks/run-dir", tmp_path / "b")
    assert trial_bundle.oras_files(bundle) == [
        "index.json:application/vnd.skillci.trials.index.v1+json",
        "trial-rn-basic__sryqofs.json.gz:application/vnd.skillci.trial.v1+gzip",
        "trial-rn-basic__BQbpoR9.json.gz:application/vnd.skillci.trial.v1+gzip",
    ]


def test_rebuild_replaces_stale_layers(tmp_path: Path) -> None:
    out = tmp_path / "b"
    out.mkdir()
    (out / "trial-old.json.gz").write_bytes(b"stale")
    bundle = run_build(TRIALS / "claude-code-doks/run-dir", out)
    assert sorted(p.name for p in out.iterdir()) == sorted(bundle.files)
