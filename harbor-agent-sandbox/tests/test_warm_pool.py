"""Tests for the warm-pool path (SandboxClaim) and the trial labels, against a fake cluster."""

from __future__ import annotations

import asyncio
import logging
import shlex
from pathlib import Path
from types import SimpleNamespace
from typing import Any

import pytest
from harbor.environments import ack
from harbor.models.task.config import EnvironmentConfig
from harbor.models.trial.paths import TrialPaths
from kubernetes.client.rest import ApiException

from harbor_agent_sandbox import AgentSandboxEnvironment, environment
from harbor_agent_sandbox.dockerfile import DockerfileError

GENERATED = Path(__file__).parent / "fixtures" / "skillevaluator-0.6.0.Dockerfile"
IMAGE = "ghcr.io/dirien/skill-ci-sandbox:0.8.1"
POOL = "default"
CLAIM = "rn-basic--abc"  # the pod name Harbor derives from the session id
WARM_POD = "default-x7k2p"  # the warm pod the claim is bound to
RUNNER = "rn-basic-claude-code-haiku-x9z"
EVAL = "acme-release-notes-v1-0-0-claude-code-claude-haiku-5-5"


@pytest.fixture(autouse=True)
def clean_env(monkeypatch, tmp_path):
    """Keep the host's sandbox settings out of every test; no runner pod unless a test adds one."""
    for name in (
        "HARBOR_SANDBOX_IMAGE",
        "HARBOR_SANDBOX_WARM_POOL",
        "HARBOR_SANDBOX_CPUS",
        "HARBOR_SANDBOX_MEMORY_MB",
        "HARBOR_BUILDKIT_ADDRESS",
        "SKILLEVAL_NAME",
    ):
        monkeypatch.delenv(name, raising=False)
    monkeypatch.setattr(environment, "SA_NAMESPACE_FILE", tmp_path / "no-namespace")
    monkeypatch.setattr(environment, "HOSTNAME_FILE", tmp_path / "no-hostname")
    monkeypatch.setattr(environment, "CLAIM_POLL_SEC", 0)
    environment._logged_once.clear()


def in_runner_pod(monkeypatch, tmp_path, namespace: str = "skill-ci") -> None:
    """Make the environment believe it runs in the runner pod RUNNER."""
    ns_file, host_file = tmp_path / "namespace", tmp_path / "hostname"
    ns_file.write_text(namespace + "\n")
    host_file.write_text(RUNNER + "\n")
    monkeypatch.setattr(environment, "SA_NAMESPACE_FILE", ns_file)
    monkeypatch.setattr(environment, "HOSTNAME_FILE", host_file)


def make_env(tmp_path: Path, dockerfile: str | None = None, **kwargs) -> AgentSandboxEnvironment:
    """Build an environment around a generated task directory, like Harbor does."""
    env_dir = tmp_path / "environment"
    (env_dir / "skills" / "acme").mkdir(parents=True, exist_ok=True)
    (env_dir / "skills" / "acme" / "SKILL.md").write_text("the skill")
    (env_dir / "Dockerfile").write_text(dockerfile or GENERATED.read_text())
    kwargs.setdefault("prebuilt_image", IMAGE)
    kwargs.setdefault("warm_pool", POOL)
    return AgentSandboxEnvironment(
        environment_dir=env_dir,
        environment_name="skillevaluator-rn-basic-with-skill",
        session_id="rn-basic__abc",
        trial_paths=TrialPaths(trial_dir=tmp_path / "trial"),
        task_env_config=EnvironmentConfig(cpus=1, memory_mb=2048, storage_mb=4096),
        namespace="skill-ci",
        **kwargs,
    )


def ready(pod: str = WARM_POD) -> dict[str, Any]:
    """Return the status of a claim bound to ``pod``."""
    return {"conditions": [{"type": "Ready", "status": "True"}], "sandbox": {"name": pod}}


def not_ready(reason: str, message: str = "") -> dict[str, Any]:
    """Return the status of a claim that is not ready for ``reason``."""
    return {
        "conditions": [{"type": "Ready", "status": "False", "reason": reason, "message": message}]
    }


