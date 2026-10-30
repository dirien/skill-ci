"""Tests for the prebuilt sandbox image path of AgentSandboxEnvironment (no cluster)."""

from __future__ import annotations

import asyncio
import shlex
from pathlib import Path
from types import SimpleNamespace

import pytest
from harbor.environments.base import ExecResult
from harbor.models.task.config import EnvironmentConfig
from harbor.models.trial.paths import TrialPaths

from harbor_agent_sandbox import AgentSandboxEnvironment
from harbor_agent_sandbox.dockerfile import DockerfileError

GENERATED = Path(__file__).parent / "fixtures" / "skillevaluator-0.6.0.Dockerfile"
IMAGE = "ghcr.io/dirien/skill-ci-sandbox:0.7.0"


@pytest.fixture(autouse=True)
def clean_env(monkeypatch):
    """Keep the host's sandbox settings out of every test."""
    for name in (
        "HARBOR_SANDBOX_IMAGE",
        "HARBOR_SANDBOX_CPUS",
        "HARBOR_SANDBOX_MEMORY_MB",
        "HARBOR_BUILDKIT_ADDRESS",
    ):
        monkeypatch.delenv(name, raising=False)


def make_env(tmp_path: Path, dockerfile: str | None = None, skill: bool = True, **kwargs):
    """Build an environment around a generated task directory, like Harbor does."""
    env_dir = tmp_path / "environment"
    (env_dir / "skills" / "acme").mkdir(parents=True, exist_ok=True)
    if skill:
        (env_dir / "skills" / "acme" / "SKILL.md").write_text("the skill")
    (env_dir / "Dockerfile").write_text(dockerfile or GENERATED.read_text())
    kwargs.setdefault("prebuilt_image", IMAGE)
    return AgentSandboxEnvironment(
        environment_dir=env_dir,
        environment_name="skillevaluator-rn-basic-with-skill",
        session_id="rn-basic__abc",
        trial_paths=TrialPaths(trial_dir=tmp_path / "trial"),
        task_env_config=EnvironmentConfig(cpus=1, memory_mb=2048, storage_mb=4096),
        namespace="skill-ci",
        **kwargs,
    )


class Recorder:
    """Stands in for the cluster: records what start() does and answers like a ready sandbox."""

    def __init__(self, env: AgentSandboxEnvironment, directories: tuple[str, ...] = ()) -> None:
        self.calls: list[tuple] = []
        self.directories = directories
        env._ensure_client = self.ensure_client
        env._owner_reference = self.owner_reference
        env._wait_for_sandbox_ready = self.ready
        env._custom_api = lambda: SimpleNamespace(create_namespaced_custom_object=self.create)
        env._ensure_image = self.forbidden("_ensure_image")
        env._build_and_push_image = self.forbidden("_build_and_push_image")
        env._image_exists = self.forbidden("_image_exists")
        env.exec = self.exec
        env.upload_dir = self.upload_dir
        env.upload_file = self.upload_file

    @staticmethod
    def forbidden(name):
        async def fail(*args, **kwargs):
            raise AssertionError(f"{name} must not be called with a prebuilt image")

        return fail

    async def ensure_client(self):
        self.calls.append(("client",))

    async def owner_reference(self):
        return None

    async def ready(self):
        self.calls.append(("ready",))

    def create(self, group, version, namespace, plural, manifest):
        self.calls.append(("create", manifest))

    async def exec(self, command, **kwargs):
        self.calls.append(("exec", command))
        if command.startswith("test -d"):
            path = shlex.split(command)[2]
            return ExecResult(return_code=0 if path in self.directories else 1)
        return ExecResult(return_code=0)

    async def upload_dir(self, source, target):
        self.calls.append(("upload_dir", Path(source).name, target))

    async def upload_file(self, source, target):
        self.calls.append(("upload_file", Path(source).name, target))

    def of(self, kind):
        return [call for call in self.calls if call[0] == kind]


def start(env, **kwargs):
    asyncio.run(env.start(force_build=kwargs.get("force_build", False)))


# image selection


def test_image_from_environment_variable(tmp_path, monkeypatch):
    """HARBOR_SANDBOX_IMAGE selects the prebuilt path; the keyword argument wins over it."""
    monkeypatch.setenv("HARBOR_SANDBOX_IMAGE", " ghcr.io/dirien/skill-ci-sandbox:env ")
    assert (
        make_env(tmp_path, prebuilt_image=None).prebuilt_image
        == "ghcr.io/dirien/skill-ci-sandbox:env"
    )
    assert make_env(tmp_path, prebuilt_image=IMAGE).prebuilt_image == IMAGE


