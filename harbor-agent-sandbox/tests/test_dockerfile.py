"""Tests for reading the Dockerfile SkillEvaluator generates, against a real generated one."""

from __future__ import annotations

from pathlib import Path

import pytest

from harbor_agent_sandbox import dockerfile
from harbor_agent_sandbox.dockerfile import DockerfileError, parse, resolve_copy

FIXTURE = Path(__file__).parent / "fixtures" / "skillevaluator-0.6.0.Dockerfile"
SANDBOX_DOCKERFILE = Path(__file__).parents[2] / "sandbox" / "Dockerfile"


@pytest.fixture(scope="module")
def generated() -> str:
    """The Dockerfile SkillEvaluator 0.6.0 wrote for a with-skill task (acme-release-notes)."""
    return FIXTURE.read_text()


def test_generated_dockerfile_is_fully_covered(generated):
    """The Dockerfile of a plain skill needs nothing the sandbox image lacks."""
    plan = parse(generated)
    assert plan.base_image == "python:3.12-slim"
    assert plan.problems() == []
    plan.require_covered()


def test_covered_runs_are_exactly_the_generated_ones(generated):
    """No stale digest stays behind when SkillEvaluator changes a step."""
    assert {run.digest for run in parse(generated).runs} == set(dockerfile.COVERED_RUNS)


def test_generated_dockerfile_settings(generated):
    """WORKDIR, ENV and the COPY steps are what the sandbox has to add at start."""
    plan = parse(generated)
    assert plan.workdir == "/workspace"
    assert plan.user is None
    assert plan.env["CLAUDE_CODE_DISABLE_POLICY_SKILLS"] == "1"
    assert plan.env["LD_PRELOAD"] == ""  # ENV X="" empties the variable
    destinations = [copy.dest for copy in plan.copies]
    assert (
        destinations
        == [
            "/workspace/skills/",
            "/root/.claude/skills/",
            "/root/.agents/skills/",
            "/root/.config/opencode/skills/",
        ]
        * 2
    )
    assert {copy.sources for copy in plan.copies} == {("skills/",)}


def test_sandbox_dockerfile_does_what_the_generated_one_runs(generated):
    """sandbox/Dockerfile carries the pinned verifier install, its smoke test and the mkdir.

    Those three steps are copied verbatim; if SkillEvaluator changes them, the sandbox image
    has to follow, and this test says so.
    """
    # sandbox/Dockerfile uses ARG, which parse() refuses, so read its instructions directly.
    instructions = dockerfile.logical_lines(SANDBOX_DOCKERFILE.read_text())
    runs = [text.partition(" ")[2] for _, text in instructions if text.startswith("RUN ")]
    sandbox_digests = {dockerfile.run_digest(dockerfile.normalize_run(run)) for run in runs}
    verifier = parse(generated).runs[1:4]
    assert all(run.digest in sandbox_digests for run in verifier)
    (apt,) = [run for run in runs if "apt-get" in run]
    for package in ("bash", "curl", "git", "jq", "ripgrep"):
        assert package in apt.split()
    assert instructions[0][1].startswith("FROM python:3.12")


@pytest.mark.parametrize(
    ("change", "expected"),
    [
        # A skill with apt-packages.txt adds packages to the apt step.
        (("jq ripgrep", "jq ripgrep libxml2-dev"), "apt-get"),
        # A skill with requirements.txt adds requirements to the pip step.
        (("boto3>=1.34", "boto3>=1.34 pandas"), "pip"),
        # A custom evals/environment/Dockerfile keeps another base.
        (("FROM python:3.12-slim", "FROM node:22-slim"), "FROM node:22-slim"),
    ],
)
def test_changed_step_is_reported(generated, change, expected):
    """A RUN or FROM the image does not have is named in the error."""
    old, new = change
    assert old in generated
    plan = parse(generated.replace(old, new, 1))
    assert plan.problems()
    with pytest.raises(DockerfileError, match="prebuilt sandbox image") as raised:
        plan.require_covered()
    assert expected in str(raised.value)


