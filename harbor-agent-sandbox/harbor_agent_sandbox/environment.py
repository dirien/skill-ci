"""Harbor environment that runs each trial in a Kubernetes Agent Sandbox.

Harbor's Kubernetes backend (``ack``) already builds task images with a
Kubernetes Job, runs commands through the exec API and copies files with tar.
This environment keeps all of that and replaces the bare privileged pod with
an ``agents.x-k8s.io`` Sandbox, so the platform controls isolation through
Agent Sandbox instead of every eval job creating raw pods.

Usage::

    harbor run ... -e harbor_agent_sandbox:AgentSandboxEnvironment \\
        --ek namespace=skill-ci --ek registry=registry.example.com/evals
"""

from __future__ import annotations

import asyncio
import io
import logging
import os
import tarfile
from pathlib import Path
from typing import Any, override

from kubernetes import client as k8s_client
from kubernetes.client.rest import ApiException

from harbor.environments.ack import ACKEnvironment
from harbor.models.trial.paths import EnvironmentPaths

logger = logging.getLogger(__name__)

GROUP = "agents.x-k8s.io"
VERSION = "v1beta1"
PLURAL = "sandboxes"


class AgentSandboxEnvironment(ACKEnvironment):
    """One Agent Sandbox per Harbor trial."""

    def __init__(
        self,
        *args: Any,
        runtime_class: str | None = None,
        ready_timeout_sec: int = 600,
        **kwargs: Any,
    ):
        super().__init__(*args, **kwargs)
        self.runtime_class = runtime_class
        self.ready_timeout_sec = int(ready_timeout_sec)
        # The platform runs one shared BuildKit daemon. Taking its address from the
        # environment keeps it out of the per-run kwargs, which callers like
        # SkillEvaluator reserve.
        if address := os.environ.get("HARBOR_BUILDKIT_ADDRESS"):
            self.use_buildkit = True
            self.buildkit_address = address

    @staticmethod
    @override
    def _create_context_archive(directory: Path) -> bytes:
        # The ack version only adds files, so an empty skills/ directory (the
        # no-skill baseline) disappears and `COPY skills/ ...` fails the build.
        buf = io.BytesIO()
        with tarfile.open(fileobj=buf, mode="w:gz") as tar:
            tar.add(str(directory), arcname=".")
        return buf.getvalue()

    def _custom_api(self) -> k8s_client.CustomObjectsApi:
        if self._core_api is None:
            raise RuntimeError("Kubernetes client is not initialized; call start() first.")
        return k8s_client.CustomObjectsApi(self._core_api.api_client)

    def _sandbox_manifest(self) -> dict[str, Any]:
        resources: dict[str, Any] = {
            "requests": {"cpu": self.cpu_request, "memory": self.memory_request}
        }
        if self.memory_limit:
            resources["limits"] = {"memory": self.memory_limit}

        pod_spec: dict[str, Any] = {
            "containers": [
                {
                    "name": "main",
                    "image": self._get_image_url(),
                    "command": ["sleep", "infinity"],
                    # Agents install their CLI as root, but nothing needs privileged.
                    "securityContext": {
                        "runAsUser": 0,
                        "allowPrivilegeEscalation": False,
                    },
                    "resources": resources,
                }
            ],
        }
        if self.image_pull_secret:
            pod_spec["imagePullSecrets"] = [{"name": self.image_pull_secret}]
        if self.runtime_class:
            pod_spec["runtimeClassName"] = self.runtime_class
        if self.node_selector:
            pod_spec["nodeSelector"] = self.node_selector
        if self.tolerations:
            pod_spec["tolerations"] = self.tolerations

        labels = {"app.kubernetes.io/managed-by": "harbor", "harbor/session": self.session_id[:63]}
        return {
            "apiVersion": f"{GROUP}/{VERSION}",
            "kind": "Sandbox",
            "metadata": {"name": self.pod_name, "namespace": self.namespace, "labels": labels},
            "spec": {"podTemplate": {"metadata": {"labels": labels}, "spec": pod_spec}},
        }

    async def _ensure_image(self, force_build: bool) -> None:
        if force_build or not (self.skip_image_check or await self._image_exists()):
            if not self.registry:
                raise ValueError("Building a task image needs --ek registry=<registry>.")
            await self._build_and_push_image()

    async def _wait_for_sandbox_ready(self) -> None:
        api = self._custom_api()
        for _ in range(self.ready_timeout_sec // 2):
            sandbox = await asyncio.to_thread(
                api.get_namespaced_custom_object, GROUP, VERSION, self.namespace, PLURAL, self.pod_name
            )
            for cond in sandbox.get("status", {}).get("conditions", []):
                if cond["type"] == "Ready" and cond["status"] == "True":
                    return
            await asyncio.sleep(2)
        raise RuntimeError(f"Sandbox {self.pod_name} was not ready after {self.ready_timeout_sec}s")

    @override
    async def start(self, force_build: bool):
        await self._ensure_client()
        await self._ensure_image(force_build)

        await asyncio.to_thread(
            self._custom_api().create_namespaced_custom_object,
            GROUP, VERSION, self.namespace, PLURAL, self._sandbox_manifest(),
        )
        await self._wait_for_sandbox_ready()

        # Agent Sandbox names the pod after the Sandbox, so the inherited exec and
        # file transfer code finds it through self.pod_name.
        dirs = f"{EnvironmentPaths.agent_dir} {EnvironmentPaths.verifier_dir}"
        result = await self.exec(f"mkdir -p {dirs} && chmod 777 {dirs}")
        if result.return_code != 0:
            raise RuntimeError(f"Failed to prepare sandbox {self.pod_name}: {result.stderr}")

        await self._upload_environment_dir_after_start()

    @override
    async def stop(self, delete: bool):
        if self._client_manager is None:
            return
        await self._ensure_client()
        try:
            if delete:
                await asyncio.to_thread(
                    self._custom_api().delete_namespaced_custom_object,
                    GROUP, VERSION, self.namespace, PLURAL, self.pod_name,
                )
        except ApiException as e:
            if e.status != 404:
                raise RuntimeError(f"Failed to delete sandbox {self.pod_name}: {e}") from e
        finally:
            try:
                await self._client_manager.release_client()
            except Exception as e:  # noqa: BLE001
                logger.error(f"Error releasing Kubernetes client: {e}")
            finally:
                self._client_manager = None
                self._core_api = None
                self._dynamic_client = None