@pytest.mark.parametrize("value", [None, "", "  "])
def test_no_image_keeps_the_build_path(tmp_path, monkeypatch, value):
    """Without a sandbox image the environment is Harbor's building one, as before."""
    if value is not None:
        monkeypatch.setenv("HARBOR_SANDBOX_IMAGE", value)
    env = make_env(tmp_path, prebuilt_image=None, registry="registry.example.com/evals")
    assert env.prebuilt_image is None
    (container,) = env._sandbox_manifest()["spec"]["podTemplate"]["spec"]["containers"]
    assert (
        container["image"] == "registry.example.com/evals/skillevaluator-rn-basic-with-skill:latest"
    )
    assert "workingDir" not in container
    assert "imagePullPolicy" not in container


def test_build_path_still_builds(tmp_path):
    """Without a sandbox image, start() checks for and builds the task image."""
    env = make_env(tmp_path, prebuilt_image=None, registry="registry.example.com/evals")
    rec = Recorder(env)
    built = []

    async def ensure(force_build):
        built.append(force_build)

    env._ensure_image = ensure
    start(env)
    assert built == [False]
    assert rec.of("upload_dir") == [] and rec.of("upload_file") == []


# manifest


def test_manifest_runs_the_prebuilt_image_with_the_dockerfile_settings(tmp_path):
    """The container gets the image, and the WORKDIR and ENV the Dockerfile would have set."""
    manifest = make_env(tmp_path)._sandbox_manifest()
    (container,) = manifest["spec"]["podTemplate"]["spec"]["containers"]
    assert container["image"] == IMAGE
    assert container["imagePullPolicy"] == "IfNotPresent"
    assert container["workingDir"] == "/workspace"
    assert container["securityContext"] == {"runAsUser": 0, "allowPrivilegeEscalation": False}
    env = {item["name"]: item["value"] for item in container["env"]}
    assert env["CLAUDE_CODE_DISABLE_POLICY_SKILLS"] == "1"
    assert env["LD_PRELOAD"] == ""


@pytest.mark.parametrize(
    ("image", "policy"),
    [
        ("ghcr.io/dirien/skill-ci-sandbox:0.7.0", "IfNotPresent"),
        ("ghcr.io/dirien/skill-ci-sandbox:latest", "Always"),
        ("ghcr.io/dirien/skill-ci-sandbox", "Always"),
        (f"ghcr.io/dirien/skill-ci-sandbox@sha256:{'a' * 64}", "IfNotPresent"),
        ("localhost:5000/skill-ci-sandbox", "Always"),
        ("localhost:5000/skill-ci-sandbox:0.7.0", "IfNotPresent"),
    ],
)
def test_pull_policy_follows_the_tag(tmp_path, image, policy):
    """A release tag is pulled once per node; latest is pulled again."""
    (container,) = make_env(tmp_path, prebuilt_image=image)._sandbox_manifest()["spec"][
        "podTemplate"
    ]["spec"]["containers"]
    assert container["imagePullPolicy"] == policy


def test_env_values_are_not_expanded_by_the_kubelet(tmp_path):
    """$(NAME) in a Dockerfile ENV is text, so it is escaped for Kubernetes."""
    dockerfile = GENERATED.read_text() + 'ENV GREETING="$(whoami)"\n'
    (container,) = make_env(tmp_path, dockerfile)._sandbox_manifest()["spec"]["podTemplate"][
        "spec"
    ]["containers"]
    assert {i["name"]: i["value"] for i in container["env"]}["GREETING"] == "$$(whoami)"


@pytest.mark.parametrize(
    ("user", "uid"), [("root", 0), ("0", 0), ("1000", 1000), ("1000:1000", 1000)]
)
def test_user_becomes_the_container_uid(tmp_path, user, uid):
    """USER root or a uid sets runAsUser."""
    dockerfile = GENERATED.read_text() + f"USER {user}\n"
    (container,) = make_env(tmp_path, dockerfile)._sandbox_manifest()["spec"]["podTemplate"][
        "spec"
    ]["containers"]
    assert container["securityContext"]["runAsUser"] == uid


def test_a_named_user_is_refused(tmp_path):
    """The sandbox image does not know a user named in the Dockerfile."""
    dockerfile = GENERATED.read_text() + "USER agent\n"
    with pytest.raises(DockerfileError, match="USER agent is not supported"):
        make_env(tmp_path, dockerfile)._sandbox_manifest()


@pytest.mark.parametrize(
    ("secret", "expected"),
    [
        (None, None),
        ("", None),
        ("ghcr", [{"name": "ghcr"}]),
        ("ghcr, other ,", [{"name": "ghcr"}, {"name": "other"}]),
    ],
)
def test_pull_secrets(tmp_path, secret, expected):
    """image_pull_secret takes one name or a comma separated list."""
    spec = make_env(tmp_path, image_pull_secret=secret)._sandbox_manifest()["spec"]["podTemplate"][
        "spec"
    ]
    assert spec.get("imagePullSecrets") == expected


