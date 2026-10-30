"""Tests for AgentSandboxEnvironment, built from real Harbor objects (no cluster)."""

from __future__ import annotations

import io
import tarfile
from pathlib import Path

import pytest
from harbor.models.task.config import EnvironmentConfig
from harbor.models.trial.paths import TrialPaths

from harbor_agent_sandbox import AgentSandboxEnvironment

SESSION = "rn-basic__" + "x" * 80  # longer than a label value may be


def make_env(tmp_path: Path, **kwargs) -> AgentSandboxEnvironment:
    """Construct an environment the way Harbor does, minus the cluster."""
    env_dir = tmp_path / "environment"
    env_dir.mkdir(exist_ok=True)
    (env_dir / "Dockerfile").write_text("FROM scratch\n")
    trial_paths = TrialPaths(trial_dir=tmp_path / "trial")
    return AgentSandboxEnvironment(
        environment_dir=env_dir,
        environment_name="rn-basic",
        session_id=SESSION,
        trial_paths=trial_paths,
        task_env_config=EnvironmentConfig(cpus=1, memory_mb=2048, storage_mb=4096),
        namespace="skill-ci",
        registry="registry.example.com/evals",
        **kwargs,
    )


def pod_spec(manifest: dict) -> dict:
    """Return the pod spec of a Sandbox manifest."""
    return manifest["spec"]["podTemplate"]["spec"]


def test_manifest_is_unprivileged_sandbox(tmp_path, monkeypatch):
    """The Sandbox runs as root without privilege escalation, never privileged."""
    monkeypatch.delenv("HARBOR_BUILDKIT_ADDRESS", raising=False)
    manifest = make_env(tmp_path)._sandbox_manifest()
    assert manifest["apiVersion"] == "agents.x-k8s.io/v1beta1"
    assert manifest["kind"] == "Sandbox"
    assert manifest["metadata"]["namespace"] == "skill-ci"
    (container,) = pod_spec(manifest)["containers"]
    assert container["image"] == "registry.example.com/evals/rn-basic:latest"
    assert container["securityContext"] == {"runAsUser": 0, "allowPrivilegeEscalation": False}
    assert container["resources"]["requests"] == {"cpu": "1", "memory": "2048Mi"}


@pytest.mark.parametrize(
    ("kwargs", "key", "expected"),
    [
        ({}, "runtimeClassName", None),
        ({"runtime_class": "gvisor"}, "runtimeClassName", "gvisor"),
        ({}, "imagePullSecrets", None),
        ({"image_pull_secret": "docr"}, "imagePullSecrets", [{"name": "docr"}]),
        ({"node_selector": {"pool": "evals"}}, "nodeSelector", {"pool": "evals"}),
    ],
)
def test_manifest_optional_fields(tmp_path, kwargs, key, expected):
    """Runtime class, pull secrets and scheduling hints appear only when configured."""
    assert pod_spec(make_env(tmp_path, **kwargs)._sandbox_manifest()).get(key) == expected


def test_manifest_labels_fit_kubernetes_limits(tmp_path):
    """Label values are at most 63 characters, on the Sandbox and its pod template."""
    manifest = make_env(tmp_path)._sandbox_manifest()
    for labels in (
        manifest["metadata"]["labels"],
        manifest["spec"]["podTemplate"]["metadata"]["labels"],
    ):
        assert labels["app.kubernetes.io/managed-by"] == "harbor"
        assert all(len(value) <= 63 for value in labels.values())
    assert len(manifest["metadata"]["name"]) <= 63


def test_context_archive_keeps_empty_directories(tmp_path):
    """The no-skill baseline has an empty skills/ directory that COPY needs."""
    context = tmp_path / "context"
    (context / "skills").mkdir(parents=True)
    (context / "Dockerfile").write_text("FROM scratch\n")
    archive = AgentSandboxEnvironment._create_context_archive(context)
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as tar:
        members = {m.name.removeprefix("./"): m for m in tar.getmembers()}
    assert members["skills"].isdir()
    assert members["Dockerfile"].isfile()


@pytest.mark.parametrize(
    ("env_value", "use_buildkit", "address"),
    [
        (None, False, None),
        ("", False, None),
        ("tcp://buildkitd.skill-ci:1234", True, "tcp://buildkitd.skill-ci:1234"),
    ],
)
def test_buildkit_address_from_environment(tmp_path, monkeypatch, env_value, use_buildkit, address):
    """HARBOR_BUILDKIT_ADDRESS switches the build to BuildKit; unset keeps Harbor's default."""
    if env_value is None:
        monkeypatch.delenv("HARBOR_BUILDKIT_ADDRESS", raising=False)
    else:
        monkeypatch.setenv("HARBOR_BUILDKIT_ADDRESS", env_value)
    env = make_env(tmp_path)
    assert env.use_buildkit is use_buildkit
    assert env.buildkit_address == address


def test_custom_api_requires_started_client(tmp_path):
    """Using the Kubernetes API before start() fails with a clear error."""
    with pytest.raises(RuntimeError, match="not initialized"):
        make_env(tmp_path)._custom_api()


class FakeCoreApi:
    """Answers read_namespaced_pod like the Kubernetes API, or fails with a status."""

    def __init__(self, status: int | None = None) -> None:
        self.status = status

    def read_namespaced_pod(self, name: str, namespace: str):
        from types import SimpleNamespace

        from kubernetes.client.rest import ApiException

        if self.status:
            raise ApiException(status=self.status)
        return SimpleNamespace(metadata=SimpleNamespace(name=name, uid=f"uid-{name}-{namespace}"))


@pytest.mark.parametrize(
    ("pod_namespace", "api_status", "expected"),
    [
        (
            "skill-ci",
            None,
            {
                "apiVersion": "v1",
                "kind": "Pod",
                "name": "runner-abc",
                "uid": "uid-runner-abc-skill-ci",
            },
        ),
        ("other", None, None),  # owner references cannot cross namespaces
        ("skill-ci", 403, None),  # no permission to read the pod: run without an owner
        (None, None, None),  # not running in a pod
    ],
)
def test_owner_reference(tmp_path, monkeypatch, pod_namespace, api_status, expected):
    """Sandboxes are owned by the runner pod when it runs in the sandbox namespace."""
    import asyncio

    from harbor_agent_sandbox import environment

    ns_file, host_file = tmp_path / "namespace", tmp_path / "hostname"
    if pod_namespace:
        ns_file.write_text(pod_namespace + "\n")
        host_file.write_text("runner-abc\n")
    monkeypatch.setattr(environment, "SA_NAMESPACE_FILE", ns_file)
    monkeypatch.setattr(environment, "HOSTNAME_FILE", host_file)
    env = make_env(tmp_path)
    env._core_api = FakeCoreApi(api_status)

    assert asyncio.run(env._owner_reference()) == expected
