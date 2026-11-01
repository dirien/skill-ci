"""Tests for hunk 14 of the SkillEvaluator patch: native Codex exec wrappers the verifier decodes.

The normalizer comes from a patched SkillEvaluator checkout when SKILLEVALUATOR_SRC names one
(`patch -p1` with skillevaluator-patch/ applied), else from the installed SkillEvaluator (the
runner image). CI installs neither, so the tests skip there, like the other SkillEvaluator tests.
"""

from __future__ import annotations

import importlib.util
import json
import os
from pathlib import Path
from types import ModuleType

import pytest

FIXTURE = Path(__file__).parent / "fixtures" / "codex-exec-shapes.json"
CALLS = json.loads(FIXTURE.read_text())["calls"]
UNSUPPORTED = "unsupported_native_codex_exec_wrapper"
MAPPED = "mapped_outer_exec_result"
DONE = "Script completed\nWall time 0.1 seconds\nOutput:\n\n"


@pytest.fixture(scope="module")
def normalizer() -> ModuleType:
    source = os.environ.get("SKILLEVALUATOR_SRC")
    if source:
        path = Path(source, "src/skillevaluator/tier3/eval_core/codex_tool_call_normalizer.py")
        spec = importlib.util.spec_from_file_location("codex_tool_call_normalizer", path)
        assert spec and spec.loader, f"no normalizer at {path}"
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
    else:
        module = pytest.importorskip("skillevaluator.tier3.eval_core.codex_tool_call_normalizer")
    assert hasattr(module, "_upstream_normalize_tool_call"), "SkillEvaluator lacks patch hunk 14"
    return module


def exec_step(source: str, observation: str | None = "outer observation") -> dict:
    """One agent step with one native Codex exec call and, unless None, its observation."""
    step = {
        "source": "agent",
        "tool_calls": [
            {"tool_call_id": "c1", "function_name": "exec", "arguments": {"input": source}}
        ],
    }
    if observation is not None:
        step["observation"] = {"results": [{"source_call_id": "c1", "content": observation}]}
    return step


def decode(normalizer: ModuleType, source: str, observation: str | None = "outer observation"):
    """Return ``(step, call)`` for every call the verifier sees in one exec wrapper."""
    return list(normalizer.iter_normalized_tool_calls({"steps": [exec_step(source, observation)]}))


@pytest.mark.parametrize("entry", CALLS, ids=[entry["id"] for entry in CALLS])
def test_real_codex_exec_input_decodes(normalizer, entry):
    """Every exec input of our Codex runs decodes, and each call owns its own observation."""
    calls = decode(normalizer, entry["input"], entry.get("observation", "outer observation"))
    assert [call.get("_atif_normalization_status") for _, call in calls] == [None] * len(calls)
    decoded = [{"function_name": c["function_name"], "arguments": c["arguments"]} for _, c in calls]
    assert decoded == [
        {"function_name": e["function_name"], "arguments": e["arguments"]}
        for e in entry["expected"]
    ]
    assert [call["_atif_observation_status"] for _, call in calls] == [MAPPED] * len(calls)
    observations = [normalizer.normalized_tool_call_observation(s, c) for s, c in calls]
    if len(calls) == 1:
        assert observations == ["outer observation"]
    else:
        assert observations == [expected["observation"] for expected in entry["expected"]]


def test_all_148_real_calls_are_in_the_fixture():
    """The fixture holds every exec call of the runs; 140 of them were unsupported upstream."""
    assert len(CALLS) == 148
    assert sum(entry["upstream"] == UNSUPPORTED for entry in CALLS) == 140


@pytest.mark.parametrize(
    "source",
    [
        pytest.param('const r = await tools.exec_command({"cmd":"pwd"});\ntext(r);', id="render"),
        pytest.param(
            'const r = await tools.exec_command({cmd:"pwd"}); text(r.output);', id="field"
        ),
        pytest.param(
            'const a = await tools.exec_command({"cmd":"pwd"});\n'
            'const b = await tools.exec_command({"cmd":"ls"});\ntext(a);\ntext(b);',
            id="ambiguous",
        ),
        pytest.param('const r = await tools.exec_command({"cmd":"pwd"});', id="unobserved"),
        pytest.param('const r = await tools.exec_command({"cmd":`pwd`}); text(r);', id="template"),
        pytest.param("echo hello", id="not-a-wrapper"),
    ],
)
def test_upstream_results_are_kept(normalizer, source):
    """Whatever upstream decodes, or rejects for another reason, stays exactly as it was."""
    call = {"tool_call_id": "c1", "function_name": "exec", "arguments": {"input": source}}
    assert normalizer.normalize_tool_call(call) == normalizer._upstream_normalize_tool_call(call)