def test_error_lists_the_first_steps_only(generated):
    """Many missing steps are summarized, with the count."""
    extra = "".join(f"RUN echo {n}\n" for n in range(6))
    with pytest.raises(DockerfileError, match=r"needs 6 step\(s\).*and 3 more"):
        parse(generated + extra).require_covered()


def test_run_normalization_ignores_layout_but_not_content():
    """Whitespace and continuation lines do not matter, a changed word does."""
    a = parse("FROM x\nRUN apt-get   install -y \\\n    curl git\n")
    b = parse("FROM x\nRUN apt-get install -y curl git\n")
    c = parse("FROM x\nRUN apt-get install -y curl\n")
    assert a.runs[0].digest == b.runs[0].digest != c.runs[0].digest
    exec_form = parse('FROM x\nRUN [ "echo",  "a" ]\n')
    assert exec_form.runs[0].command == 'exec:["echo","a"]'


def test_instruction_forms():
    """ENV both ways, relative WORKDIR, USER, flags, JSON form, comments inside continuations."""
    plan = parse(
        "# a comment\n"
        "FROM python:3.12-slim\n"
        'ENV A=1 B="two words" C=\n'
        "ENV legacy value with spaces\n"
        "ENV D=$A-${B}\n"
        "WORKDIR /srv\n"
        "WORKDIR app\n"
        "USER 1000:1000\n"
        "COPY --chown=1000:1000 --chmod=755 a.txt b.txt dest/\n"
        'COPY ["with space.txt", "/abs/"]\n'
        "RUN echo one \\\n"
        "    # comment inside\n"
        "    && echo two\n"
        "LABEL x=y\n"
        "EXPOSE 80\n"
        "ENTRYPOINT []\n"
        "HEALTHCHECK NONE\n"
    )
    assert plan.env == {
        "A": "1",
        "B": "two words",
        "C": "",
        "legacy": "value with spaces",
        "D": "1-two words",
    }
    assert plan.workdir == "/srv/app"
    assert plan.user == "1000"
    first, second = plan.copies
    assert (first.sources, first.dest, first.chown, first.chmod) == (
        ("a.txt", "b.txt"),
        "/srv/app/dest/",
        "1000:1000",
        "755",
    )
    assert (second.sources, second.dest) == (("with space.txt",), "/abs/")
    assert plan.runs[0].command == "sh:echo one && echo two"
    assert plan.runs[0].line == 11


def test_add_of_local_files_is_copy():
    """ADD of plain local files and directories behaves like COPY."""
    plan = parse("FROM x\nWORKDIR /w\nADD config.json app/\nADD data /data\n")
    assert [(c.sources, c.dest) for c in plan.copies] == [
        (("config.json",), "/w/app/"),
        (("data",), "/data"),
    ]


@pytest.mark.parametrize(
    ("text", "message"),
    [
        ("RUN echo\n", "must start with FROM"),
        ("", "no FROM"),
        ("FROM a\nFROM b\n", "multi-stage"),
        ("FROM --platform=linux/amd64 a\n", "FROM options"),
        ("FROM a\nARG X=1\n", "ARG is not supported"),
        ('FROM a\nSHELL ["/bin/zsh"]\n', "SHELL is not supported"),
        ("FROM a\nFROBNICATE x\n", "unknown instruction"),
        ("FROM a\nRUN cat <<EOF\nx\nEOF\n", "here-documents"),
        ("FROM a\nCOPY --from=build /a /b\n", "--from is not supported"),
        ("FROM a\nCOPY onlyone\n", "needs a source and a destination"),
        ("FROM a\nADD https://example.com/x /x\n", "remote source"),
        ("FROM a\nADD pkg.tar.gz /opt/\n", "unpack the archive"),
        ("# escape=`\nFROM a\n", "escape character"),
        ("FROM a\nENV oops\n", "name and a value"),
        ('FROM a\nCOPY ["a", 1]\n', "JSON array of strings"),
    ],
)
def test_unsupported_dockerfiles_fail_clearly(text, message):
    """What cannot be replayed is an error with a line number, not a silent skip."""
    with pytest.raises(DockerfileError, match=message):
        parse(text)