class FakeCluster:
    """The Kubernetes API as the environment uses it: custom objects, pods and exec.

    ``claim_statuses`` are the claim's status on each read (the last one repeats), and
    ``pod_labels`` the warm pod's labels on each read (the last one repeats).
    """

    def __init__(
        self,
        env: AgentSandboxEnvironment,
        monkeypatch,
        claim_statuses: list[dict] | None = None,
        pod_labels: list[dict] | None = None,
        runner_labels: dict[str, str] | None = None,
        delete_status: int | None = None,
    ) -> None:
        self.claim_statuses = claim_statuses or [ready()]
        self.pod_labels = pod_labels if pod_labels is not None else [env._trial_labels()]
        self.runner_labels = runner_labels or {}
        self.delete_status = delete_status
        self.created: list[tuple[str, str, dict]] = []
        self.deleted: list[tuple[str, str, Any]] = []
        self.claim_reads = 0
        self.pod_reads = 0
        self.commands: list[str] = []
        self.uploads: list[tuple[str, str]] = []
        self.released = False

        async def ensure_client():
            return None

        async def upload_dir(source, target):
            self.uploads.append((Path(source).name, target))

        async def upload_file(source, target):
            self.uploads.append((Path(source).name, target))

        env._ensure_client = ensure_client
        env._core_api = self
        env._exec_api = SimpleNamespace(connect_get_namespaced_pod_exec=None)
        env._custom_api = lambda: self
        env._client_manager = self
        env.upload_dir = upload_dir
        env.upload_file = upload_file
        monkeypatch.setattr(ack, "stream", self.stream)

    # CustomObjectsApi

    def create_namespaced_custom_object(self, group, version, namespace, plural, body):
        self.created.append((group, plural, body))
        return body

    def get_namespaced_custom_object(self, group, version, namespace, plural, name):
        assert (group, plural, name) == ("extensions.agents.x-k8s.io", "sandboxclaims", CLAIM)
        status = self.claim_statuses[min(self.claim_reads, len(self.claim_statuses) - 1)]
        self.claim_reads += 1
        return {"metadata": {"name": name}, "status": status}

    def delete_namespaced_custom_object(self, group, version, namespace, plural, name, **kw):
        if self.delete_status:
            raise ApiException(status=self.delete_status)
        self.deleted.append((plural, name, kw.get("body")))

    # CoreV1Api

    def read_namespaced_pod(self, name, namespace):
        if name == RUNNER:
            return SimpleNamespace(
                metadata=SimpleNamespace(name=name, uid="uid-runner", labels=self.runner_labels)
            )
        assert name == WARM_POD
        labels = self.pod_labels[min(self.pod_reads, len(self.pod_labels) - 1)]
        self.pod_reads += 1
        return SimpleNamespace(metadata=SimpleNamespace(name=name, labels=dict(labels)))

    # kubernetes.stream.stream, as ACKEnvironment.exec calls it

    def stream(self, func, pod_name, namespace, command, **kwargs):
        assert pod_name == WARM_POD
        self.commands.append(command[2])
        inner = shlex.split(command[2])
        code = 1 if "test -d" in " ".join(inner) else 0
        return SimpleNamespace(
            is_open=lambda: False,
            run_forever=lambda timeout=0: None,
            returncode=code,
            close=lambda: None,
        )

    # KubernetesClientManager

    async def release_client(self):
        self.released = True


def start(env: AgentSandboxEnvironment) -> None:
    asyncio.run(env.start(force_build=False))


def claims(cluster: FakeCluster) -> list[dict]:
    return [body for _, plural, body in cluster.created if plural == "sandboxclaims"]


# claim manifest