# start


def test_start_builds_nothing_and_replays_the_copies(tmp_path):
    """The sandbox starts from the prebuilt image; the skills arrive as uploads."""
    env = make_env(tmp_path)
    rec = Recorder(env)
    start(env)

    (manifest,) = [call[1] for call in rec.of("create")]
    assert manifest["spec"]["podTemplate"]["spec"]["containers"][0]["image"] == IMAGE
    skills = (tmp_path / "environment" / "skills").resolve().name
    assert rec.of("upload_dir") == [
        ("upload_dir", skills, target)
        for target in [
            "/workspace/skills",
            "/root/.claude/skills",
            "/root/.agents/skills",
            "/root/.config/opencode/skills",
        ]
        * 2
    ]
    assert rec.of("upload_file") == []
    commands = [call[1] for call in rec.of("exec")]
    # The trial log directories first, then, per copy: mkdir the target, fix ownership.
    assert commands[0].startswith("mkdir -p ")
    assert "mkdir -p /workspace/skills" in commands
    assert "cd /workspace/skills && chown -R 0:0 -- acme" in commands
    assert [c[0] for c in rec.calls].index("ready") < [c[0] for c in rec.calls].index("upload_dir")


def test_baseline_without_a_skill_still_creates_the_directories(tmp_path):
    """The no-skill arm has an empty skills/: nothing is uploaded but the targets exist."""
    env = make_env(tmp_path, skill=False)
    for child in (tmp_path / "environment" / "skills").iterdir():
        child.rmdir()
    rec = Recorder(env)
    start(env)
    assert rec.of("upload_dir") == []
    commands = [call[1] for call in rec.of("exec")]
    assert "mkdir -p /workspace/skills" in commands
    assert not any("chown" in command for command in commands)


def test_copied_files_follow_the_destination_rules(tmp_path):
    """A file goes into an existing directory; --chown and --chmod are applied."""
    dockerfile = (
        GENERATED.read_text()
        + "COPY note.txt /workspace/input\n"
        + "COPY --chown=1000:1000 --chmod=600 note.txt /opt/note.txt\n"
    )
    env = make_env(tmp_path, dockerfile)
    (tmp_path / "environment" / "note.txt").write_text("hi")
    rec = Recorder(env, directories=("/workspace/input",))
    start(env)
    assert rec.of("upload_file") == [
        ("upload_file", "note.txt", "/workspace/input/note.txt"),
        ("upload_file", "note.txt", "/opt/note.txt"),
    ]
    commands = [call[1] for call in rec.of("exec")]
    assert "cd /workspace/input && chown -R 0:0 -- note.txt" in commands
    assert "cd /opt && chown -R 1000:1000 -- note.txt && chmod -R 600 -- note.txt" in commands


def test_a_failed_chown_fails_the_start(tmp_path):
    """An exec that fails while replaying a COPY raises with the sandbox output."""
    env = make_env(tmp_path)
    Recorder(env)

    async def exec_fails(command, **kwargs):
        return ExecResult(stderr="chown: invalid user", return_code=1 if "chown" in command else 0)

    env.exec = exec_fails
    with pytest.raises(RuntimeError, match="chown: invalid user"):
        start(env)


@pytest.mark.parametrize(
    ("change", "match"),
    [
        (("jq ripgrep", "jq ripgrep libxml2-dev"), "apt-get"),
        (("FROM python:3.12-slim", "FROM node:22"), "FROM node:22"),
    ],
)
def test_mismatching_dockerfile_fails_before_the_cluster(tmp_path, change, match):
    """A RUN or FROM the image does not have is an error that names it; nothing is created."""
    env = make_env(tmp_path, GENERATED.read_text().replace(*change, 1))
    rec = Recorder(env)
    with pytest.raises(DockerfileError, match=match) as raised:
        start(env)
    assert "skillevaluator-rn-basic-with-skill cannot run on " + IMAGE in str(raised.value)
    assert rec.calls == []  # not even a client


def test_missing_copy_source_fails_before_the_cluster(tmp_path):
    """A COPY of something that is not in the context fails the start."""
    env = make_env(tmp_path, GENERATED.read_text() + "COPY input/ /workspace/input/\n")
    rec = Recorder(env)
    with pytest.raises(DockerfileError, match="not in the build context"):
        start(env)
    assert rec.calls == []


def test_force_build_is_ignored_for_a_prebuilt_image(tmp_path):
    """There is nothing to build: force_build does not reach the builder."""
    env = make_env(tmp_path)
    rec = Recorder(env)
    start(env, force_build=True)
    assert len(rec.of("create")) == 1