def make_context(tmp_path: Path) -> Path:
    """Create a build context like SkillEvaluator's: skills/, input/ and loose files."""
    context = tmp_path / "ctx"
    (context / "skills" / "demo" / "scripts").mkdir(parents=True)
    (context / "skills" / "demo" / "SKILL.md").write_text("skill")
    (context / "skills" / "demo" / "scripts" / "run.sh").write_text("echo")
    (context / "input").mkdir()
    (context / "a.txt").write_text("a")
    (context / "b.txt").write_text("b")
    (context / "empty").mkdir()
    return context


def transfers(tmp_path: Path, copy_line: str, workdir: str = "/workspace"):
    """Resolve the COPY line of a one-line Dockerfile against a fresh context."""
    plan = parse(f"FROM x\nWORKDIR {workdir}\n{copy_line}\n")
    context = make_context(tmp_path)
    return [t for copy in plan.copies for t in resolve_copy(copy, context)], context


def test_copy_of_a_directory_copies_its_contents(tmp_path):
    """COPY skills/ /workspace/skills/ puts the contents of skills/ into the target."""
    (transfer,), context = transfers(tmp_path, "COPY skills/ /workspace/skills/")
    assert transfer.is_dir
    assert transfer.source == (context / "skills").resolve()
    assert transfer.target == "/workspace/skills"


def test_copy_of_an_empty_directory_still_creates_the_target(tmp_path):
    """The no-skill baseline has an empty skills/; the transfer exists so the target is made."""
    (transfer,), _ = transfers(tmp_path, "COPY empty/ /workspace/skills/")
    assert transfer.is_dir
    assert not any(transfer.source.iterdir())


@pytest.mark.parametrize(
    ("line", "targets", "maybe_dir"),
    [
        ("COPY a.txt /opt/x.txt", ["/opt/x.txt"], True),
        ("COPY a.txt /opt/", ["/opt/a.txt"], False),
        ("COPY a.txt b.txt /opt", ["/opt/a.txt", "/opt/b.txt"], False),
        ("COPY *.txt rel/", ["/workspace/rel/a.txt", "/workspace/rel/b.txt"], False),
    ],
)
def test_copy_of_files(tmp_path, line, targets, maybe_dir):
    """A file copied to a path may land in a directory; several sources always do."""
    found, _ = transfers(tmp_path, line)
    assert [t.target for t in found] == targets
    assert not any(t.is_dir for t in found)
    assert all(t.maybe_dir is maybe_dir for t in found)


def test_copy_flags_travel_with_the_transfer(tmp_path):
    """--chown and --chmod reach the upload step."""
    (transfer,), _ = transfers(tmp_path, "COPY --chown=1:2 --chmod=0644 a.txt /opt/")
    assert (transfer.chown, transfer.chmod) == ("1:2", "0644")


@pytest.mark.parametrize(
    ("line", "message"),
    [
        ("COPY missing/ /x/", "not in the build context"),
        ("COPY *.nothing /x/", "not in the build context"),
        ("COPY ../outside /x/", "not in the build context|leaves the build context"),
    ],
)
def test_copy_sources_must_exist_inside_the_context(tmp_path, line, message):
    """A missing source or one outside the context fails like `docker build` would."""
    with pytest.raises(DockerfileError, match=message):
        transfers(tmp_path, line)


def test_copy_cannot_follow_a_symlink_out_of_the_context(tmp_path):
    """A link to a file outside the context is refused."""
    context = make_context(tmp_path)
    outside = tmp_path / "secret.txt"
    outside.write_text("x")
    (context / "link.txt").symlink_to(outside)
    plan = parse("FROM x\nCOPY link.txt /x\n")
    with pytest.raises(DockerfileError, match="leaves the build context"):
        resolve_copy(plan.copies[0], context)


def test_digests_command_prints_the_covered_runs(capsys):
    """`python -m harbor_agent_sandbox.dockerfile digests` lists what to paste into COVERED_RUNS."""
    assert dockerfile._main(["x", "digests", str(FIXTURE)]) == 0
    out = capsys.readouterr().out
    assert out.startswith("FROM python:3.12-slim")
    assert all(digest in out for digest in dockerfile.COVERED_RUNS)
    assert dockerfile._main(["x"]) == 2