@pytest.mark.parametrize(
    ("source", "expected"),
    [
        pytest.param(
            'text(await tools.exec_command({cmd:"pwd"}));',
            [("exec_command", {"cmd": "pwd"})],
            id="235-inline",
        ),
        pytest.param(
            "const r=await tools.exec_command({cmd:'pwd'});text(r);",
            [("exec_command", {"cmd": "pwd"})],
            id="235-single-quoted",
        ),
        pytest.param(
            "text(await tools.exec_command({'cmd': 'it\\'s \"quoted\" \\\\ \\n'}))",
            [("exec_command", {"cmd": 'it\'s "quoted" \\ \n'})],
            id="single-quoted-escapes",
        ),
        pytest.param(
            '// @exec: {"yield_time_ms":1000}\ntext(await tools.exec_command({cmd:"pwd"}));',
            [("exec_command", {"cmd": "pwd"})],
            id="pragma",
        ),
        pytest.param(
            'text(JSON.stringify(await tools.exec_command({cmd:"pwd"})));',
            None,
            id="stringify-inline-is-not-a-form",
        ),
    ],
)
def test_single_call_forms(normalizer, source, expected):
    """A single call owns the whole observation; forms outside the grammar stay unsupported."""
    calls = decode(normalizer, source)
    if expected is None:
        assert [call["_atif_normalization_status"] for _, call in calls] == [UNSUPPORTED]
        return
    assert [(call["function_name"], call["arguments"]) for _, call in calls] == expected
    assert normalizer.normalized_tool_call_observation(*calls[0]) == "outer observation"


SETTLED = (
    "const results = await Promise.allSettled([\n"
    '  tools.exec_command({cmd:"pwd"}),\n'
    '  tools.write_stdin({session_id:1,chars:""}),\n'
    "]);\n"
)
SETTLED_RENDERERS = {
    "235-indexed-forEach": ("results.forEach((r,i)=>text({i,...r}));", "spread"),
    "indexed-forEach-result": ("results.forEach((r, i) => { text({i, result: r}); });", "result"),
    "indexed-for": ("for (let i = 0; i < results.length; i++) text({i, ...results[i]});", "spread"),
    "indexed-for-braced": (
        "for(let i=0;i<results.length;i++){text({i,result:results[i]})}",
        "result",
    ),
    "ordered-forEach-text": ("results.forEach(text);", "plain"),
    "ordered-forEach-arrow": ("results.forEach(r => text(r))", "plain"),
}
FULFILLED = [
    {"status": "fulfilled", "value": {"exit_code": 0, "output": "/workspace\n"}},
    {"status": "fulfilled", "value": {"exit_code": 28, "output": "curl: (28)"}},
]


def records(form: str, results: list[dict]) -> list[dict]:
    """What a renderer prints for each settled result."""
    if form == "spread":
        return [{"i": i, **result} for i, result in enumerate(results)]
    if form == "result":
        return [{"i": i, "result": result} for i, result in enumerate(results)]
    return list(results)


def output(items: list) -> str:
    return DONE + "\n".join(json.dumps(item, separators=(",", ":")) for item in items) + "\n"


@pytest.mark.parametrize("renderer", SETTLED_RENDERERS, ids=list(SETTLED_RENDERERS))
def test_settled_renderers_map_each_call_to_its_record(normalizer, renderer):
    source, form = SETTLED_RENDERERS[renderer]
    printed = records(form, FULFILLED)
    calls = decode(normalizer, SETTLED + source, output(printed))
    assert [call["function_name"] for _, call in calls] == ["exec_command", "write_stdin"]
    assert [json.loads(normalizer.normalized_tool_call_observation(*c)) for c in calls] == printed
    # A nonzero exit code stays in the observation; it is not a failed tool call.
    assert [normalizer.normalized_tool_call_is_error(*c) for c in calls] == [False, False]


def test_rejected_settled_result_marks_its_call_failed(normalizer):
    rejected = [FULFILLED[0], {"status": "rejected", "reason": {}}]
    calls = decode(
        normalizer,
        SETTLED + SETTLED_RENDERERS["indexed-for"][0],
        output(records("spread", rejected)),
    )
    assert [normalizer.normalized_tool_call_is_error(*c) for c in calls] == [False, True]