def test_claim_names_the_warm_pool_and_the_trial_labels(tmp_path, monkeypatch):
    """The claim refers to the pool, carries the labels for the pod and sets no env."""
    monkeypatch.setenv("SKILLEVAL_NAME", EVAL)
    manifest = make_env(tmp_path)._claim_manifest()
    assert manifest["apiVersion"] == "extensions.agents.x-k8s.io/v1beta1"
    assert manifest["kind"] == "SandboxClaim"
    assert manifest["metadata"]["name"] == CLAIM
    assert manifest["metadata"]["labels"]["app.kubernetes.io/managed-by"] == "harbor"
    assert manifest["spec"] == {
        "warmPoolRef": {"name": POOL},
        "additionalPodMetadata": {
            "labels": {
                "sandbox.users.io/skillci-eval": EVAL,
                "sandbox.users.io/skillci-role": "trial",
                "sandbox.users.io/skillci-trial": "rn-basic__abc",
            }
        },
    }


def test_warm_pool_from_environment_variable(tmp_path, monkeypatch):
    """HARBOR_SANDBOX_WARM_POOL selects the claim path; blank keeps the bare Sandbox."""
    monkeypatch.setenv("HARBOR_SANDBOX_WARM_POOL", " small ")
    assert make_env(tmp_path, warm_pool=None).warm_pool == "small"
    monkeypatch.setenv("HARBOR_SANDBOX_WARM_POOL", " ")
    assert make_env(tmp_path, warm_pool=None).warm_pool is None


# start


def test_start_claims_a_warm_pod_and_replays_the_task(tmp_path, monkeypatch):
    """start() creates one owned claim, no Sandbox, and runs everything in the claimed pod."""
    in_runner_pod(monkeypatch, tmp_path)
    monkeypatch.setenv("SKILLEVAL_NAME", EVAL)
    env = make_env(tmp_path)
    cluster = FakeCluster(env, monkeypatch)
    start(env)

    assert [plural for _, plural, _ in cluster.created] == ["sandboxclaims"]
    (claim,) = claims(cluster)
    assert claim["metadata"]["ownerReferences"] == [
        {"apiVersion": "v1", "kind": "Pod", "name": RUNNER, "uid": "uid-runner"}
    ]
    assert env.pod_name == WARM_POD  # status.sandbox.name, not the claim name
    assert env._claim_name == CLAIM
    # The skill COPY steps are replayed as uploads into the warm pod.
    assert ("skills", "/root/.claude/skills") in cluster.uploads
    assert any("chown -R 0:0 -- acme" in c for c in cluster.commands)


def test_start_without_a_prebuilt_image_builds_nothing(tmp_path, monkeypatch):
    """The profile's template has the image: no task image is checked or built."""
    env = make_env(tmp_path, prebuilt_image=None)
    FakeCluster(env, monkeypatch)

    async def forbidden(*args, **kwargs):
        raise AssertionError("no image build on the warm-pool path")

    env._ensure_image = forbidden
    start(env)
    assert env.pod_name == WARM_POD


def test_start_waits_until_the_pod_carries_the_labels(tmp_path, monkeypatch):
    """The eval label on the pod is what the network policy selects: start waits for it."""
    monkeypatch.setenv("SKILLEVAL_NAME", EVAL)
    env = make_env(tmp_path)
    labels = env._trial_labels()
    cluster = FakeCluster(
        env,
        monkeypatch,
        claim_statuses=[{}, not_ready("SandboxNotReady"), ready()],
        pod_labels=[{"skillci.dev/component": "trial-sandbox"}, {}, labels],
    )
    start(env)
    assert cluster.claim_reads == 3
    assert cluster.pod_reads == 3
    assert cluster.commands  # nothing ran before the labels were there


@pytest.mark.parametrize(
    ("reason", "message"),
    [
        ("InvalidMetadata", "label domain skillci.dev is not allowed"),
        ("EnvVarsInjectionRejected", "env injection is disabled for template default"),
    ],
)
def test_claim_failures_fail_fast(tmp_path, monkeypatch, reason, message):
    """A claim Agent Sandbox rejected fails at once with the condition's message."""
    env = make_env(tmp_path)
    cluster = FakeCluster(env, monkeypatch, claim_statuses=[not_ready(reason, message)])
    with pytest.raises(RuntimeError, match=f"{reason}: {message}"):
        start(env)
    assert cluster.claim_reads == 1
    assert cluster.commands == []


