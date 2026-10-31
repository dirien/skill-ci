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

Where the sandbox comes from:

* **Warm pool.** ``HARBOR_SANDBOX_WARM_POOL`` names a ``SandboxWarmPool`` (a skill-ci
  ``SandboxProfile``). The trial claims a pod from it with an ``extensions.agents.x-k8s.io``
  ``SandboxClaim``: image, CPU and memory come from the profile's template. The claim cannot
  change the container, so the task's ``ENV`` and ``WORKDIR`` are applied to every command
  instead, and a ``USER`` other than root fails the trial.
* **Bare Sandbox.** Without it, the trial creates its own ``Sandbox`` with the task's size.

Both paths label the trial pod ``sandbox.users.io/skillci-eval: <SkillEval>`` (from
``SKILLEVAL_NAME``, else the runner pod's Job name) and ``sandbox.users.io/skillci-role: trial``,
which is what the SkillEval's network policy selects, and ``sandbox.users.io/skillci-trial:
<trial label>`` (:func:`trial_label` of the Harbor trial name), which the runner uses to map
Hubble flows to their trial (runner/flows.py imports :func:`trial_label` from here).

Usage::

    HARBOR_SANDBOX_IMAGE=ghcr.io/dirien/skill-ci-sandbox:0.7.0 \\
    harbor run ... -e harbor_agent_sandbox:AgentSandboxEnvironment --ek namespace=skill-ci
"""

from __future__ import annotations

import asyncio
import hashlib
import io
import logging
import os
import posixpath
import re
import shlex
import tarfile
import time
from pathlib import Path
from typing import Any, cast, override

from harbor.environments.ack import ACKEnvironment
from harbor.environments.base import ExecResult
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

# The warm-pool path: a SandboxClaim on a SandboxWarmPool (the skill-ci SandboxProfile).
WARM_POOL_ENV = "HARBOR_SANDBOX_WARM_POOL"
CLAIM_GROUP = "extensions.agents.x-k8s.io"
CLAIM_PLURAL = "sandboxclaims"
CLAIM_POLL_SEC = 0.5
# Ready=False reasons that will not resolve by waiting; the condition message says why.
CLAIM_FAILURES = frozenset({"InvalidMetadata", "EnvVarsInjectionRejected"})

# Trial pod labels. Agent Sandbox applies claim labels only in its allowed domain, which is
# sandbox.users.io by default; the SkillEval's network policy selects both.
SKILLEVAL_NAME_ENV = "SKILLEVAL_NAME"
EVAL_LABEL = "sandbox.users.io/skillci-eval"
ROLE_LABEL = "sandbox.users.io/skillci-role"
# Maps network flows to their trial; no policy selects it (CONTRACT.md "Network modes").
TRIAL_LABEL = "sandbox.users.io/skillci-trial"
JOB_NAME_LABEL = "batch.kubernetes.io/job-name"
LABEL_VALUE = re.compile(r"^[A-Za-z0-9]([A-Za-z0-9._-]{0,61}[A-Za-z0-9])?$")

# Over-long or odd trial names: the first 52 characters, "-" and 10 hex characters of the
# SHA-256, the rule of the controller's policy names.
_LABEL_PREFIX_CHARS = 52
_LABEL_HASH_CHARS = 10
_NOT_LABEL_CHAR = re.compile(r"[^A-Za-z0-9._-]")
# Harbor's environment session ids: "<trial name>__env", and "<trial name>__verifier__<key>"
# for a separate verifier environment (harbor/trial/trial.py).
_ENV_SUFFIX = "__env"
_VERIFIER_MARK = "__verifier__"

# Where a pod finds its own namespace and name.
SA_NAMESPACE_FILE = Path("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
HOSTNAME_FILE = Path("/etc/hostname")

# Notices logged once per process, not once per trial.
_logged_once: set[str] = set()


def _log_once(key: str, message: str, *args: Any) -> None:
    """Log ``message`` at info level the first time ``key`` is seen in this process."""
    if key not in _logged_once:
        _logged_once.add(key)
        logger.info(message, *args)


def trial_label(trial_id: str) -> str:
    """Return the value of the ``sandbox.users.io/skillci-trial`` label for a trial id.

    The Harbor trial name (``rn-basic__sryqofs``) when it is a valid label value of at most 63
    characters; otherwise its first 52 characters, ``-`` and the first 10 hex characters of
    its SHA-256 (the rule of the controller's policy names). Characters a label value cannot
    hold are replaced by ``-`` in that prefix, and leading or trailing punctuation is dropped,
    so the result is always a valid label value. This is the one implementation: the runner
    (runner/flows.py) imports it to map flows to trials.
    """
    if LABEL_VALUE.match(trial_id):
        return trial_id
    digest = hashlib.sha256(trial_id.encode()).hexdigest()[:_LABEL_HASH_CHARS]
    prefix = _NOT_LABEL_CHAR.sub("-", trial_id[:_LABEL_PREFIX_CHARS]).lstrip("._-")
    return f"{prefix}-{digest}" if prefix else digest


def trial_name_from_session(session_id: str) -> str:
    """Return the Harbor trial name behind an environment session id.

    Harbor names the trial environment ``<trial name>__env`` and a separate verifier
    environment ``<trial name>__verifier__<key>``; both belong to the same trial. Any other
    session id is returned as it is.
    """
    if session_id.endswith(_ENV_SUFFIX):
        return session_id[: -len(_ENV_SUFFIX)]
    name, mark, _ = session_id.partition(_VERIFIER_MARK)
    return name if mark and name else session_id


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
    With ``HARBOR_SANDBOX_WARM_POOL`` (or ``warm_pool``) the trial claims a pod from that warm pool
    instead; its template decides image, size, runtime class, scheduling and pull secrets, and
    nothing is built either.
    """

    def __init__(
        self,
        *args: Any,
        runtime_class: str | None = None,
        ready_timeout_sec: int = 600,
        prebuilt_image: str | None = None,
        warm_pool: str | None = None,
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
        # The warm pool and the SkillEval name come from the environment for the same reason.
        pool = warm_pool or os.environ.get(WARM_POOL_ENV) or ""
        self.warm_pool: str | None = pool.strip() or None
        self.skilleval_name: str | None = (os.environ.get(SKILLEVAL_NAME_ENV) or "").strip() or None
        self._claim_name: str | None = None  # set once the SandboxClaim is created
        self._runner_pod_read = False
        self._runner_pod_object: Any = None
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
        On the warm-pool path the profile sizes the pod and both variables are ignored.
        """
        if self.warm_pool:
            if os.environ.get("HARBOR_SANDBOX_CPUS") or os.environ.get("HARBOR_SANDBOX_MEMORY_MB"):
                _log_once(
                    "size",
                    "HARBOR_SANDBOX_CPUS and HARBOR_SANDBOX_MEMORY_MB are ignored: warm pool %s "
                    "(its sandbox profile) sizes the sandbox",
                    self.warm_pool,
                )
            return
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
        a ``USER`` that is not root or a uid (not root at all on the warm-pool path).
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
            if _run_as_user(plan) != 0 and self.warm_pool:
                raise dockerfile.DockerfileError(
                    f"USER {plan.user} is not supported with a warm pool: the sandbox profile's "
                    "pod runs as root and a claim cannot change that; drop the USER step"
                )
        except dockerfile.DockerfileError as e:
            raise dockerfile.DockerfileError(
                f"task {self.environment_name} cannot run on {self._sandbox_source()}: {e}"
            ) from e
        self._plan, self._transfers = plan, transfers
        return plan

    def _replays_dockerfile(self) -> bool:
        """Return True when nothing is built and the task Dockerfile is replayed at start."""
        return bool(self.prebuilt_image or self.warm_pool)

    def _sandbox_source(self) -> str:
        """Describe what the trial runs on, for error messages."""
        if self.warm_pool:
            return f"warm pool {self.warm_pool}"
        return self.prebuilt_image or self._get_image_url()

    def _trial_labels(self) -> dict[str, str]:
        """Return the labels of every trial pod; the eval label only when the name is known.

        Only the ``sandbox.users.io`` domain: a SandboxClaim may put no other label on the pod.
        The trial label carries :func:`trial_label` of the Harbor trial name.
        """
        labels = {ROLE_LABEL: "trial"}
        if self.skilleval_name:
            labels[EVAL_LABEL] = self.skilleval_name
        if trial := trial_name_from_session(self.session_id or ""):
            labels[TRIAL_LABEL] = trial_label(trial)
        return labels

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

        labels = self._object_labels()
        return {
            "apiVersion": f"{GROUP}/{VERSION}",
            "kind": "Sandbox",
            "metadata": {"name": self.pod_name, "namespace": self.namespace, "labels": labels},
            "spec": {
                "podTemplate": {
                    # A cluster scale-down must not kill a running trial.
                    "metadata": {
                        "labels": {**labels, **self._trial_labels()},
                        "annotations": {SAFE_TO_EVICT: "false"},
                    },
                    "spec": pod_spec,
                }
            },
        }

    def _object_labels(self) -> dict[str, str]:
        """Return the labels of the Sandbox or SandboxClaim object itself."""
        return {"app.kubernetes.io/managed-by": "harbor", "harbor/session": self.session_id[:63]}

    def _claim_manifest(self) -> dict[str, Any]:
        """Return the SandboxClaim that takes a pod from the warm pool.

        It sets no ``env``: claim env forces a cold start and Agent Sandbox rejects it by
        default. The task's ENV and WORKDIR are applied per command instead (see ``exec``).
        """
        return {
            "apiVersion": f"{CLAIM_GROUP}/{VERSION}",
            "kind": "SandboxClaim",
            "metadata": {
                "name": self.pod_name,
                "namespace": self.namespace,
                "labels": self._object_labels(),
            },
            "spec": {
                "warmPoolRef": {"name": self.warm_pool},
                "additionalPodMetadata": {"labels": self._trial_labels()},
            },
        }

    async def _ensure_image(self, force_build: bool) -> None:
        if force_build or not (self.skip_image_check or await self._image_exists()):
            if not self.registry:
                raise ValueError("Building a task image needs --ek registry=<registry>.")
            await self._build_and_push_image()

    async def _runner_pod(self) -> Any:
        """Return the pod running Harbor when it runs next to the sandboxes, else None.

        Read once per environment: the owner reference and the SkillEval name both use it.
        """
        if self._runner_pod_read:
            return self._runner_pod_object
        try:
            if SA_NAMESPACE_FILE.read_text().strip() != self.namespace:
                return None
            pod_name = HOSTNAME_FILE.read_text().strip()
        except OSError:
            return None  # not running in a pod
        if self._core_api is None:
            return None
        self._runner_pod_read = True
        try:
            self._runner_pod_object = await asyncio.to_thread(
                self._core_api.read_namespaced_pod, pod_name, self.namespace
            )
        except ApiException as e:
            logger.warning(
                "Sandbox %s gets no owner: reading pod %s failed: %s", self.pod_name, pod_name, e
            )
        return self._runner_pod_object

    async def _resolve_skilleval_name(self) -> None:
        """Fill in the SkillEval name from the runner pod's Job when ``SKILLEVAL_NAME`` is unset.

        The Job is named like the SkillEval (a retry adds ``-r<n>``, so the variable is the
        reliable source). A name that cannot be a label value is dropped with a warning: the
        trial then runs without the eval label and no eval network policy selects it.
        """
        if not self.skilleval_name and (pod := await self._runner_pod()) is not None:
            labels = getattr(pod.metadata, "labels", None) or {}
            self.skilleval_name = labels.get(JOB_NAME_LABEL) or None
        if self.skilleval_name and not LABEL_VALUE.match(self.skilleval_name):
            logger.warning(
                "SkillEval name %r is not a label value; sandbox %s has no %s label",
                self.skilleval_name,
                self.pod_name,
                EVAL_LABEL,
            )
            self.skilleval_name = None
        if not self.skilleval_name:
            # Only a warm-pool trial is behind an eval network policy that needs the label.
            (logger.warning if self.warm_pool else logger.debug)(
                "Sandbox %s has no %s label: the SkillEval is unknown", self.pod_name, EVAL_LABEL
            )

    async def _owner_reference(self) -> dict[str, Any] | None:
        """Return an owner reference to the pod running Harbor, if it runs next to the sandboxes.

        With it, Kubernetes deletes the sandboxes of a cancelled or crashed run
        together with the run's pod instead of leaving them to hold capacity.
        """
        pod = await self._runner_pod()
        if pod is None:
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

    async def _wait_for_claim_ready(self, claim_name: str) -> str:
        """Wait until the claim is Ready and its pod carries the trial labels; return the pod name.

        The pod is ``status.sandbox.name`` of the claim. Agent Sandbox patches the claim's labels
        onto a running warm pod, and the eval network policy selects the pod by them, so the
        trial does not start before they are there. Ready=False with a reason in
        ``CLAIM_FAILURES`` fails at once with the condition's message.
        """
        api = self._custom_api()
        core = self._core_api
        if core is None:
            raise RuntimeError("Kubernetes client is not initialized; call start() first.")
        wanted = self._trial_labels()
        deadline = time.monotonic() + self.ready_timeout_sec
        pod_name: str | None = None
        waiting_for = "Ready"
        while True:
            if pod_name is None:
                claim = cast(
                    "dict[str, Any]",
                    await asyncio.to_thread(
                        api.get_namespaced_custom_object,
                        CLAIM_GROUP, VERSION, self.namespace, CLAIM_PLURAL, claim_name,
                    ),
                )  # fmt: skip
                status = claim.get("status") or {}
                for cond in status.get("conditions") or []:
                    if cond.get("type") != "Ready":
                        continue
                    if cond.get("status") == "True":
                        pod_name = (status.get("sandbox") or {}).get("name") or None
                        waiting_for = "status.sandbox.name"
                    elif cond.get("reason") in CLAIM_FAILURES:
                        raise RuntimeError(
                            f"SandboxClaim {claim_name} on warm pool {self.warm_pool} failed: "
                            f"{cond['reason']}: {cond.get('message', '')}"
                        )
                    else:
                        waiting_for = f"Ready ({cond.get('reason')}: {cond.get('message', '')})"
            if pod_name is not None:
                waiting_for = f"labels {wanted} on pod {pod_name}"
                try:
                    pod = await asyncio.to_thread(
                        core.read_namespaced_pod, pod_name, self.namespace
                    )
                except ApiException as e:
                    if e.status != 404:
                        raise
                else:
                    labels = pod.metadata.labels or {}
                    if all(labels.get(key) == value for key, value in wanted.items()):
                        return pod_name
            if time.monotonic() >= deadline:
                raise RuntimeError(
                    f"SandboxClaim {claim_name} on warm pool {self.warm_pool} was not ready "
                    f"after {self.ready_timeout_sec}s, waiting for {waiting_for}"
                )
            await asyncio.sleep(CLAIM_POLL_SEC)

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

    async def _start_sandbox(self) -> None:
        """Create a bare Sandbox for the trial and wait until it is ready.

        Agent Sandbox names the pod after the Sandbox, so the inherited exec and file transfer
        code finds it through self.pod_name.
        """
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

    async def _start_claim(self) -> None:
        """Claim a pod from the warm pool and point self.pod_name at it.

        The claim is owned by the runner pod like a bare Sandbox, so a cancelled run releases
        it; deleting the claim deletes its Sandbox and pod.
        """
        manifest = self._claim_manifest()
        if owner := await self._owner_reference():
            manifest["metadata"]["ownerReferences"] = [owner]
        claim_name = manifest["metadata"]["name"]
        await asyncio.to_thread(
            self._custom_api().create_namespaced_custom_object,
            CLAIM_GROUP,
            VERSION,
            self.namespace,
            CLAIM_PLURAL,
            manifest,
        )
        self._claim_name = claim_name
        self.pod_name = await self._wait_for_claim_ready(claim_name)
        logger.debug("SandboxClaim %s runs in pod %s", claim_name, self.pod_name)

    @override
    def _merge_env(self, env: dict[str, str] | None) -> dict[str, str] | None:
        # On the warm-pool path the container env is the profile's: the task's ENV goes first
        # into every command, below anything the command or Harbor sets itself.
        merged = super()._merge_env(env)
        if self.warm_pool and self._plan is not None and self._plan.env:
            return {**self._plan.env, **(merged or {})}
        return merged

    @override
    async def exec(
        self,
        command: str,
        cwd: str | None = None,
        env: dict[str, str] | None = None,
        timeout_sec: int | None = None,
        user: str | int | None = None,
    ) -> ExecResult:
        # On the warm-pool path the container's workingDir is the profile's: commands without
        # a directory of their own start in the task's WORKDIR, as in a built image.
        if self.warm_pool and self._plan is not None and not cwd:
            cwd = self.task_env_config.workdir or shlex.quote(self._plan.workdir)
        return await super().exec(command, cwd=cwd, env=env, timeout_sec=timeout_sec, user=user)

    @override
    async def start(self, force_build: bool) -> None:
        if self._replays_dockerfile():
            # Fail on a Dockerfile the image cannot stand in for before any cluster call.
            self._load_plan()
            if force_build:
                logger.debug("force_build is ignored: %s is prebuilt", self._sandbox_source())
        await self._ensure_client()
        if not self._replays_dockerfile():
            await self._ensure_image(force_build)

        await self._resolve_skilleval_name()
        if self.warm_pool:
            await self._start_claim()
        else:
            await self._start_sandbox()

        dirs = f"{EnvironmentPaths.agent_dir} {EnvironmentPaths.verifier_dir}"
        command = f"mkdir -p {dirs} && chmod 777 {dirs}"
        if self.warm_pool and self._plan is not None:
            # WORKDIR creates its directory in a build; every later command starts in it.
            result = await self.exec(
                f"{command} && mkdir -p {shlex.quote(self._plan.workdir)}", "/"
            )
        else:
            result = await self.exec(command)
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
            if delete and self.warm_pool:
                if self._claim_name:
                    # Deleting the claim deletes its Sandbox and pod. Nothing waits for the pod.
                    await asyncio.to_thread(
                        self._custom_api().delete_namespaced_custom_object,
                        CLAIM_GROUP,
                        VERSION,
                        self.namespace,
                        CLAIM_PLURAL,
                        self._claim_name,
                        body=k8s_client.V1DeleteOptions(propagation_policy="Background"),
                    )
            elif delete:
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
                what = (
                    f"sandbox claim {self._claim_name}"
                    if self.warm_pool
                    else f"sandbox {self.pod_name}"
                )
                raise RuntimeError(f"Failed to delete {what}: {e}") from e
        finally:
            try:
                await self._client_manager.release_client()
            except Exception as e:
                logger.error(f"Error releasing Kubernetes client: {e}")
            finally:
                self._client_manager = None
                self._core_api = None
                self._dynamic_client = None
