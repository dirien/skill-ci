"""Harbor environment that runs each trial in a Kubernetes Agent Sandbox.

Harbor's Kubernetes backend (``ack``) already builds task images with a
Kubernetes Job, runs commands through the exec API and copies files with tar.
This environment keeps all of that and replaces the bare privileged pod with
an ``agents.x-k8s.io`` Sandbox, so the platform controls isolation through
Agent Sandbox instead of every eval job creating raw pods.

Task images come from one of two places:

* **Prebuilt (default for skill-ci).** ``HARBOR_SANDBOX_IMAGE`` names a generic sandbox image
  (``sandbox/Dockerfile``). Nothing is built: the sandbox runs that image and the
  task-specific steps of the generated Dockerfile (``COPY``, ``ENV``, ``WORKDIR``, ``USER``)
  are replayed when it starts. A ``RUN`` step the image does not cover fails the trial with
  a clear error, see :mod:`harbor_agent_sandbox.dockerfile`.
* **Built.** Without ``HARBOR_SANDBOX_IMAGE``, Harbor's ``ack`` build runs as before and needs
  a registry you control: ``--ek registry=registry.example.com/evals``, a builder
  (a ``docker:dind`` Job, or BuildKit through ``HARBOR_BUILDKIT_ADDRESS``) and push rights.

Usage::

    HARBOR_SANDBOX_IMAGE=ghcr.io/dirien/skill-ci-sandbox:0.7.0 \\
    harbor run ... -e harbor_agent_sandbox:AgentSandboxEnvironment --ek namespace=skill-ci
"""

from __future__ import annotations

import asyncio
import io
import logging
import os
import posixpath
import shlex
import tarfile
from pathlib import Path
from typing import Any, cast, override

from harbor.environments.ack import ACKEnvironment
from harbor.models.trial.paths import EnvironmentPaths
from kubernetes import client as k8s_client
from kubernetes.client.rest import ApiException

from harbor_agent_sandbox import dockerfile

logger = logging.getLogger(__name__)

GROUP = "agents.x-k8s.io"
VERSION = "v1beta1"
PLURAL = "sandboxes"
# The generic sandbox image that replaces per-task image builds.
SANDBOX_IMAGE_ENV = "HARBOR_SANDBOX_IMAGE"
SAFE_TO_EVICT = "cluster-autoscaler.kubernetes.io/safe-to-evict"