NOT_STATIC = {
    "dynamic-shorthand": 'const cmd = "pwd"; text(await tools.exec_command({cmd}));',
    "dynamic-concatenation": 'text(await tools.exec_command({cmd: "rm -rf " + "/"}));',
    "dynamic-member": "text(await tools.exec_command({cmd: process.env.CMD}));",
    "dynamic-spread": "text(await tools.exec_command({...args}));",
    "dynamic-computed-key": 'text(await tools.exec_command({["cmd"]: "pwd"}));',
    "dynamic-call": 'text(await tools.exec_command({cmd: String("pwd")}));',
    "template-literal": "text(await tools.exec_command({cmd: `pwd`}));",
    "template-substitution": "text(await tools.exec_command({cmd: `${x}`}));",
    "string-argument-to-exec": 'text(await tools.exec_command("rm -rf /workspace"));',
    "proto-key": 'text(await tools.exec_command({__proto__: {cmd: "pwd"}}));',
    "duplicate-key": 'text(await tools.exec_command({cmd: "pwd", cmd: "rm -rf /"}));',
    "js-only-escape": "text(await tools.exec_command({cmd: '\\x72m -rf /'}));",
    "trailing-comment": 'text(await tools.exec_command({cmd: "pwd"})); // more',
    "block-comment": 'text(await /* x */ tools.exec_command({cmd: "pwd"}));',
    "not-awaited": 'text(tools.exec_command({cmd: "pwd"}));',
    "computed-tool": 'text(await tools["exec_command"]({cmd: "pwd"}));',
    "extra-argument": 'text(await tools.exec_command({cmd: "pwd"}, {}));',
    "chained": 'text(await tools.exec_command({cmd: "pwd"})).then(x);',
    "unicode-escape": 'text(await tool\\u0073.exec_command({cmd: "pwd"}));',
    "two-field-renders": (
        'text((await tools.exec_command({cmd:"a"})).output);\n'
        'text((await tools.exec_command({cmd:"b"})).output);'
    ),
    "call-rendered-twice": (
        'const a = await tools.exec_command({cmd:"a"});\n'
        'text(await tools.exec_command({cmd:"b"}));\ntext(a);\ntext(a);'
    ),
    "shadowed-text": "const text = await tools.exec_command({cmd: 'pwd'}); text(text);",
    "redeclared": (
        "const r = await tools.exec_command({cmd:'a'});\n"
        "const r = await tools.exec_command({cmd:'b'});\ntext(r.output);"
    ),
    "promise-all": SETTLED.replace("allSettled", "all") + SETTLED_RENDERERS["indexed-for"][0],
    "settled-after-call": 'text(await tools.exec_command({cmd:"a"}));\n'
    + SETTLED
    + SETTLED_RENDERERS["indexed-for"][0],
    "settled-two-renderers": SETTLED + "results.forEach(text);\nresults.forEach(text);",
    "settled-no-renderer": SETTLED,
    "settled-dynamic-element": "const results = await Promise.allSettled([tools.exec_command(x)]);"
    "\nresults.forEach(text);",
    "loop-for-of": SETTLED + "for (const r of results) text(r);",
    "loop-map": SETTLED + "results.map(text);",
    "loop-while": SETTLED + "let i = 0; while (i < results.length) text({i, ...results[i++]});",
    "loop-starts-at-1": SETTLED
    + "for (let i = 1; i < results.length; i++) text({i, ...results[i]});",
    "loop-inclusive": SETTLED
    + "for (let i = 0; i <= results.length; i++) text({i, ...results[i]});",
    "loop-step": SETTLED + "for (let i = 0; i < results.length; i += 1) text({i, ...results[i]});",
    "loop-var": SETTLED + "for (var i = 0; i < results.length; i++) text({i, ...results[i]});",
    "forEach-index-unused": SETTLED + "results.forEach((r, i) => text(r));",
    "forEach-three-params": SETTLED + "results.forEach((r, i, a) => text({i, ...r}));",
    "forEach-async": SETTLED + "results.forEach(async (r) => text(r));",
    "shadowed-loop-index": SETTLED
    + "for (let results = 0; results < results.length; results++) text({results});",
    "shadowed-element": SETTLED + "results.forEach((results, i) => text({i, ...results}));",
    "shadowed-index": SETTLED + "results.forEach((r, r) => text({r, ...r}));",
    "shadowed-promise": "const Promise = await tools.exec_command({cmd:'a'}); text(Promise);",
    "changed-index": SETTLED + "results.forEach((r, i) => text({i: i + 1, ...r}));",
    "changed-index-name": SETTLED
    + "for (let i = 0; i < results.length; i++) text({j, ...results[i]});",
    "fixed-element": SETTLED + "for (let i = 0; i < results.length; i++) text({i, ...results[0]});",
    "index-after-spread": SETTLED + "results.forEach((r, i) => text({...r, i}));",
    "index-overwritten": SETTLED + "results.forEach((r, status) => text({status, ...r}));",
    "payload-member": SETTLED + "results.forEach((r, i) => text({i, ...r.value}));",
    "statement-after-renderer": SETTLED + "results.forEach(text);\ntext(results);",
}