def test_claim_timeout_names_what_it_waited_for(tmp_path, monkeypatch):
    """A claim that never becomes ready times out with the last reason."""
    env = make_env(tmp_path, ready_timeout_sec=0)
    FakeCluster(env, monkeypatch, claim_statuses=[not_ready("NoWarmSandbox", "pool empty")])
    with pytest.raises(RuntimeError, match=r"not ready after 0s.*NoWarmSandbox: pool empty"):
        start(env)


def test_claim_timeout_while_labels_are_missing(tmp_path, monkeypatch):
    """A ready claim whose pod never gets the labels times out naming them."""
    env = make_env(tmp_path, ready_timeout_sec=0)
    FakeCluster(env, monkeypatch, pod_labels=[{}])
    with pytest.raises(RuntimeError, match=f"labels .* on pod {WARM_POD}"):
        start(env)


def test_named_or_numeric_user_is_refused_on_the_warm_pool(tmp_path, monkeypatch):
    """The profile's pod runs as root; a USER step fails before any cluster call."""
    env = make_env(tmp_path, GENERATED.read_text() + "USER 1000\n")
    cluster = FakeCluster(env, monkeypatch)
    with pytest.raises(DockerfileError, match="warm pool default: USER 1000 is not supported"):
        start(env)
    assert cluster.created == []


def test_root_user_is_fine_on_the_warm_pool(tmp_path, monkeypatch):
    """USER root is what the profile runs anyway."""
    env = make_env(tmp_path, GENERATED.read_text() + "USER root\n")
    FakeCluster(env, monkeypatch)
    start(env)


# ENV and WORKDIR per command


def test_commands_get_the_task_workdir_and_env(tmp_path, monkeypatch):
    """Every command starts in the task's WORKDIR with its ENV; explicit values win."""
    dockerfile = GENERATED.read_text() + 'ENV GREETING="$(whoami)" FOO=bar\n'
    env = make_env(tmp_path, dockerfile)
    cluster = FakeCluster(env, monkeypatch)
    start(env)

    cluster.commands.clear()
    asyncio.run(env.exec("echo $FOO"))
    asyncio.run(env.exec("pwd", cwd="/tmp", env={"FOO": "override"}))
    plain, explicit = cluster.commands
    assert plain.startswith("cd /workspace && ")
    assert "FOO=bar " in plain
    assert "GREETING='$(whoami)' " in plain  # text, not a command substitution
    assert "CLAUDE_CODE_DISABLE_POLICY_SKILLS=1 " in plain
    assert plain.endswith("bash -ic 'echo $FOO'")
    assert explicit.startswith("cd /tmp && ")
    assert "FOO=override " in explicit and "FOO=bar" not in explicit


def test_start_creates_the_workdir_first(tmp_path, monkeypatch):
    """The first command runs from / and creates the WORKDIR every later command enters."""
    dockerfile = GENERATED.read_text() + "WORKDIR /workspace/project\n"
    env = make_env(tmp_path, dockerfile)
    cluster = FakeCluster(env, monkeypatch)
    start(env)
    first, second = cluster.commands[:2]
    assert first.startswith("cd / && ")
    assert "mkdir -p /workspace/project" in first
    assert second.startswith("cd /workspace/project && ")


def test_bare_sandbox_commands_are_not_wrapped(tmp_path, monkeypatch):
    """Without a warm pool the container has the ENV and WORKDIR; commands stay as they were."""
    env = make_env(tmp_path, warm_pool=None)
    env._load_plan()
    cluster = FakeCluster(env, monkeypatch)
    env.pod_name = WARM_POD
    asyncio.run(env.exec("true"))
    assert cluster.commands == ["bash -ic true"]


# size


def test_size_variables_are_ignored_and_logged_once(tmp_path, monkeypatch, caplog):
    """The profile sizes a warm pod: the size variables change nothing and are reported once."""
    monkeypatch.setenv("HARBOR_SANDBOX_CPUS", "4")
    monkeypatch.setenv("HARBOR_SANDBOX_MEMORY_MB", "8192")
    with caplog.at_level(logging.INFO, logger=environment.__name__):
        first, second = make_env(tmp_path), make_env(tmp_path)
    assert (first.cpu_request, second.memory_request) == ("1", "2048Mi")
    assert [r.getMessage() for r in caplog.records].count(
        "HARBOR_SANDBOX_CPUS and HARBOR_SANDBOX_MEMORY_MB are ignored: warm pool default "
        "(its sandbox profile) sizes the sandbox"
    ) == 1