# Where a pod finds its own namespace and name.
SA_NAMESPACE_FILE = Path("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
HOSTNAME_FILE = Path("/etc/hostname")


def _positive_int(name: str, value: str) -> int:
    """Parse the positive integer in environment variable ``name``."""
    if not value.isdigit() or int(value) < 1:
        raise ValueError(f"{name} must be a positive integer, got {value!r}")
    return int(value)


def _run_as_user(plan: dockerfile.Plan) -> int:
    """Return the uid a Dockerfile's last ``USER`` asks for (root without one)."""
    if plan.user is None or plan.user in {"root", "0"}:
        return 0
    if plan.user.isdigit():
        return int(plan.user)
    raise dockerfile.DockerfileError(
        f"USER {plan.user} is not supported, only root or a numeric uid: the sandbox image "
        "does not know that user"
    )


def _is_mutable_tag(image: str) -> bool:
    """Return True for a reference Kubernetes would re-pull on every start (no tag, or latest)."""
    if "@" in image:
        return False
    tag = image.rsplit("/", 1)[-1].partition(":")[2]
    return tag in {"", "latest"}


class AgentSandboxEnvironment(ACKEnvironment):
    """One Agent Sandbox per Harbor trial.

    With ``HARBOR_SANDBOX_IMAGE`` (or ``prebuilt_image``) the sandbox runs that generic image and
    nothing is built; otherwise the task image is built and pushed by Harbor's ``ack`` backend.
    """

    def __init__(
        self,
        *args: Any,
        runtime_class: str | None = None,
        ready_timeout_sec: int = 600,
        prebuilt_image: str | None = None,
        **kwargs: Any,
    ) -> None:
        super().__init__(*args, **kwargs)
        self.runtime_class = runtime_class
        self.ready_timeout_sec = int(ready_timeout_sec)
        # The generic sandbox image comes from the environment, like the BuildKit address and
        # the size below: that keeps it out of the per-run kwargs, which callers like
        # SkillEvaluator reserve. The keyword argument is for direct use and tests.
        configured = prebuilt_image or os.environ.get(SANDBOX_IMAGE_ENV) or ""
        self.prebuilt_image: str | None = configured.strip() or None
        self._plan: dockerfile.Plan | None = None
        self._transfers: list[dockerfile.Transfer] = []
        # Only the build path uses BuildKit. Taking its address from the environment keeps it
        # out of the per-run kwargs as well.
        if address := os.environ.get("HARBOR_BUILDKIT_ADDRESS"):
            self.use_buildkit = True
            self.buildkit_address = address
        self._apply_size_override()

    def _apply_size_override(self) -> None:
        """Size the sandbox from ``HARBOR_SANDBOX_CPUS`` and ``HARBOR_SANDBOX_MEMORY_MB``.

        ``skillevaluator validate`` has no ``--override-cpus`` or ``--override-memory-mb``, and
        a skill task asks for 2 CPU and 4 GiB, which starves a small cluster. Like the BuildKit
        address, the size comes from the environment, so it stays out of the per-run kwargs.
        A memory limit set through ``memory_limit_multiplier`` keeps its ratio to the request.
        """
        if cpus := os.environ.get("HARBOR_SANDBOX_CPUS"):
            self.cpu_request = str(_positive_int("HARBOR_SANDBOX_CPUS", cpus))
        if memory := os.environ.get("HARBOR_SANDBOX_MEMORY_MB"):
            memory_mb = _positive_int("HARBOR_SANDBOX_MEMORY_MB", memory)
            if self.memory_limit:
                old_request = int(self.memory_request.removesuffix("Mi"))
                old_limit = int(self.memory_limit.removesuffix("Mi"))
                self.memory_limit = f"{memory_mb * old_limit // old_request}Mi"
            self.memory_request = f"{memory_mb}Mi"

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

    def _load_plan(self) -> dockerfile.Plan:
        """Read the task Dockerfile and check the prebuilt image can stand in for it.

        Raises :class:`dockerfile.DockerfileError` before any cluster call when it cannot:
        a ``RUN`` or ``FROM`` the image does not cover, a ``COPY`` source that is missing, or
        a ``USER`` that is not root or a uid.
        """
        if self._plan is not None:
            return self._plan
        try:
            plan = dockerfile.parse(self._environment_definition_path.read_text())
            plan.require_covered()
            transfers = [
                transfer
                for copy in plan.copies
                for transfer in dockerfile.resolve_copy(copy, self.environment_dir)
            ]
            _run_as_user(plan)
        except dockerfile.DockerfileError as e:
            raise dockerfile.DockerfileError(
                f"task {self.environment_name} cannot run on {self.prebuilt_image}: {e}"
            ) from e
        self._plan, self._transfers = plan, transfers
        return plan

    def _image_ref(self) -> str:
        """Return the image the sandbox runs: the prebuilt one, else the task image."""
        return self.prebuilt_image or self._get_image_url()

    def _pull_secrets(self) -> list[str]:
        """Return the pull secret names: ``image_pull_secret`` may list several, comma separated."""
        return [name.strip() for name in (self.image_pull_secret or "").split(",") if name.strip()]

    def _sandbox_manifest(self) -> dict[str, Any]:
        resources: dict[str, Any] = {
            "requests": {"cpu": self.cpu_request, "memory": self.memory_request}
        }
        if self.memory_limit:
            resources["limits"] = {"memory": self.memory_limit}

        container: dict[str, Any] = {
            "name": "main",
            "image": self._image_ref(),
            "command": ["sleep", "infinity"],
            # Agents install their CLI as root, but nothing needs privileged.
            "securityContext": {"runAsUser": 0, "allowPrivilegeEscalation": False},
            "resources": resources,
        }
        if self.prebuilt_image:
            plan = self._load_plan()
            # What the Dockerfile's last WORKDIR, ENV and USER would have put in the image.
            container["workingDir"] = plan.workdir
            container["securityContext"]["runAsUser"] = _run_as_user(plan)
            if plan.env:
                container["env"] = [
                    # The kubelet expands $(NAME) in values; $$ keeps the text as written.
                    {"name": name, "value": value.replace("$(", "$$(")}
                    for name, value in plan.env.items()
                ]
            if _is_mutable_tag(self.prebuilt_image):
                container["imagePullPolicy"] = "Always"
            else:
                container["imagePullPolicy"] = "IfNotPresent"

        pod_spec: dict[str, Any] = {"containers": [container]}
        if secrets := self._pull_secrets():
            pod_spec["imagePullSecrets"] = [{"name": name} for name in secrets]
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
            "spec": {
                "podTemplate": {
                    # A cluster scale-down must not kill a running trial.
                    "metadata": {"labels": labels, "annotations": {SAFE_TO_EVICT: "false"}},
                    "spec": pod_spec,
                }
            },
        }

    async def _ensure_image(self, force_build: bool) -> None:
        if force_build or not (self.skip_image_check or await self._image_exists()):
            if not self.registry:
                raise ValueError("Building a task image needs --ek registry=<registry>.")
            await self._build_and_push_image()

    async def _owner_reference(self) -> dict[str, Any] | None:
        """Return an owner reference to the pod running Harbor, if it runs next to the sandboxes.

        With it, Kubernetes deletes the sandboxes of a cancelled or crashed run
        together with the run's pod instead of leaving them to hold capacity.
        """
        try:
            if SA_NAMESPACE_FILE.read_text().strip() != self.namespace:
                return None
            pod_name = HOSTNAME_FILE.read_text().strip()
        except OSError:
            return None  # not running in a pod
        if self._core_api is None:
            return None
        try:
            pod = await asyncio.to_thread(
                self._core_api.read_namespaced_pod, pod_name, self.namespace
            )
        except ApiException as e:
            logger.warning(
                "Sandbox %s gets no owner: reading pod %s failed: %s", self.pod_name, pod_name, e
            )
            return None
        return {
            "apiVersion": "v1",
            "kind": "Pod",
            "name": pod.metadata.name,
            "uid": pod.metadata.uid,
        }

    async def _wait_for_sandbox_ready(self) -> None:
        api = self._custom_api()
        for _ in range(self.ready_timeout_sec // 2):
            sandbox = cast(
                "dict[str, Any]",
                await asyncio.to_thread(
                    api.get_namespaced_custom_object,
                    GROUP, VERSION, self.namespace, PLURAL, self.pod_name,
                ),
            )  # fmt: skip
            for cond in sandbox.get("status", {}).get("conditions", []):
                if cond["type"] == "Ready" and cond["status"] == "True":
                    return
            await asyncio.sleep(2)
        raise RuntimeError(f"Sandbox {self.pod_name} was not ready after {self.ready_timeout_sec}s")

    async def _exec_checked(self, command: str, what: str) -> None:
        """Run a command in the sandbox and raise with its output when it fails."""
        result = await self.exec(command)
        if result.return_code != 0:
            detail = (result.stderr or result.stdout or "").strip()
            raise RuntimeError(f"{what} failed in sandbox {self.pod_name}: {detail}")

    async def _replay_transfer(self, transfer: dockerfile.Transfer) -> None:
        """Do what one ``COPY`` of the task Dockerfile would have done at build time.

        Files end up owned by root (or ``--chown``) with their modes, as Docker's ``COPY``
        leaves them. Like Harbor's ``upload_dir``, empty directories inside a source are not
        created.
        """
        quote = shlex.quote
        if transfer.is_dir:
            await self._exec_checked(
                f"mkdir -p {quote(transfer.target)}", f"mkdir {transfer.target}"
            )
            if any(path.is_file() for path in transfer.source.rglob("*")):
                await self.upload_dir(transfer.source, transfer.target)
            base = transfer.target
            names = sorted(path.name for path in transfer.source.iterdir())
        else:
            target = transfer.target
            if (
                transfer.maybe_dir
                and (await self.exec(f"test -d {quote(target)}")).return_code == 0
            ):
                target = posixpath.join(target, transfer.source.name)
            await self.upload_file(transfer.source, target)
            base, names = posixpath.dirname(target) or "/", [posixpath.basename(target)]
        if not names:
            return
        paths = " ".join(quote(name) for name in names)
        command = f"cd {quote(base)} && chown -R {quote(transfer.chown or '0:0')} -- {paths}"
        if transfer.chmod:
            command += f" && chmod -R {quote(transfer.chmod)} -- {paths}"
        await self._exec_checked(command, f"setting owner and mode of {transfer.target}")

    @override
    async def start(self, force_build: bool) -> None:
        if self.prebuilt_image:
            # Fail on a Dockerfile the image cannot stand in for before any cluster call.
            self._load_plan()
            if force_build:
                logger.debug("force_build is ignored: %s is prebuilt", self.prebuilt_image)
        await self._ensure_client()
        if not self.prebuilt_image:
            await self._ensure_image(force_build)

        manifest = self._sandbox_manifest()
        if owner := await self._owner_reference():
            manifest["metadata"]["ownerReferences"] = [owner]
        await asyncio.to_thread(
            self._custom_api().create_namespaced_custom_object,
            GROUP,
            VERSION,
            self.namespace,
            PLURAL,
            manifest,
        )
        await self._wait_for_sandbox_ready()

        # Agent Sandbox names the pod after the Sandbox, so the inherited exec and
        # file transfer code finds it through self.pod_name.
        dirs = f"{EnvironmentPaths.agent_dir} {EnvironmentPaths.verifier_dir}"
        result = await self.exec(f"mkdir -p {dirs} && chmod 777 {dirs}")
        if result.return_code != 0:
            raise RuntimeError(f"Failed to prepare sandbox {self.pod_name}: {result.stderr}")

        # The COPY steps a build would have baked into the task image.
        for transfer in self._transfers:
            await self._replay_transfer(transfer)

        await self._upload_environment_dir_after_start()

    @override
    async def stop(self, delete: bool) -> None:
        if self._client_manager is None:
            return
        await self._ensure_client()
        try:
            if delete:
                await asyncio.to_thread(
                    self._custom_api().delete_namespaced_custom_object,
                    GROUP,
                    VERSION,
                    self.namespace,
                    PLURAL,
                    self.pod_name,
                )
        except ApiException as e:
            if e.status != 404:
                raise RuntimeError(f"Failed to delete sandbox {self.pod_name}: {e}") from e
        finally:
            try:
                await self._client_manager.release_client()
            except Exception as e:
                logger.error(f"Error releasing Kubernetes client: {e}")
            finally:
                self._client_manager = None
                self._core_api = None
                self._dynamic_client = None