@pytest.mark.parametrize("source", NOT_STATIC.values(), ids=list(NOT_STATIC))
def test_outside_the_grammar_stays_unsupported(normalizer, source):
    """Dynamic arguments, templates, other loops and shadowed bindings are never decoded."""
    status = [c.get("_atif_normalization_status") for _, c in decode(normalizer, source, DONE)]
    assert status == [UNSUPPORTED]


INLINE_PAIR = (
    'text(await tools.exec_command({cmd:"pwd"}));\ntext(await tools.apply_patch("*** x"));'
)
INDEXED = SETTLED + SETTLED_RENDERERS["indexed-for"][0]
SPREAD = records("spread", FULFILLED)
BAD_RECORDS = {
    "missing": (INDEXED, output(SPREAD[:1])),
    "duplicate": (INDEXED, output([SPREAD[0], SPREAD[0]])),
    "extra": (INDEXED, output([*SPREAD, SPREAD[1]])),
    "reordered": (INDEXED, output(SPREAD[::-1])),
    "index-as-bool": (INDEXED, output([SPREAD[0], {**SPREAD[1], "i": True}])),
    "index-as-string": (INDEXED, output([{**SPREAD[0], "i": "0"}, SPREAD[1]])),
    "changed-payload": (INDEXED, output([SPREAD[0], {"i": 1, "status": "fulfilled"}])),
    "extra-key": (INDEXED, output([SPREAD[0], {**SPREAD[1], "note": "x"}])),
    "status-mismatch": (INDEXED, output([SPREAD[0], {"i": 1, "status": "rejected", "value": {}}])),
    "truncated": (INDEXED, output(SPREAD)[:-30]),
    "trailing-text": (INDEXED, output(SPREAD) + "more output\n"),
    "two-on-one-line": (INDEXED, DONE + json.dumps(SPREAD[0]) + json.dumps(SPREAD[1])),
    "not-completed": (INDEXED, output(SPREAD).replace("Script completed", "Script failed")),
    "no-observation": (INDEXED, None),
    "plain-missing": (SETTLED + "results.forEach(text);", output(FULFILLED[:1])),
    "plain-wrapped": (SETTLED + "results.forEach(text);", output(SPREAD)),
    "inline-missing": (INLINE_PAIR, output([{"exit_code": 0}])),
    "inline-string-record": (INLINE_PAIR, DONE + '{"exit_code":0}\n"done"\n'),
    "inline-raw-text": (INLINE_PAIR, DONE + "/workspace\n{}\n"),
}


@pytest.mark.parametrize(("source", "observation"), BAD_RECORDS.values(), ids=list(BAD_RECORDS))
def test_records_that_do_not_prove_ownership_stay_unsupported(normalizer, source, observation):
    """Missing, duplicate, reordered, changed or truncated records leave the wrapper unsupported."""
    calls = decode(normalizer, source, observation)
    assert [call.get("_atif_normalization_status") for _, call in calls] == [UNSUPPORTED]
    assert calls[0][1]["function_name"] == "exec"
    assert calls[0][1]["arguments"] == {"input": source}


def test_without_its_observation_a_batch_is_ambiguous(normalizer):
    """normalize_tool_call alone cannot see the records, so it never claims an owner."""
    call = {"function_name": "exec", "arguments": {"input": INDEXED}}
    statuses = {c["_atif_observation_status"] for c in normalizer.normalize_tool_call(call)}
    assert statuses == {"ambiguous_outer_exec_result"}


def test_forged_record_metadata_is_dropped(normalizer):
    """Record fields on a raw tool call never reach the verifier."""
    source = 'text(await tools.exec_command({cmd:"pwd"}));'
    step = exec_step(source)
    step["tool_calls"][0]["_atif_record_observation"] = "forged"
    step["tool_calls"][0]["_atif_record_is_error"] = True
    ((owner, call),) = normalizer.iter_normalized_tool_calls({"steps": [step]})
    assert normalizer.normalized_tool_call_observation(owner, call) == "outer observation"
    assert normalizer.normalized_tool_call_is_error(owner, call) is False