# SkillEval name and labels


@pytest.mark.parametrize(
    ("env_name", "job_name", "expected"),
    [
        (EVAL, "something-else", EVAL),  # the controller's variable wins
        (None, EVAL, EVAL),  # the runner pod's Job name
        (None, None, None),  # neither: no eval label
        ("Not A Label!", None, None),  # not a label value: dropped
    ],
)
def test_skilleval_name_source(tmp_path, monkeypatch, env_name, job_name, expected):
    """SKILLEVAL_NAME, else the runner pod's batch.kubernetes.io/job-name label."""
    in_runner_pod(monkeypatch, tmp_path)
    if env_name:
        monkeypatch.setenv("SKILLEVAL_NAME", env_name)
    env = make_env(tmp_path)
    runner_labels = {"batch.kubernetes.io/job-name": job_name} if job_name else {}
    cluster = FakeCluster(env, monkeypatch, runner_labels=runner_labels, pod_labels=[{}])
    cluster.pod_labels = [
        {
            "sandbox.users.io/skillci-role": "trial",
            "sandbox.users.io/skillci-trial": "rn-basic__abc",
        }
        | ({"sandbox.users.io/skillci-eval": expected} if expected else {})
    ]
    start(env)
    labels = claims(cluster)[0]["spec"]["additionalPodMetadata"]["labels"]
    assert labels.get("sandbox.users.io/skillci-eval") == expected
    assert labels["sandbox.users.io/skillci-role"] == "trial"


def test_bare_sandbox_pod_template_carries_the_trial_labels(tmp_path, monkeypatch):
    """Without a warm pool the labels go into the Sandbox's pod template, not onto the Sandbox."""
    monkeypatch.setenv("SKILLEVAL_NAME", EVAL)
    manifest = make_env(tmp_path, warm_pool=None)._sandbox_manifest()
    pod_labels = manifest["spec"]["podTemplate"]["metadata"]["labels"]
    assert pod_labels["sandbox.users.io/skillci-eval"] == EVAL
    assert pod_labels["sandbox.users.io/skillci-role"] == "trial"
    assert pod_labels["app.kubernetes.io/managed-by"] == "harbor"
    assert "sandbox.users.io/skillci-eval" not in manifest["metadata"]["labels"]


@pytest.mark.parametrize(
    ("session_id", "trial"),
    [
        ("rn-basic__sryqofs__env", "rn-basic__sryqofs"),
        ("rn-basic__sryqofs__verifier__default", "rn-basic__sryqofs"),
        ("rn-basic__sryqofs", "rn-basic__sryqofs"),
        ("__verifier__x", "__verifier__x"),
    ],
)
def test_trial_name_from_session(session_id, trial):
    """The trial environment and a separate verifier environment map to the same trial."""
    assert environment.trial_name_from_session(session_id) == trial


@pytest.mark.parametrize("warm_pool", [POOL, None])
def test_both_paths_label_the_pod_with_the_trial(tmp_path, monkeypatch, warm_pool):
    """The claim's additionalPodMetadata and the bare pod template carry the same trial label."""
    monkeypatch.setenv("SKILLEVAL_NAME", EVAL)
    env = make_env(tmp_path, warm_pool=warm_pool)
    env.session_id = "rn-basic__sryqofs__env"
    if warm_pool:
        manifest = env._claim_manifest()
        pod_labels = manifest["spec"]["additionalPodMetadata"]["labels"]
    else:
        manifest = env._sandbox_manifest()
        pod_labels = manifest["spec"]["podTemplate"]["metadata"]["labels"]
    assert pod_labels["sandbox.users.io/skillci-trial"] == "rn-basic__sryqofs"
    # Only the agent's label domain on the pod labels a claim asks for.
    if warm_pool:
        assert all(key.startswith("sandbox.users.io/") for key in pod_labels)
    assert "sandbox.users.io/skillci-trial" not in manifest["metadata"]["labels"]


def test_long_trial_names_get_a_hashed_label(tmp_path, monkeypatch):
    """Past 63 characters: the first 52, "-" and 10 hex characters of the SHA-256."""
    import hashlib

    trial = "release-notes-for-a-case-with-a-really-long-name-xxxxxxx__sryqofs"
    env = make_env(tmp_path)
    env.session_id = trial + "__env"
    label = env._claim_manifest()["spec"]["additionalPodMetadata"]["labels"][
        "sandbox.users.io/skillci-trial"
    ]
    assert label == trial[:52] + "-" + hashlib.sha256(trial.encode()).hexdigest()[:10]
    assert environment.LABEL_VALUE.match(label)
    assert environment.trial_label(trial) == label


@pytest.mark.parametrize("trial", ["_x__abc", "a/b__abc", "-" * 70, "x" * 50 + ".__abc" + "y" * 20])
def test_trial_label_is_always_a_label_value(trial):
    label = environment.trial_label(trial)
    assert environment.LABEL_VALUE.match(label)
    assert len(label) <= 63


def test_start_waits_for_the_trial_label(tmp_path, monkeypatch):
    """The trial label maps flows: start waits until the claimed pod carries it too."""
    monkeypatch.setenv("SKILLEVAL_NAME", EVAL)
    env = make_env(tmp_path)
    labels = env._trial_labels()
    without_trial = {k: v for k, v in labels.items() if k != "sandbox.users.io/skillci-trial"}
    cluster = FakeCluster(env, monkeypatch, pod_labels=[without_trial, labels])
    start(env)
    assert cluster.pod_reads == 2


def test_bare_sandbox_start_takes_the_name_from_the_job(tmp_path, monkeypatch):
    """The bare path resolves the SkillEval name through the same runner pod lookup."""
    in_runner_pod(monkeypatch, tmp_path)
    env = make_env(tmp_path, warm_pool=None)
    cluster = FakeCluster(env, monkeypatch, runner_labels={"batch.kubernetes.io/job-name": EVAL})

    async def sandbox_ready():
        env.pod_name = WARM_POD  # let the fake exec find it

    env._wait_for_sandbox_ready = sandbox_ready
    start(env)
    ((group, plural, sandbox),) = cluster.created
    assert (group, plural) == ("agents.x-k8s.io", "sandboxes")
    assert sandbox["metadata"]["ownerReferences"][0]["name"] == RUNNER
    template = sandbox["spec"]["podTemplate"]["metadata"]["labels"]
    assert template["sandbox.users.io/skillci-eval"] == EVAL


# stop


def test_stop_deletes_the_claim(tmp_path, monkeypatch):
    """Deleting the claim releases the warm pod; nothing waits for it to go."""
    env = make_env(tmp_path)
    cluster = FakeCluster(env, monkeypatch)
    start(env)
    asyncio.run(env.stop(delete=True))
    ((plural, name, body),) = cluster.deleted
    assert (plural, name) == ("sandboxclaims", CLAIM)
    assert body.propagation_policy == "Background"
    assert cluster.released


def test_stop_ignores_a_claim_that_is_gone(tmp_path, monkeypatch):
    """A claim already deleted (for example by garbage collection) is not an error."""
    env = make_env(tmp_path)
    FakeCluster(env, monkeypatch, delete_status=404)
    start(env)
    asyncio.run(env.stop(delete=True))


def test_stop_reports_a_failed_claim_deletion(tmp_path, monkeypatch):
    """Another API error names the claim."""
    env = make_env(tmp_path)
    FakeCluster(env, monkeypatch, delete_status=403)
    start(env)
    with pytest.raises(RuntimeError, match=f"sandbox claim {CLAIM}"):
        asyncio.run(env.stop(delete=True))


def test_stop_before_a_claim_deletes_nothing(tmp_path, monkeypatch):
    """A start that failed before the claim existed leaves nothing to delete."""
    env = make_env(tmp_path)
    cluster = FakeCluster(env, monkeypatch)
    asyncio.run(env.stop(delete=True))
    assert cluster.deleted == []
