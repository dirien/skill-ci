"""Run one SkillEval: pull a skill with ORAS, check it through all SkillEvaluator tiers.

The runner Job configures this entrypoint through environment variables (see
CONTRACT.md, "Runner Job contract" and "All tiers and publication"). A first
``skillevaluator validate --tiers 1,2`` (static and security checks, deduplication
against the other skills in Harbor) is the cheap gate. Only when neither tier
failed does a second ``skillevaluator validate`` run all three tiers, including
the live Tier 3 evaluation, in one invocation: its report then carries
SkillEvaluator's own publication assessment. A skill without a Tier 3 source (no
eval dataset, no native Harbor tasks) gets the gate only and then ends with an
error that says how to draft the dataset. On completion the runner writes one
JSON object to the container termination log and exits 0, or ``{"error": ...}``
and exits 1. An error after the gate ran keeps the gate's ``tiers`` and ``findings``
next to ``error``.

After the run the runner attaches two OCI referrers to the skill digest (CONTRACT.md, "Trial
bundles" and "Eval reports"): first the trial bundle (``trial_bundle.py``, with the network
flows ``flows.py`` collected during Tier 3), then the report, which names the bundle digest
or the reason there is none. Neither ever fails the eval. When Tier 3 started and the run
then failed, both are still attached and the error payload carries ``report``.
"""

from __future__ import annotations

import importlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from collections import Counter
from collections.abc import Callable, Iterable, Mapping
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit

TERMINATION_LOG = "/dev/termination-log"
MAX_MESSAGE_BYTES = 4000  # Kubernetes caps the termination message at 4096 bytes.
REPORT_TYPE = "application/vnd.skillci.report.v1+json"
MEDIA_TYPES = {".html": "text/html", ".json": "application/json", ".md": "text/markdown"}
# Order of the files in the report referrer. report.html and BENCHMARK.md are the combined
# SkillEvaluator report of the last ``validate`` run (it holds the publication assessment);
# result.json is the Tier 3 result when Tier 3 ran. validate.json (the combined JSON report)
# and tier3.html (the Tier 3 report) are attached only when Tier 3 ran.
REPORT_FILES = ("report.html", "result.json", "BENCHMARK.md", "validate.json", "tier3.html")
SECRET_ENV = (
    "ANTHROPIC_API_KEY",
    "OPENAI_API_KEY",
    "NVIDIA_API_KEY",
    "OLLAMA_API_KEY",
    "OPENROUTER_API_KEY",
)
# The judge key the controller mounts from llm-keys/<SkillCIConfig spec.judge.secretKey>.
JUDGE_KEY_ENV = "SKILL_EVAL_LLM_API_KEY"
# Every value ``redact`` removes from the log and the termination message.
REDACTED_ENV = (
    *SECRET_ENV,
    JUDGE_KEY_ENV,
    "SKILL_EVAL_EMBEDDING_API_KEY",
    "AWS_BEARER_TOKEN_BEDROCK",
)
# The agent CLI release each agent runs when the Job names none (AGENT_VERSION). claude-code and
# codex are the pins SkillEvaluator 0.6.0 ships (tier3/harbor/runner.py: _CLAUDE_CODE_VERSION,
# _CODEX_VERSION; a test compares them). Upstream pins no OpenCode release, so Harbor would
# install the latest one per trial; 1.19.1 (npm opencode-ai, GitHub anomalyco/opencode v1.19.1,
# published 2026-10-15) is the newest release that was at least two weeks old on 2026-10-30.
DEFAULT_AGENT_VERSIONS = {"claude-code": "2.1.292", "codex": "0.161.0", "opencode": "1.19.1"}
# A version reaches `npm install -g pkg@<version>` in every sandbox: letters, digits, dot, dash,
# underscore only, which is also a valid Kubernetes label value.
AGENT_VERSION_PATTERN = re.compile(r"^[A-Za-z0-9]([A-Za-z0-9._-]{0,61}[A-Za-z0-9])?$")
# A container image reference (registry, repository, tag or digest) and a Kubernetes Secret name.
IMAGE_REFERENCE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,254}$")
SECRET_NAME = re.compile(r"^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$")
OPENAI_DEFAULT_BASE_URL = "https://api.openai.com/v1"
ANTHROPIC_DEFAULT_BASE_URL = "https://api.anthropic.com"
NVIDIA_DEFAULT_BASE_URL = "https://integrate.api.nvidia.com/v1"
BEDROCK_DEFAULT_REGION = "us-west-2"  # SkillEvaluator's default when AWS_REGION is unset
# The judge providers SkillEvaluator 0.6.0 supports (provider_config.py, _SUPPORTED_PROVIDERS)
# and the variable its resolve_llm_provider reads the key from: anthropic and nv_build read only
# their own key, openai reads OPENAI_API_KEY and then SKILL_EVAL_LLM_API_KEY, openai-compatible
# reads SKILL_EVAL_LLM_API_KEY (with SKILL_EVAL_LLM_BASE_URL, both required), and bedrock uses the
# AWS credential chain, where a Bedrock API key is AWS_BEARER_TOKEN_BEDROCK.
JUDGE_PROVIDERS = {
    "anthropic": "ANTHROPIC_API_KEY",
    "openai": "OPENAI_API_KEY",
    "openai-compatible": JUDGE_KEY_ENV,
    "nv_build": "NVIDIA_API_KEY",
    "bedrock": "AWS_BEARER_TOKEN_BEDROCK",
}
# The judges each agent can run under. SkillEvaluator chooses the agent's credentials from the
# judge provider (tier3/harbor/runner.py, _agent_credentials): under openai-compatible, Codex
# would call the judge's gateway; under bedrock, Claude Code would run on Bedrock and Codex
# would get no key; and the OpenCode routes of skill-ci (patch hunks 8 to 11) act only under
# the anthropic judge. An agent not listed here is not restricted.
AGENT_JUDGES = {
    "claude-code": ("anthropic", "openai", "openai-compatible", "nv_build"),
    "codex": ("anthropic", "openai", "nv_build"),
    "opencode": ("anthropic",),
}
# Other AWS credential sources a Bedrock judge can use instead of a Bedrock API key.
BEDROCK_CREDENTIAL_ENV = (
    "AWS_ACCESS_KEY_ID",
    "AWS_WEB_IDENTITY_TOKEN_FILE",
    "AWS_CONTAINER_CREDENTIALS_FULL_URI",
    "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
    "AWS_PROFILE",
)
OPENAI_EMBEDDING_MODEL = "text-embedding-3-small"
# OpenCode providers served through an OpenAI-compatible Chat Completions endpoint: the model
# `<provider>/<model id>` runs with the key from llm-keys and the base URL below (patch hunks 8-11).
GATEWAY_PROVIDERS = {
    "ollama-cloud": ("OLLAMA_API_KEY", "https://ollama.com/v1"),
    "openrouter": ("OPENROUTER_API_KEY", "https://openrouter.ai/api/v1"),
    "nvidia": ("NVIDIA_API_KEY", "https://integrate.api.nvidia.com/v1"),
    "openai": ("OPENAI_API_KEY", OPENAI_DEFAULT_BASE_URL),
}
# The Tier 1 + 2 gate or one catalog build, scanners and embedding calls included.
STATIC_TIMEOUT_SEC = 1200
RETRY_DELAYS = (2, 5, 10)  # seconds to wait before each retry of a failed oras call
# Harbor's wait before trial retry n (0, 1, ...) is min(min_wait_sec * wait_multiplier**n,
# max_wait_sec): 20 s, 40 s, 80 s and so on. Provider rate limits are per minute, so Harbor's
# own default (1 s, doubling, at most 60 s) would mostly hit the same window again.
TRIAL_RETRY_BACKOFF = {"wait_multiplier": 2, "min_wait_sec": 20, "max_wait_sec": 300}
TRIAL_RETRY_CONFIG = "harbor-retry.json"  # in the work dir, read by Harbor (patch hunk 13)
MAX_FINDINGS = 5
MAX_FINDING_CHARS = 200
MAX_CATALOG_SKILLS = 200  # other skills compared against in Tier 2
MAX_SAMPLES = 50  # per-trial scores kept per arm; 3 cases x 3 attempts give 9
SEMVER_TAG = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")
SEVERITY_RANK = {"critical": 0, "high": 1, "medium": 2, "low": 3}
# The eval dataset files SkillEvaluator 0.6.0 reads from <skill>/evals/, in its order
# (tier3/harbor/adapter.py: find_evals_file). Native Harbor tasks under evals/harbor/ are the
# other Tier 3 source. The runner passes --no-autopilot, so SkillEvaluator never drafts one.
EVAL_DATASET_FILES = (
    "evals.json",
    "evals.jsonl",
    "evals.yaml",
    "evals.yml",
    "dataset.json",
    "dataset.jsonl",
)
NATIVE_TASKS_DIR = "harbor"
# The error of a skill without a Tier 3 source; tests and docs quote it, keep it stable.
NO_DATASET_ERROR = (
    "no eval dataset: the live evaluation (Tier 3) needs test cases in evals/evals.json. "
    "Draft them with `make draft-evals SKILL={skill}` (skillevaluator create-eval-dataset), "
    "review them, and push again"
)

# The trial bundle and the network flows (CONTRACT.md, "Trial bundles"). The modules sit next to
# this file in the runner image; they are imported when needed, so a broken one never fails a run.
BUNDLE_MODULE = "trial_bundle"
FLOWS_MODULE = "flows"
BUNDLE_TYPE = "application/vnd.skillci.trials.v1+json"
TRIALS_ANNOTATION = "skillci.dev/trials"
WITHHELD_ANNOTATION = "skillci.dev/trials-withheld"
WITHHELD_BUILD_FAILED = "build-failed"
WITHHELD_ATTACH_FAILED = "attach-failed"
WITHHELD_DISABLED = "disabled"
WITHHELD_NO_TRIALS = "no-trials"
DEFAULT_FLOW_ROWS = 200  # trial_bundle.Limits.flow_rows, when the limits cannot be read
NETWORK_OFF = {
    "state": "off",
    "reason": "no Hubble Relay configured (SkillCIConfig spec.observability.hubbleRelay is empty)",
}

# The generic sandbox image of the release the runner belongs to. runner/Dockerfile sets
# SANDBOX_IMAGE to its own release tag; this is the fallback outside the image.
DEFAULT_SANDBOX_IMAGE = "ghcr.io/dirien/skill-ci-sandbox:latest"

# Defaults mirror the runner image; the Job can override any of them.
DEFAULTS = {
    "ATTEMPTS": "1",
    "REGISTRY_CONFIG": "/auth/config.json",
    "REGISTRY_PLAIN_HTTP": "true",
    "SANDBOX_NAMESPACE": "skill-ci",
    # Every trial runs in this image and nothing is built. The task-specific steps of the
    # generated Dockerfile (COPY skills, ENV, WORKDIR) are replayed when the sandbox starts.
    "SANDBOX_IMAGE": DEFAULT_SANDBOX_IMAGE,
    # Names of image pull secrets for the sandbox pods, comma separated. None: public image.
    "SANDBOX_PULL_SECRETS": "",
    # Bring your own builder: with a registry here the trials build and push a task image each
    # (Harbor's ack build, needs push rights, TASK_IMAGE_PULL_SECRET and a builder you run) and
    # SANDBOX_IMAGE is not used.
    "TASK_IMAGE_REGISTRY": "",
    "TASK_IMAGE_PULL_SECRET": "",
    # Skill tasks write a few files; the 2 CPU / 4 GiB task default starves a small cluster. The
    # size reaches the sandbox as HARBOR_SANDBOX_CPUS / HARBOR_SANDBOX_MEMORY_MB (tool_env).
    "SANDBOX_CPUS": "1",
    "SANDBOX_MEMORY_MB": "2048",
    # The SandboxWarmPool of the eval's sandbox profile (set by the controller). With it the
    # trials claim pods from that pool and the profile, not SANDBOX_CPUS/SANDBOX_MEMORY_MB,
    # sizes them. Without it every trial creates its own Sandbox.
    "SANDBOX_WARM_POOL": "",
    # Trials running at once per eval; the controller caps how many evals run at once.
    "TRIAL_CONCURRENCY": "2",
    # How often Harbor runs a trial again that failed with ApiRateLimitError (patch hunk 13),
    # waiting TRIAL_RETRY_BACKOFF in between. 0 turns trial retries off.
    "TRIAL_RETRIES": "2",
    # The sandbox image has the agent CLIs at the default pins, so a trial does not install
    # them; another AGENT_VERSION is still installed in every sandbox, and on a busy cluster
    # that can outlast Harbor's 360 s agent setup timeout.
    "TIMEOUT_MULTIPLIER": "2",
    # SkillEvaluator validation profile for Tier 1. See runner/README or DEMO.md "Profile".
    "TIER1_PROFILE": "external",
}
REQUIRED = ("SKILL_REF", "SKILL_NAME", "AGENT", "MODEL")


class EvalError(Exception):
    """A failure that ends the run with an ``{"error": ...}`` termination message.

    ``tiers`` and ``findings`` are the gate's outcome when the failure came after the Tier 1 + 2
    gate ran; the termination message then carries them next to the error. ``report`` is the
    digest of the report attached after Tier 3 started and failed.
    """

    def __init__(
        self,
        message: str,
        tiers: Mapping[str, str] | None = None,
        findings: Iterable[str] = (),
        report: str | None = None,
    ) -> None:
        super().__init__(message)
        self.tiers = dict(tiers) if tiers is not None else None
        self.findings = list(findings)
        self.report = report


def load_config(environ: Mapping[str, str]) -> dict[str, str]:
    """Return the runner configuration from the environment, applying defaults."""
    missing = [name for name in REQUIRED if not environ.get(name)]
    if missing:
        raise EvalError(f"missing required env: {', '.join(missing)}")
    config = {name: environ[name] for name in REQUIRED}
    for name, default in DEFAULTS.items():
        config[name] = environ.get(name) or default
    config["AGENT_VERSION"] = agent_version(environ)
    if config["AGENT_VERSION"] and not AGENT_VERSION_PATTERN.match(config["AGENT_VERSION"]):
        raise EvalError(
            f"AGENT_VERSION must be a release like 2.1.292, got {config['AGENT_VERSION']!r}"
        )
    if not builds_task_images(config):
        if not IMAGE_REFERENCE.match(config["SANDBOX_IMAGE"]):
            raise EvalError(
                f"SANDBOX_IMAGE must be an image reference, got {config['SANDBOX_IMAGE']!r}"
            )
        secrets = [n.strip() for n in config["SANDBOX_PULL_SECRETS"].split(",") if n.strip()]
        if bad := [n for n in secrets if not SECRET_NAME.match(n)]:
            raise EvalError(f"SANDBOX_PULL_SECRETS must list Secret names, got {bad[0]!r}")
        config["SANDBOX_PULL_SECRETS"] = ",".join(secrets)
        config["SANDBOX_WARM_POOL"] = config["SANDBOX_WARM_POOL"].strip()
        if config["SANDBOX_WARM_POOL"] and not SECRET_NAME.match(config["SANDBOX_WARM_POOL"]):
            raise EvalError(
                "SANDBOX_WARM_POOL must be a SandboxWarmPool name, "
                f"got {config['SANDBOX_WARM_POOL']!r}"
            )
    config["AGENT_MODEL"] = agent_route(config["AGENT"], config["MODEL"], environ).model
    # Both go into the trial bundle; the controller always sets SKILLEVAL_NAME.
    config["SKILLEVAL_NAME"] = (environ.get("SKILLEVAL_NAME") or "").strip()
    config["SKILL_VERSION"] = (environ.get("SKILL_VERSION") or "").strip()
    for name in ("ATTEMPTS", "SANDBOX_CPUS", "SANDBOX_MEMORY_MB", "TRIAL_CONCURRENCY"):
        if not config[name].isdigit() or int(config[name]) < 1:
            raise EvalError(f"{name} must be a positive integer, got {config[name]!r}")
    config["TRIAL_RETRIES"] = config["TRIAL_RETRIES"].strip()
    if not config["TRIAL_RETRIES"].isdigit():
        raise EvalError(
            f"TRIAL_RETRIES must be a whole number (0 is off), got {config['TRIAL_RETRIES']!r}"
        )
    try:
        multiplier = float(config["TIMEOUT_MULTIPLIER"])
    except ValueError:
        multiplier = 0.0
    if multiplier <= 0:
        raise EvalError(
            f"TIMEOUT_MULTIPLIER must be a positive number, got {config['TIMEOUT_MULTIPLIER']!r}"
        )
    return config


def agent_version(environ: Mapping[str, str]) -> str:
    """Return the agent CLI release to run: ``AGENT_VERSION``, else the default pin of ``AGENT``."""
    return (environ.get("AGENT_VERSION") or "").strip() or DEFAULT_AGENT_VERSIONS.get(
        environ.get("AGENT", ""), ""
    )


@dataclass(frozen=True)
class AgentRoute:
    """How the agent under test reaches its model.

    ``model`` is the value for ``--agent-model``; ``env`` the variables that replace the Job's
    for this run. ``gateway`` is true for OpenCode behind an OpenAI-compatible endpoint.
    """

    model: str
    env: Mapping[str, str] = field(default_factory=dict)
    gateway: bool = False


def agent_route(agent: str, model: str, environ: Mapping[str, str]) -> AgentRoute:
    """Choose the credential route of ``agent`` running ``model``.

    Claude Code and Codex use the keys of the Job as they are (Codex needs ``OPENAI_API_KEY``
    and ``OPENAI_BASE_URL``). OpenCode models are ``<provider>/<model>``: ``anthropic/...`` runs
    natively with the Anthropic key; a provider from ``GATEWAY_PROVIDERS`` runs through its
    OpenAI-compatible endpoint, with that provider's key presented as ``OPENAI_API_KEY`` to the
    agent only. The judge and Tier 2 keep their own providers (see ``tool_env``).
    """
    if agent != "opencode":
        return AgentRoute(model)
    provider, _, model_id = model.partition("/")
    if not model_id:
        raise EvalError(f"opencode needs a <provider>/<model> model, got {model!r}")
    if provider == "anthropic":
        return AgentRoute(model)
    if provider not in GATEWAY_PROVIDERS:
        known = ", ".join(["anthropic", *GATEWAY_PROVIDERS])
        raise EvalError(f"opencode provider {provider!r} is not supported, use one of: {known}")
    key_env, base_url = GATEWAY_PROVIDERS[provider]
    key = environ.get(key_env, "").strip()
    if not key:
        raise EvalError(f"opencode model {model!r} needs {key_env} (llm-keys) in the Job")
    if provider == "openai":
        base_url = environ.get("OPENAI_BASE_URL") or base_url
    return AgentRoute(
        f"openai/{model_id}",
        {
            "OPENAI_API_KEY": key,
            "OPENAI_BASE_URL": base_url,
            "SKILLEVALUATOR_OPENCODE_GATEWAY": "1",
        },
        gateway=True,
    )


def check_gateway_key(
    model: str,
    environ: Mapping[str, str],
    fetch: Callable[[urllib.request.Request], bytes] | None = None,
) -> None:
    """Fail before any work when an OpenRouter key cannot run models.

    OpenRouter provisioning (management) keys pass the key lookup but every chat call answers
    401 "User not found.", so each OpenCode trial would fail the same way. ``GET /key`` says which
    kind a key is. Only a confirmed provisioning key fails the run: a network error or an
    unexpected answer leaves the decision to the trials.
    """
    if not model.startswith("openrouter/"):
        return
    key_env, base_url = GATEWAY_PROVIDERS["openrouter"]
    headers = {"Authorization": f"Bearer {environ.get(key_env, '')}"}
    request = urllib.request.Request(f"{base_url}/key", headers=headers)  # noqa: S310 (fixed https URL)
    if fetch is None:

        def fetch(req: urllib.request.Request) -> bytes:
            with urllib.request.urlopen(req, timeout=15) as response:  # noqa: S310 (fixed https URL)
                return response.read()

    try:
        data = json.loads(fetch(request)).get("data") or {}
    except (urllib.error.URLError, TimeoutError, ValueError, AttributeError) as err:
        print(f"skill-eval: could not check the OpenRouter key type: {err}", flush=True)
        return
    if data.get("is_provisioning_key") is True:
        raise EvalError(
            f"{key_env} (llm-keys key openrouter) is an OpenRouter provisioning key, which cannot "
            "run models; create an API key at https://openrouter.ai/settings/keys"
        )


def host_of(url: str) -> str:
    """Return the host name of an http(s) URL, or an empty string when it has none."""
    try:
        parts = urlsplit(url.strip())
    except ValueError:
        return ""
    if parts.scheme not in ("http", "https"):
        return ""
    return parts.hostname or ""


def judge_provider(environ: Mapping[str, str]) -> str:
    """Return the judge provider: ``SKILL_EVAL_LLM_PROVIDER``, else the one the Job's keys imply.

    Without ``SKILL_EVAL_LLM_PROVIDER`` (a Job from a controller that predates
    SkillCIConfig ``spec.judge``) the judge is ``anthropic`` when there is an Anthropic key,
    else ``openai`` when there is an OpenAI key, else none (empty string).
    """
    if provider := (environ.get("SKILL_EVAL_LLM_PROVIDER") or "").strip().lower():
        return provider
    if (environ.get("ANTHROPIC_API_KEY") or "").strip():
        return "anthropic"
    if (environ.get("OPENAI_API_KEY") or "").strip():
        return "openai"
    return ""


@dataclass(frozen=True)
class Judge:
    """The LLM judge of Tier 3 as SkillEvaluator will resolve it from the Job's environment.

    ``key_env`` names the variable the key is read from (empty when the Job has none), ``host``
    the endpoint the judge calls (empty when the base URL has no host).
    """

    provider: str
    key_env: str
    host: str


def judge_of(environ: Mapping[str, str]) -> Judge | None:
    """Describe the judge of the Job ``environ`` (before ``tool_env``), or ``None`` without one.

    The key: ``SKILL_EVAL_LLM_API_KEY`` stands in for the provider's own variable when that is
    unset (``tool_env`` copies it there), except for ``openai``, where SkillEvaluator reads
    ``OPENAI_API_KEY`` first. The host: ``SKILL_EVAL_LLM_BASE_URL`` when set, else the provider's
    own base URL variable (``ANTHROPIC_BASE_URL``, ``OPENAI_BASE_URL``), else its public
    endpoint; Bedrock's is ``bedrock-runtime.<AWS_REGION>.amazonaws.com``.
    """
    provider = judge_provider(environ)
    if not provider:
        return None

    def value(name: str) -> str:
        return (environ.get(name) or "").strip()

    base_url = value("SKILL_EVAL_LLM_BASE_URL")
    native = JUDGE_PROVIDERS.get(provider, "")
    key_env = native if native and value(native) else ""
    if provider == "anthropic":
        base_url = base_url or value("ANTHROPIC_BASE_URL") or ANTHROPIC_DEFAULT_BASE_URL
    elif provider == "openai":
        base_url = base_url or value("OPENAI_BASE_URL") or OPENAI_DEFAULT_BASE_URL
    elif provider == "nv_build":
        base_url = base_url or NVIDIA_DEFAULT_BASE_URL
    elif provider == "bedrock":
        region = value("AWS_REGION") or BEDROCK_DEFAULT_REGION
        base_url = f"https://bedrock-runtime.{region}.amazonaws.com"
        if not key_env and not value(JUDGE_KEY_ENV):
            key_env = next((name for name in BEDROCK_CREDENTIAL_ENV if value(name)), "")
    if not key_env and native and value(JUDGE_KEY_ENV):
        key_env = JUDGE_KEY_ENV if provider in ("openai", "openai-compatible") else native
    return Judge(provider, key_env, host_of(base_url))


def check_judge(config: Mapping[str, str], environ: Mapping[str, str]) -> Judge:
    """Fail before any work when the Tier 3 judge cannot run; return it otherwise.

    The judge's key is required, whatever the provider (SkillCIConfig ``spec.judge``: the Job
    gets ``SKILL_EVAL_LLM_PROVIDER`` and the key from llm-keys/<secretKey> as
    ``SKILL_EVAL_LLM_API_KEY``), and the judge must be one the agent can run under
    (``AGENT_JUDGES``). The agent's own key is checked too, since the Anthropic key is no longer
    implied by the judge: Claude Code and OpenCode on ``anthropic/...`` need ``ANTHROPIC_API_KEY``,
    Codex needs ``OPENAI_API_KEY`` (``agent_route`` checks the gateway keys), unless the judge is
    of the same provider, whose key SkillEvaluator then gives the agent.
    """
    judge = judge_of(environ)
    if judge is None:
        raise EvalError(
            "no judge for Tier 3: set SkillCIConfig spec.judge (SKILL_EVAL_LLM_PROVIDER and the "
            "llm-keys key spec.judge.secretKey), or the llm-keys key anthropic"
        )
    if judge.provider not in JUDGE_PROVIDERS:
        raise EvalError(
            f"SKILL_EVAL_LLM_PROVIDER must be one of: {', '.join(sorted(JUDGE_PROVIDERS))}, "
            f"got {judge.provider!r}"
        )
    if judge.provider == "openai-compatible" and not environ.get("SKILL_EVAL_LLM_BASE_URL"):
        raise EvalError(
            "the openai-compatible judge needs SKILL_EVAL_LLM_BASE_URL (SkillCIConfig "
            "spec.judge.baseURL)"
        )
    if not judge.host:
        raise EvalError(
            f"the {judge.provider} judge needs an http(s) base URL with a host "
            "(SKILL_EVAL_LLM_BASE_URL, SkillCIConfig spec.judge.baseURL)"
        )
    if not judge.key_env:
        native = JUDGE_PROVIDERS[judge.provider]
        names = " or ".join(dict.fromkeys((JUDGE_KEY_ENV, native)))
        if judge.provider == "bedrock":
            names += " (or AWS credentials)"
        raise EvalError(
            f"the {judge.provider} judge needs its key: {names} in the Job, from the llm-keys "
            "key named by SkillCIConfig spec.judge.secretKey"
        )
    agent = config["AGENT"]
    allowed = AGENT_JUDGES.get(agent)
    if allowed is not None and judge.provider not in allowed:
        raise EvalError(
            f"{agent} cannot run under the {judge.provider} judge (SkillEvaluator would route the "
            f"agent through the judge's provider); use one of: {', '.join(allowed)}"
        )
    route = agent_route(agent, config["MODEL"], environ)
    agent_key = "" if route.gateway else {"codex": "OPENAI_API_KEY"}.get(agent, "ANTHROPIC_API_KEY")
    # Under the judge of the agent's own provider SkillEvaluator hands the agent the judge's key.
    shared = agent_key == JUDGE_PROVIDERS[judge.provider]
    if (
        agent in AGENT_JUDGES
        and agent_key
        and not shared
        and not (environ.get(agent_key) or "").strip()
    ):
        source = "openai" if agent_key == "OPENAI_API_KEY" else "anthropic"
        raise EvalError(f"{agent} needs {agent_key} (llm-keys key {source}) in the Job")
    native = JUDGE_PROVIDERS[judge.provider]
    given = (environ.get(JUDGE_KEY_ENV) or "").strip()
    native_value = (environ.get(native) or "").strip()
    if given and native != JUDGE_KEY_ENV and judge.key_env == native and given != native_value:
        print(
            f"skill-eval: the {judge.provider} judge reads {native}; {JUDGE_KEY_ENV} holds "
            "another key and is not used",
            file=sys.stderr,
        )
    return judge


def registry_flags(config: Mapping[str, str]) -> list[str]:
    """Return the oras flags that select the registry credentials and transport."""
    flags = []
    if Path(config["REGISTRY_CONFIG"]).is_file():
        flags += ["--registry-config", config["REGISTRY_CONFIG"]]
    if config["REGISTRY_PLAIN_HTTP"].lower() == "true":
        flags.append("--plain-http")
    return flags


def run_oras(cmd: list[str], **kwargs: Any) -> subprocess.CompletedProcess:
    """Run an oras command, retrying up to three times after a non-zero exit.

    Harbor sits behind an nginx that now and then answers 502; a short backoff
    (see ``RETRY_DELAYS``) rides that out. Returns the last result.
    """
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False, **kwargs)
    for delay in RETRY_DELAYS:
        if proc.returncode == 0:
            break
        print(
            f"skill-eval: oras {cmd[1]} failed ({proc.returncode}), retrying in {delay}s",
            file=sys.stderr,
        )
        time.sleep(delay)
        proc = subprocess.run(cmd, capture_output=True, text=True, check=False, **kwargs)
    return proc


def pull_skill(config: Mapping[str, str], dest: Path) -> Path:
    """Pull the skill artifact into ``dest`` and return the skill directory."""
    cmd = ["oras", "pull", config["SKILL_REF"], "--output", str(dest), *registry_flags(config)]
    proc = run_oras(cmd)
    if proc.returncode != 0:
        raise EvalError(f"oras pull failed ({proc.returncode}): {proc.stderr.strip()}")
    skill_dir = dest / config["SKILL_NAME"]
    if not skill_dir.is_dir():
        raise EvalError(f"artifact has no directory named {config['SKILL_NAME']!r}")
    return skill_dir


def has_tier3_source(skill_dir: Path) -> bool:
    """Return True when the skill has something Tier 3 can evaluate.

    That is an eval dataset in ``evals/`` (any of ``EVAL_DATASET_FILES``) or native Harbor
    tasks in ``evals/harbor/``, the same test SkillEvaluator's autopilot makes before it
    reuses an existing source. Whether the source is valid is SkillEvaluator's call.
    """
    evals = skill_dir / "evals"
    return (
        any((evals / name).exists() for name in EVAL_DATASET_FILES)
        or (evals / NATIVE_TASKS_DIR).exists()
    )


@dataclass
class TierResult:
    """Outcome of one tier: its status (passed | failed | incomplete) and short findings."""

    status: str
    findings: list[str] = field(default_factory=list)


@dataclass
class ValidateReport:
    """What one ``skillevaluator validate`` run reported.

    ``tiers`` holds the Tier 1 and Tier 2 statuses; ``publication`` is SkillEvaluator's own
    publication assessment mapped to ``passed | failed | incomplete``.
    """

    tiers: dict[str, str]
    findings: list[str]
    publication: str
    json_path: Path | None = None
    html_path: Path | None = None
    benchmark_path: Path | None = None


def builds_task_images(settings: Mapping[str, str]) -> bool:
    """Return True when trials build task images in a registry of the operator's own.

    That is the optional "bring your own builder" path, selected by ``TASK_IMAGE_REGISTRY``.
    Otherwise every trial runs the generic sandbox image and nothing is built.
    """
    return bool((settings.get("TASK_IMAGE_REGISTRY") or "").strip())


def tool_env(environ: Mapping[str, str]) -> dict[str, str]:
    """Return the environment for every SkillEvaluator command (all three tiers).

    SkillEvaluator refuses to guess when several provider keys exist, so the
    chat and embeddings providers are named here. Anthropic has no embeddings
    API: Tier 2 embeds through OpenAI (or NVIDIA Build) when that key is present.
    A value already in the environment wins. The sandbox size travels the same way:
    ``validate`` has no ``--override-cpus``, so the harbor_agent_sandbox environment reads
    ``HARBOR_SANDBOX_CPUS`` and ``HARBOR_SANDBOX_MEMORY_MB`` (patch hunk 4 lets them through).
    With a warm pool (``SANDBOX_WARM_POOL``, passed on as ``HARBOR_SANDBOX_WARM_POOL``) the sandbox
    profile sizes the pods and the size variables are not set. ``SKILLEVAL_NAME`` (set by the
    controller) goes through as it is; the environment labels the trial pods with it.

    The judge is the Job's ``SKILL_EVAL_LLM_PROVIDER`` (SkillCIConfig ``spec.judge``), with its
    optional ``SKILL_EVAL_LLM_MODEL`` and ``SKILL_EVAL_LLM_BASE_URL``; a blank one is dropped,
    because SkillEvaluator refuses an empty model. SkillEvaluator reads the anthropic, nv_build
    and bedrock keys only under their own names, so ``SKILL_EVAL_LLM_API_KEY`` is copied to
    ``ANTHROPIC_API_KEY``, ``NVIDIA_API_KEY`` or ``AWS_BEARER_TOKEN_BEDROCK`` when that is unset.
    Without ``SKILL_EVAL_LLM_PROVIDER`` the judge is ``anthropic`` whenever the Job has an
    Anthropic key, else ``openai`` with an OpenAI key (``judge_provider``). Codex needs
    ``OPENAI_API_KEY`` together
    with ``OPENAI_BASE_URL`` (the OpenAI endpoint unless the Job says otherwise). OpenCode behind
    a gateway (``agent_route``) gets the gateway key as ``OPENAI_API_KEY``, so Tier 2 embeddings
    keep the real OpenAI key through ``SKILL_EVAL_EMBEDDING_*``. The requested agent release
    reaches SkillEvaluator as ``SKILLEVALUATOR_AGENT_VERSION`` (patch hunk 7), and the sandbox
    image as ``HARBOR_SANDBOX_IMAGE`` (patch hunk 4).
    """
    env = dict(environ)
    route = (
        agent_route(env.get("AGENT", ""), env.get("MODEL", ""), env) if env.get("AGENT") else None
    )
    if not env.get("SKILL_EVAL_EMBEDDING_PROVIDER"):
        # Chosen from the Job's own keys, before a gateway key takes the OPENAI_API_KEY name.
        if env.get("OPENAI_API_KEY"):
            env["SKILL_EVAL_EMBEDDING_PROVIDER"] = "openai"
            if route and route.gateway:
                env["SKILL_EVAL_EMBEDDING_PROVIDER"] = "openai-compatible"
                env["SKILL_EVAL_EMBEDDING_API_KEY"] = env["OPENAI_API_KEY"]
                env["SKILL_EVAL_EMBEDDING_BASE_URL"] = (
                    env.get("OPENAI_BASE_URL") or OPENAI_DEFAULT_BASE_URL
                )
                env["SKILL_EVAL_EMBEDDING_MODEL"] = OPENAI_EMBEDDING_MODEL
        elif env.get("NVIDIA_API_KEY"):
            env["SKILL_EVAL_EMBEDDING_PROVIDER"] = "nv_build"
    if route and route.gateway:
        env.update(route.env)
    elif env.get("OPENAI_API_KEY") and not env.get("OPENAI_BASE_URL"):
        env["OPENAI_BASE_URL"] = OPENAI_DEFAULT_BASE_URL
    if version := agent_version(env):
        env["SKILLEVALUATOR_AGENT_VERSION"] = version
    # The generic sandbox image is what the environment runs, unless the Job brings a registry
    # to build task images in. HARBOR_SANDBOX_IMAGE is the variable the environment reads.
    # A warm pool runs its profile's image, so building task images rules it out too. The Job's
    # SANDBOX_WARM_POOL decides, not a HARBOR_SANDBOX_WARM_POOL left in the environment.
    warm_pool = (env.get("SANDBOX_WARM_POOL") or "").strip()
    if builds_task_images(env):
        env.pop("HARBOR_SANDBOX_IMAGE", None)
        warm_pool = ""
    else:
        env["HARBOR_SANDBOX_IMAGE"] = env.get("SANDBOX_IMAGE") or DEFAULTS["SANDBOX_IMAGE"]
    if warm_pool:
        env["HARBOR_SANDBOX_WARM_POOL"] = warm_pool
    else:
        env.pop("HARBOR_SANDBOX_WARM_POOL", None)
        for name, source in (
            ("HARBOR_SANDBOX_CPUS", "SANDBOX_CPUS"),
            ("HARBOR_SANDBOX_MEMORY_MB", "SANDBOX_MEMORY_MB"),
        ):
            if not env.get(name):
                env[name] = env.get(source) or DEFAULTS[source]
    for name in ("SKILL_EVAL_LLM_PROVIDER", "SKILL_EVAL_LLM_MODEL", "SKILL_EVAL_LLM_BASE_URL"):
        if name in env and not env[name].strip():
            del env[name]
    if JUDGE_KEY_ENV in env and not env[JUDGE_KEY_ENV].strip():
        del env[JUDGE_KEY_ENV]
    # Decided on the Job's own keys, before a gateway route takes the OPENAI_API_KEY name.
    if provider := judge_provider(environ):
        env["SKILL_EVAL_LLM_PROVIDER"] = provider
        native = JUDGE_PROVIDERS.get(provider, "")
        own_name = native not in ("", "OPENAI_API_KEY", JUDGE_KEY_ENV)
        if own_name and not env.get(native) and (key := env.get(JUDGE_KEY_ENV)):
            env[native] = key
    return env


def trial_retry_env(
    env: Mapping[str, str], config: Mapping[str, str], work: Path
) -> dict[str, str]:
    """Return ``env`` for the Tier 3 run, with Harbor's trial retries for provider rate limits.

    With ``TRIAL_RETRIES`` above 0 the patched SkillEvaluator (hunk 13) passes Harbor
    ``--max-retries TRIAL_RETRIES --retry-include ApiRateLimitError`` and ``-c`` with a JobConfig
    file that holds only the backoff (``TRIAL_RETRY_BACKOFF``), written to ``work``. Harbor runs
    the failed trial again under the same name and keeps only the last result, so the scores
    count one result per trial and the controller's trial budget does not count the retries.
    Harbor classifies a failure as ``ApiRateLimitError`` when the agent's output mentions "rate
    limit" or "too many requests", and that output includes the task text: a failed trial of a
    task about rate limits can be retried too, at most ``TRIAL_RETRIES`` times.
    ``TRIAL_RETRIES=0`` turns it off and drops the same variables when the Job's environment has
    them.
    """
    out = dict(env)
    out.pop("SKILLEVALUATOR_TRIAL_RETRIES", None)
    out.pop("SKILLEVALUATOR_HARBOR_CONFIG", None)
    if int(config["TRIAL_RETRIES"]) == 0:
        return out
    path = work / TRIAL_RETRY_CONFIG
    path.write_text(json.dumps({"retry": TRIAL_RETRY_BACKOFF}, indent=2) + "\n")
    out["SKILLEVALUATOR_TRIAL_RETRIES"] = config["TRIAL_RETRIES"]
    out["SKILLEVALUATOR_HARBOR_CONFIG"] = str(path.absolute())
    return out


def validate_command(
    config: Mapping[str, str],
    skill_dir: Path,
    out: Path,
    *,
    tiers: str,
    catalog: Path | None = None,
    results: Path | None = None,
) -> list[str]:
    """Build a ``skillevaluator validate`` command line.

    ``tiers`` is ``1,2`` for the gate (static, security, quality, lint and deduplication; no
    LLM calls in Tier 1, no sandboxes) and ``1,2,3`` for the full run that adds the live Tier 3
    evaluation, with ``results`` as the Harbor results directory. ``--no-autopilot`` keeps the
    skill's own ``evals/`` and never generates a dataset, exactly as ``tier3 evaluate`` did.
    ``catalog`` makes Tier 2 compare the skill with the other skills in Harbor
    (patch hunks 1 to 3). ``--continue-on-failure`` keeps the reports complete.
    """
    cmd = [
        "skillevaluator", "validate", str(skill_dir),
        "--tiers", tiers,
        "--no-llm",
        "--profile", config["TIER1_PROFILE"],
        "--continue-on-failure",
        "-r", "json,html",
        "-o", str(out),
    ]  # fmt: skip
    if catalog is not None:
        cmd += ["--catalog", str(catalog)]
    if "3" in tiers.split(","):
        if results is None:
            raise ValueError("a run with Tier 3 needs a results directory")
        cmd += [
            "--no-autopilot",
            "--agents", config["AGENT"],
            "--env-mode", "ack",
            "--ek", f"namespace={config['SANDBOX_NAMESPACE']}",
            "--agent-model", f"{config['AGENT']}={config['AGENT_MODEL']}",
            "--n-attempts", config["ATTEMPTS"],
            "--n-concurrent", config["TRIAL_CONCURRENCY"],
            "--timeout-multiplier", config["TIMEOUT_MULTIPLIER"],
            "--results-dir", str(results),
        ]  # fmt: skip
        cmd += image_flags(config)
    return cmd


def image_flags(config: Mapping[str, str]) -> list[str]:
    """Return the ``--ek`` flags that say where the sandbox gets its image.

    The generic sandbox image itself travels in ``HARBOR_SANDBOX_IMAGE`` (see ``tool_env``);
    only its pull secrets are a keyword argument. With ``TASK_IMAGE_REGISTRY`` Harbor builds and
    pushes a task image instead, and the registry, its pull secret and the existence check
    (``skip_image_check=false``: build only when the image is missing) are needed.
    """
    if builds_task_images(config):
        flags = ["--ek", f"registry={config['TASK_IMAGE_REGISTRY']}"]
        if config["TASK_IMAGE_PULL_SECRET"]:
            flags += ["--ek", f"image_pull_secret={config['TASK_IMAGE_PULL_SECRET']}"]
        return [*flags, "--ek", "skip_image_check=false"]
    if config["SANDBOX_PULL_SECRETS"]:
        return ["--ek", f"image_pull_secret={config['SANDBOX_PULL_SECRETS']}"]
    return []


def catalog_build_command(catalog_dir: Path, catalog: Path, out: Path) -> list[str]:
    """Build the command that embeds the other skills into a Tier 2 catalog file."""
    return [
        "skillevaluator", "similarity-check", str(catalog_dir),
        "--type", "skill",
        "--save-catalog", str(catalog),
        "-r", "json",
        "-o", str(out),
    ]  # fmt: skip


def semver_key(tag: str) -> tuple[int, int, int] | None:
    """Return the sort key of a ``vX.Y.Z`` tag, or ``None`` for any other tag."""
    found = SEMVER_TAG.match(tag)
    return (int(found[1]), int(found[2]), int(found[3])) if found else None


def pick_tag(tags: Iterable[str]) -> str | None:
    """Choose the version of a skill to compare against: ``stable``, else the newest ``vX.Y.Z``."""
    tags = list(tags)
    if "stable" in tags:
        return "stable"
    versions = [(key, tag) for tag in tags if (key := semver_key(tag))]
    return max(versions)[1] if versions else None


def oras_lines(cmd: list[str]) -> list[str] | None:
    """Run an oras listing command and return its output lines, or ``None`` on failure."""
    proc = run_oras(cmd)
    if proc.returncode != 0:
        print(f"skill-eval: {' '.join(cmd[:3])} failed: {proc.stderr.strip()}", file=sys.stderr)
        return None
    return [line.strip() for line in proc.stdout.splitlines() if line.strip()]


def catalog_repositories(config: Mapping[str, str]) -> list[str]:
    """List the other skill repositories in the Harbor project of ``SKILL_REF``.

    Returns an empty list when the registry cannot list repositories: Tier 2 then
    compares the skill only with itself and the log says so.
    """
    host, project, *_ = [*config["SKILL_REF"].split("/"), "", ""]
    lines = oras_lines(["oras", "repo", "ls", f"{host}/{project}", *registry_flags(config)])
    if lines is None:
        return []
    # Harbor lists the repositories of the namespace relative to it ("acme-release-notes");
    # other registries may return "skills/acme-release-notes". Accept both.
    names = {
        line.removeprefix(f"{project}/")
        for line in lines
        if "/" not in line.removeprefix(f"{project}/")
    }
    names.discard(config["SKILL_NAME"])
    return sorted(names)[:MAX_CATALOG_SKILLS]


def pull_catalog(config: Mapping[str, str], dest: Path) -> list[str]:
    """Pull every other skill (``stable``, else newest ``vX.Y.Z``) into ``dest``.

    Returns the names of the skills that are now in ``dest``; skills that cannot
    be listed or pulled are skipped with a log line.
    """
    host, project, *_ = [*config["SKILL_REF"].split("/"), "", ""]
    pulled = []
    for name in catalog_repositories(config):
        repo = f"{host}/{project}/{name}"
        tags = oras_lines(["oras", "repo", "tags", repo, *registry_flags(config)])
        if not tags or not (tag := pick_tag(tags)):
            print(f"skill-eval: catalog: no stable or vX.Y.Z tag for {name}", file=sys.stderr)
            continue
        cmd = ["oras", "pull", f"{repo}:{tag}", "--output", str(dest), *registry_flags(config)]
        proc = run_oras(cmd)
        if proc.returncode != 0 or not (dest / name / "SKILL.md").is_file():
            print(f"skill-eval: catalog: cannot pull {name}:{tag}", file=sys.stderr)
            continue
        pulled.append(name)
    return pulled


def run_static(cmd: list[str], env: Mapping[str, str]) -> subprocess.CompletedProcess | None:
    """Run a Tier 1 or Tier 2 command, echo its output to the log, return ``None`` on timeout."""
    try:
        proc = subprocess.run(
            cmd,
            env=dict(env),
            capture_output=True,
            text=True,
            check=False,
            timeout=STATIC_TIMEOUT_SEC,
        )
    except subprocess.TimeoutExpired:
        print(f"skill-eval: {cmd[1]} timed out after {STATIC_TIMEOUT_SEC}s", file=sys.stderr)
        return None
    print(redact(proc.stdout), flush=True)
    print(redact(proc.stderr), file=sys.stderr, flush=True)
    return proc


def newest(directory: Path, pattern: str) -> Path | None:
    """Return the newest file matching ``pattern`` under ``directory``, if any."""
    found = sorted(directory.glob(pattern), key=lambda p: p.stat().st_mtime)
    return found[-1] if found else None


def finding_text(tier: str, check: str, message: str) -> str:
    """Format one finding as ``tier1/security: message`` within the length limit."""
    message = re.sub(r"^\[[A-Z0-9_ -]+\]\s*", "", " ".join(message.split()))
    text = f"{tier}/{check}: {message}"
    return text if len(text) <= MAX_FINDING_CHARS else text[: MAX_FINDING_CHARS - 3] + "..."


def finding_message(finding: Mapping[str, Any]) -> str:
    """Format a report finding as ``rule: message (file:line)``."""
    where = finding.get("file_path")
    if where and finding.get("line_number"):
        where = f"{where}:{finding['line_number']}"
    text = ": ".join(
        str(part) for part in (finding.get("check_name"), finding.get("message")) if part
    )
    return f"{text} ({where})" if where else text


def result_tier(result: Mapping[str, Any]) -> int | None:
    """Return the tier (1, 2 or 3) a ``results[]`` entry belongs to, if the report says."""
    tier = (result.get("gating") or {}).get("tier")
    if tier is None:
        tier = (result.get("publication_evidence") or {}).get("tier")
    return tier if isinstance(tier, int) else None


def report_findings(report: Mapping[str, Any], tier: str) -> list[str]:
    """Extract short findings for ``tier`` (``tier1`` or ``tier2``) from a SkillEvaluator report.

    Reads ``results[]``: every validator of that tier whose ``status`` is not ``passed``
    yields its high and critical ``findings[]`` (rule name, message, file and line) or, when
    it has none, its blocking ``legacy.errors``. ``publication_evidence.check_id`` names the
    check (``security``, ``pii``, ``similarity`` ...). A skipped validator names its skip
    reason instead, because a skipped required check leaves the tier incomplete.
    """
    out: list[str] = []
    for result in report.get("results", []):
        if f"tier{result_tier(result)}" != tier:
            continue
        skipped = result.get("status") == "skipped" or result.get("skipped")
        if result.get("status") == "passed":
            continue
        evidence = result.get("publication_evidence") or {}
        check = evidence.get("check_id") or str(result.get("validator", "check")).lower()
        if skipped:
            reason = str(result.get("skip_reason") or "skipped")
            out.append(finding_text(tier, check, reason.splitlines()[0]))
            continue
        serious = [
            f
            for f in result.get("findings", [])
            if SEVERITY_RANK.get(str(f.get("severity")).lower(), 9) <= SEVERITY_RANK["high"]
        ]
        serious.sort(key=lambda f: SEVERITY_RANK.get(str(f.get("severity")).lower(), 9))
        messages = [finding_message(f) for f in serious] or list(
            result.get("legacy", {}).get("errors", [])
        )
        if not messages:
            scans = ", ".join(result.get("incomplete_scans", []))
            messages = [f"{result.get('status', 'not passed')}{f' ({scans})' if scans else ''}"]
        out += [finding_text(tier, check, m) for m in messages]
    return out


def tier_status(report: Mapping[str, Any], tier: int) -> str:
    """Return ``passed``, ``failed`` or ``incomplete`` for one tier of a ``validate`` report.

    Failed beats incomplete. A tier with no results, or whose results were all skipped
    (Tier 2 without an embeddings provider), is incomplete: SkillEvaluator requires it.
    """
    results = [r for r in report.get("results", []) if result_tier(r) == tier]
    statuses = {r.get("status") for r in results}
    if "failed" in statuses:
        return "failed"
    if (
        not results
        or "incomplete" in statuses
        or any(r.get("incomplete_scans") for r in results)
        or statuses <= {"skipped"}
    ):
        return "incomplete"
    return "passed"


def publication_of(report: Mapping[str, Any]) -> str:
    """Map SkillEvaluator's publication assessment to ``passed``, ``failed`` or ``incomplete``.

    The report's ``publication.status`` is ``pass``, ``fail``, ``incomplete`` or ``neutral``
    (Tier 3 ran, but the skill does not clearly beat the baseline). ``neutral`` maps to
    ``passed``: the evidence is complete and nothing failed; the lift is the controller's
    business (verdict ``Neutral``). Anything unrecognised is ``incomplete``.
    """
    publication = report.get("publication")
    status = publication.get("status") if isinstance(publication, dict) else None
    status = status or report.get("publication_status")
    return {"pass": "passed", "neutral": "passed", "fail": "failed"}.get(str(status), "incomplete")


def read_validate(out: Path, proc: subprocess.CompletedProcess | None) -> ValidateReport:
    """Turn the report a ``validate`` run wrote under ``out`` into a :class:`ValidateReport`."""
    json_path = newest(out, "skillevaluator-output-*.json")
    html_path = newest(out, "skillevaluator-output-*.html")
    benchmark = out / "BENCHMARK.md"
    benchmark_path = benchmark if benchmark.is_file() else None
    incomplete = {"tier1": "incomplete", "tier2": "incomplete"}
    if json_path is None:
        reason = "timed out" if proc is None else (proc.stderr or proc.stdout).strip()
        last = reason.splitlines()[-1] if reason else f"exited {proc.returncode} without a report"
        finding = finding_text("tier1", "run", last)
        return ValidateReport(incomplete, [finding], "incomplete", None, html_path, benchmark_path)
    try:
        report = json.loads(json_path.read_text())
    except (OSError, ValueError) as err:
        finding = finding_text("tier1", "run", f"unreadable report: {err}")
        return ValidateReport(incomplete, [finding], "incomplete", None, html_path, benchmark_path)
    tiers = {f"tier{n}": tier_status(report, n) for n in (1, 2)}
    findings = [
        finding
        for tier, status in tiers.items()
        if status != "passed"
        for finding in (report_findings(report, tier) or [finding_text(tier, "run", status)])
    ]
    return ValidateReport(
        tiers, findings, publication_of(report), json_path, html_path, benchmark_path
    )


def prepare_catalog(
    config: Mapping[str, str], work: Path, env: Mapping[str, str]
) -> tuple[bool, Path | None, list[str]]:
    """Prepare the Tier 2 comparison catalog and say whether Tier 2 can run.

    Returns ``(tier2, catalog, findings)``. ``tier2`` is false when Tier 2 cannot produce
    trustworthy evidence (no embeddings provider, or a catalog that cannot be built); the
    runs then leave Tier 2 out, SkillEvaluator calls the publication incomplete, and the
    finding says why. ``catalog`` is ``None`` when there are no other skills to compare with.
    """
    if not env.get("SKILL_EVAL_EMBEDDING_PROVIDER"):
        reason = "no embeddings provider: set OPENAI_API_KEY (or NVIDIA_API_KEY) in llm-keys"
        return False, None, [finding_text("tier2", "embeddings", reason)]
    catalog_dir = work / "catalog" / "skills"
    if not pull_catalog(config, catalog_dir):
        print(
            "skill-eval: no other skills to compare with; Tier 2 checks the skill alone", flush=True
        )
        return True, None, []
    catalog_file = work / "catalog.json"
    proc = run_static(
        catalog_build_command(catalog_dir, catalog_file, work / "catalog-report"), env
    )
    if not catalog_file.is_file():
        detail = "timed out" if proc is None else (proc.stderr or proc.stdout).strip()
        last = detail.splitlines()[-1] if detail else f"exited {proc.returncode}"
        return False, None, [finding_text("tier2", "catalog", last)]
    return True, catalog_file, []


def newest_result(results: Path) -> Path:
    """Return the newest ``result.json`` under ``results``."""
    candidates = sorted(results.glob("*/*/result.json"), key=lambda p: p.stat().st_mtime)
    if not candidates:
        raise EvalError("SkillEvaluator wrote no result.json")
    return candidates[-1]


def trial_scores(entry: Mapping[str, Any], arm: str) -> list[float]:
    """Return the per-trial overall scores of one arm (``with_skill`` or ``without_skill``).

    SkillEvaluator lists every attempt of every case under
    ``pass_at_k.<arm>.cases.<case>.attempts[].score``; the arm's overall score is their mean.
    Scores are rounded to two decimals and capped at ``MAX_SAMPLES``. An older result without
    that block gives an empty list, and the controller then uses its fixed-number rules.
    """
    cases = ((entry.get("pass_at_k") or {}).get(arm) or {}).get("cases") or {}
    scores = [
        round(float(attempt["score"]), 2)
        for case in cases.values()
        for attempt in case.get("attempts") or []
        if isinstance(attempt.get("score"), int | float)
    ]
    return scores[:MAX_SAMPLES]


def read_scores(results: Path, agent: str) -> dict[str, Any]:
    """Extract the scores for ``agent`` from the newest ``result.json`` under ``results``."""
    path = newest_result(results)
    try:
        result = json.loads(path.read_text())
    except (OSError, ValueError) as err:
        raise EvalError(f"cannot read {path.name}: {err}") from err
    entry = result.get("agents", {}).get(agent)
    if entry is None:
        raise EvalError(f"result.json has no entry for agent {agent!r}")
    with_skill = entry.get("overall_with_skill")
    without_skill = entry.get("overall_without_skill")
    if not all(isinstance(v, int | float) for v in (with_skill, without_skill)):
        errors = "; ".join(map(str, result.get("execution_errors", []))) or result.get("error")
        raise EvalError(f"no scores in result.json: {errors or 'unknown reason'}")
    with_skill, without_skill = round(with_skill, 2), round(without_skill, 2)
    payload: dict[str, Any] = {
        "withSkill": with_skill,
        "withoutSkill": without_skill,
        "lift": round(with_skill - without_skill, 2),
        "agent": agent,
        "model": entry.get("model", ""),
    }
    samples = {"withSkill": trial_scores(entry, "with_skill")}
    samples["withoutSkill"] = trial_scores(entry, "without_skill")
    if all(samples.values()):
        payload["samples"] = samples
    return payload


def used_agent_version(run_dir: Path) -> str | None:
    """Return the agent CLI release the trials ran, from Harbor's trial results.

    Every trial's ``result.json`` under ``_harbor-jobs/<job>/<trial>/`` carries
    ``agent_info.version``: the release Harbor installed (the requested pin), or the one it
    detected afterwards when no pin was given. The most common valid value wins; ``None`` when
    the jobs were not kept or no trial reports a version.
    """
    seen = Counter()
    for path in sorted(run_dir.glob("_harbor-jobs/*/*/result.json")):
        try:
            info = json.loads(path.read_text()).get("agent_info") or {}
        except (OSError, ValueError, AttributeError):
            continue
        version = str(info.get("version") or "").strip()
        if version != "unknown" and AGENT_VERSION_PATTERN.match(version):  # Harbor's "no idea"
            seen[version] += 1
    return seen.most_common(1)[0][0] if seen else None


def tier3_tier(results: Path) -> TierResult:
    """Return the Tier 3 status: ``passed`` when the run succeeded, else ``incomplete``.

    ``result.json`` carries ``execution_status`` (``succeeded`` when every trial
    ran). Scores that are low are still a valid result; the controller reads them
    as a verdict, not as a failed tier.
    """
    result = json.loads(newest_result(results).read_text())
    if result.get("execution_status", "succeeded") == "succeeded":
        return TierResult("passed")
    errors = "; ".join(map(str, result.get("execution_errors", []))) or result["execution_status"]
    return TierResult("incomplete", [finding_text("tier3", "execution", errors)])


def collect_report(directory: Path, report: ValidateReport, run_dir: Path | None) -> list[str]:
    """Copy the reports of the last ``validate`` run into ``directory``; return the file names.

    ``report.html`` and ``BENCHMARK.md`` are the combined SkillEvaluator report (with its
    publication assessment) of that run. With Tier 3, ``result.json`` is the Tier 3 result the
    controller reads its dimension scores from, and ``validate.json`` and ``tier3.html`` are
    the combined JSON and the Tier 3 report; without it ``result.json`` is the combined JSON,
    so a Rejected run shows the failing checks.
    """
    directory.mkdir(parents=True, exist_ok=True)
    sources: dict[str, Path | None] = {
        "report.html": report.html_path,
        "result.json": report.json_path,
        "BENCHMARK.md": report.benchmark_path,
    }
    if run_dir is not None:
        sources |= {
            "report.html": report.html_path or run_dir / "report.html",
            "result.json": run_dir / "result.json",
            "validate.json": report.json_path,
            "tier3.html": run_dir / "report.html",
        }
    for name, src in sources.items():
        if src is not None and src.is_file():
            shutil.copy(src, directory / name)
    return [name for name in REPORT_FILES if (directory / name).is_file()]


def attach(
    config: Mapping[str, str],
    directory: Path,
    artifact_type: str,
    annotations: Mapping[str, str],
    files: Iterable[str],
    what: str,
) -> str | None:
    """Attach ``files`` (``<name>:<media type>``) in ``directory`` to the evaluated skill digest.

    SKILL_REF is already pinned to a digest, so the artifact becomes an OCI referrer of exactly
    the version that was checked. Returns its digest, or ``None`` when attaching fails: a
    missing ``what`` (report, trial bundle) must not fail the eval.
    """
    files = list(files)
    if not files:
        return None
    cmd = ["oras", "attach", "--artifact-type", artifact_type, *registry_flags(config)]
    for key, value in annotations.items():
        cmd += ["--annotation", f"{key}={value}"]
    cmd += [config["SKILL_REF"], *files]
    try:
        # oras rejects absolute file paths, so run it inside the directory.
        proc = run_oras(cmd, cwd=directory)
    except OSError as err:
        print(f"skill-eval: {what} not attached: {err}", file=sys.stderr)
        return None
    found = re.search(r"Digest:\s*(sha256:[0-9a-f]{64})", proc.stdout + proc.stderr)
    if proc.returncode != 0 or not found:
        detail = redact((proc.stderr or proc.stdout).strip())
        print(f"skill-eval: {what} not attached ({proc.returncode}): {detail}", file=sys.stderr)
        return None
    return found.group(1)


def attach_report(
    config: Mapping[str, str], directory: Path, files: list[str], annotations: Mapping[str, str]
) -> str | None:
    """Attach the report files in ``directory`` as the report referrer; return its digest."""
    typed = [f"{name}:{MEDIA_TYPES[Path(name).suffix]}" for name in files]
    return attach(config, directory, REPORT_TYPE, annotations, typed, "report")


def optional_module(name: str) -> Any | None:
    """Import ``name`` (``trial_bundle``, ``flows``), or return ``None`` and log why it failed.

    The bundle and the flows never fail a run, not even when their module is broken.
    """
    try:
        return importlib.import_module(name)
    except Exception as err:
        print(f"skill-eval: module {name} unavailable: {redact(str(err))}", file=sys.stderr)
        return None


def bundle_enabled(environ: Mapping[str, str]) -> bool:
    """Return whether ``TRIAL_BUNDLE`` allows a trial bundle (``trial_bundle.enabled``).

    A module that cannot answer counts as enabled: the build then fails and the report says so
    (``build-failed``).
    """
    module = optional_module(BUNDLE_MODULE)
    try:
        return module is None or bool(module.enabled(environ))
    except Exception as err:
        print(f"skill-eval: TRIAL_BUNDLE not readable: {redact(str(err))}", file=sys.stderr)
        return True


def unavailable(reason: str) -> dict[str, str]:
    """Return the index's run-level network state ``unavailable`` with ``reason``."""
    return {"state": "unavailable", "reason": redact(reason)[:300]}


def parse_time(value: Any) -> datetime | None:
    """Parse an ISO 8601 time from Harbor's ``result.json`` (UTC when it has no zone)."""
    if not isinstance(value, str) or not value:
        return None
    try:
        parsed = datetime.fromisoformat(value)
    except ValueError:
        return None
    return parsed if parsed.tzinfo else parsed.replace(tzinfo=UTC)


def trial_windows(run_dir: Path, agent: str, window: Callable[..., Any]) -> dict[str, Any]:
    """Return ``window(start, end)`` of every trial under ``run_dir``, by trial id.

    From ``<agent>/<arm>/trials/<trial id>/result.json`` (``started_at``, ``finished_at``); a
    trial that has not finished ends now, an unreadable one is left out.
    """
    windows: dict[str, Any] = {}
    for path in sorted(run_dir.glob(f"{agent}/*/trials/*/result.json")):
        try:
            result = json.loads(path.read_text())
        except (OSError, ValueError):
            continue
        if not isinstance(result, dict) or not (start := parse_time(result.get("started_at"))):
            continue
        end = parse_time(result.get("finished_at")) or datetime.now(UTC)
        windows[path.parent.name] = window(start, end)
    return windows


class FlowWatch:
    """The network flows of one Tier 3 run (``flows.py``). Nothing here ever raises.

    ``start`` runs ``flows.start_from_env`` right before the Tier 3 ``validate``, ``stop``
    stops the collector after it, and ``summarize`` maps the flows to the trials. Without
    ``HUBBLE_SERVER`` the state is ``off``; a collector that fails is ``unavailable``.
    """

    def __init__(self, environ: Mapping[str, str], eval_name: str, work: Path) -> None:
        """Prepare the watch for the trial pods of ``eval_name``; nothing starts yet."""
        self.environ = environ
        self.eval_name = eval_name
        self.work = work
        self.collector: Any = None
        self.collection: Any = None
        self.network: dict[str, str] = dict(NETWORK_OFF)

    def start(self) -> None:
        """Start collecting when the Job has ``HUBBLE_SERVER``; record why when it cannot."""
        if not (self.environ.get("HUBBLE_SERVER") or "").strip():
            return
        if not self.eval_name:
            self.network = unavailable("SKILLEVAL_NAME is not set, so no trial pod label to watch")
            return
        if (module := optional_module(FLOWS_MODULE)) is None:
            self.network = unavailable("the flows module cannot be imported")
            return
        try:
            self.collector = module.start_from_env(self.environ, self.eval_name, self.work)
        except Exception as err:
            self.network = unavailable(f"flow collector did not start: {err}")
            print(f"skill-eval: {self.network['reason']}", file=sys.stderr)

    def stop(self) -> None:
        """Stop the collector, if one runs, and keep what it collected."""
        collector, self.collector = self.collector, None
        if collector is None:
            return
        try:
            self.collection = collector.stop()
        except Exception as err:
            self.network = unavailable(f"flow collector did not stop cleanly: {err}")
            print(f"skill-eval: {self.network['reason']}", file=sys.stderr)

    def summarize(
        self, run_dir: Path, agent: str, max_rows: int
    ) -> tuple[dict[str, Any], dict[str, Any] | None]:
        """Return the index's run-level ``network`` and the per-trial sections (or ``None``)."""
        if self.collection is None:
            return dict(self.network), None
        module = optional_module(FLOWS_MODULE)
        try:
            windows = trial_windows(run_dir, agent, module.Window)
            network, trials = module.summarize(self.collection, windows, max_rows=max_rows)
        except Exception as err:
            print(f"skill-eval: flows not summarized: {redact(str(err))}", file=sys.stderr)
            return unavailable(f"flows could not be summarized: {err}"), None
        return dict(network), dict(trials)


def find_run_dir(results: Path) -> Path | None:
    """Return SkillEvaluator's run directory under ``results``, or ``None`` when there is none.

    The parent of the newest ``result.json``; a run that died before writing one still leaves
    its newest ``<skill>/<run id>/`` directory, which may hold trials.
    """
    try:
        return newest_result(results).parent
    except EvalError:
        pass
    runs = sorted((p for p in results.glob("*/*") if p.is_dir()), key=lambda p: p.stat().st_mtime)
    return runs[-1] if runs else None


def credentials(
    config: Mapping[str, str], environ: Mapping[str, str]
) -> list[tuple[str, str, str]]:
    """Return the provider routes of the run as ``(role, env, host)``: the agent's, the judge's.

    Names only, never values (the index's ``credentials``). The agent: Claude Code and OpenCode
    on ``anthropic/...`` use ``ANTHROPIC_API_KEY`` (under the anthropic judge SkillEvaluator
    hands them the judge's endpoint), Codex ``OPENAI_API_KEY``, an OpenCode gateway route its
    provider's key and endpoint. The judge: ``judge_of``.
    """
    agent, model = config["AGENT"], config["MODEL"]
    judge = judge_of(environ)
    route = agent_route(agent, model, environ)
    if route.gateway:
        key_env = GATEWAY_PROVIDERS[model.partition("/")[0]][0]
        agent_cred = (key_env, host_of(route.env["OPENAI_BASE_URL"]))
    elif agent == "codex":
        base_url = environ.get("OPENAI_BASE_URL") or OPENAI_DEFAULT_BASE_URL
        agent_cred = ("OPENAI_API_KEY", host_of(base_url))
    elif judge is not None and judge.provider == "anthropic":
        agent_cred = ("ANTHROPIC_API_KEY", judge.host)
    else:
        base_url = environ.get("ANTHROPIC_BASE_URL") or ANTHROPIC_DEFAULT_BASE_URL
        agent_cred = ("ANTHROPIC_API_KEY", host_of(base_url))
    routes = [("agent", *agent_cred)]
    if judge is not None and judge.key_env and judge.host:
        routes.append(("judge", judge.key_env, judge.host))
    return [route for route in routes if route[2]]


def bundle_config(config: Mapping[str, str]) -> dict[str, str]:
    """Return the runner settings the trial bundle index records."""
    names = ("SKILL_NAME", "SKILL_REF", "AGENT", "MODEL", "ATTEMPTS", "SKILLEVAL_NAME")
    settings = {name: config[name] for name in names}
    if config.get("SKILL_VERSION"):
        settings["SKILL_VERSION"] = config["SKILL_VERSION"]
    return settings


def attach_trials(
    config: Mapping[str, str],
    environ: Mapping[str, str],
    work: Path,
    run_dir: Path | None,
    agent_version: str,
    flows: FlowWatch,
) -> tuple[str, str]:
    """Build and attach the trial bundle; return the report annotation that pairs with it.

    ``(skillci.dev/trials, <digest>)`` when the bundle is attached, else
    ``(skillci.dev/trials-withheld, <reason>)``: ``disabled``, ``no-trials``, a reason of
    ``trial_bundle.NotBuilt``, ``build-failed`` or ``attach-failed``. Never raises.
    """
    if not bundle_enabled(environ):
        return WITHHELD_ANNOTATION, WITHHELD_DISABLED
    if run_dir is None:
        return WITHHELD_ANNOTATION, WITHHELD_NO_TRIALS
    if (module := optional_module(BUNDLE_MODULE)) is None:
        return WITHHELD_ANNOTATION, WITHHELD_BUILD_FAILED
    try:
        try:
            max_rows = module.limits_from_env(environ).flow_rows
        except Exception as err:
            print(f"skill-eval: bundle limits not readable: {redact(str(err))}", file=sys.stderr)
            max_rows = DEFAULT_FLOW_ROWS
        network, trial_networks = flows.summarize(run_dir, config["AGENT"], max_rows)
        routes = [module.Credential(*route) for route in credentials(config, environ)]
        built = module.build(
            run_dir,
            work / "trials",
            environ=environ,
            config=bundle_config(config),
            agent_version=agent_version,
            credentials=routes,
            network=network,
            trial_networks=trial_networks,
            auth_config=Path(config["REGISTRY_CONFIG"]),
        )
        if isinstance(built, module.NotBuilt):
            detail = f": {built.detail}" if built.detail else ""
            print(f"skill-eval: no trial bundle ({built.reason}){redact(detail)}", file=sys.stderr)
            return WITHHELD_ANNOTATION, built.reason
        files = module.oras_files(built)
        annotations = {
            "skillci.dev/eval": config["SKILLEVAL_NAME"],
            "skillci.dev/agent": config["AGENT"],
            "skillci.dev/agent-version": agent_version,
            "skillci.dev/model": config["MODEL"],
            "skillci.dev/trials-schema": str(module.SCHEMA_MAJOR),
            "skillci.dev/redaction": str(module.REDACTION_VERSION),
            **built.annotations,
        }
        annotations = {key: value for key, value in annotations.items() if value}
        artifact_type = getattr(module, "ARTIFACT_TYPE", BUNDLE_TYPE)
    except Exception as err:
        print(f"skill-eval: trial bundle not built: {redact(str(err))}", file=sys.stderr)
        return WITHHELD_ANNOTATION, WITHHELD_BUILD_FAILED
    digest = attach(config, built.directory, artifact_type, annotations, files, "trial bundle")
    if digest is None:
        return WITHHELD_ANNOTATION, WITHHELD_ATTACH_FAILED
    return TRIALS_ANNOTATION, digest


def redact_report(
    config: Mapping[str, str], environ: Mapping[str, str], directory: Path, files: list[str]
) -> list[str]:
    """Run the bundle's redaction and fail-closed scan over the report files.

    Returns the files that may be attached (``trial_bundle.redact_report_files``). Fails
    closed: when the redaction cannot run, no report file is attached.
    """
    if not files:
        return []
    if (module := optional_module(BUNDLE_MODULE)) is None:
        print("skill-eval: report not attached: redaction unavailable", file=sys.stderr)
        return []
    try:
        auth_config = Path(config["REGISTRY_CONFIG"])
        redactor = module.Redactor(module.secret_values(environ, auth_config))
        allowed = set(module.redact_report_files(directory, files, redactor))
    except Exception as err:
        detail = redact(str(err))
        print(f"skill-eval: report not attached: redaction failed: {detail}", file=sys.stderr)
        return []
    return [name for name in files if name in allowed]


def publish(
    config: Mapping[str, str],
    environ: Mapping[str, str],
    work: Path,
    report: ValidateReport,
    run_dir: Path | None,
    *,
    publication: str,
    agent_version: str,
    lift: float | None,
    flows: FlowWatch,
) -> str | None:
    """Attach the trial bundle, then the report that names it; return the report digest.

    The report carries model, agent, agent version, lift, publication and either
    ``skillci.dev/trials`` or ``skillci.dev/trials-withheld``; its files go through the
    bundle's redaction first. Never raises: ``None`` means no report was attached.
    """
    try:
        trials = attach_trials(config, environ, work, run_dir, agent_version, flows)
        annotations = {"skillci.dev/model": config["MODEL"], "skillci.dev/agent": config["AGENT"]}
        if agent_version:
            annotations["skillci.dev/agent-version"] = agent_version
        if lift is not None:
            annotations["skillci.dev/lift"] = f"{lift:+.2f}"
        annotations["skillci.dev/publication"] = publication
        annotations[trials[0]] = trials[1]
        directory = work / "report"
        files = collect_report(directory, report, run_dir)
        files = redact_report(config, environ, directory, files)
        return attach_report(config, directory, files, annotations)
    except Exception as err:
        print(f"skill-eval: report not attached: {redact(str(err))}", file=sys.stderr)
        return None


def final_publication(report: ValidateReport, tiers: Mapping[str, str]) -> str:
    """Return the ``publication`` of the termination message.

    It is SkillEvaluator's assessment, with two guards in the safe direction: a failed tier
    is a failure (SkillEvaluator reports ``incomplete`` first when something else is missing),
    and ``passed`` needs every tier to have passed.
    """
    if "failed" in tiers.values():
        return "failed"
    if report.publication == "passed" and set(tiers.values()) != {"passed"}:
        return "incomplete"
    return report.publication


def gate_findings(findings: list[str], notes: list[str], tier2: bool) -> list[str]:
    """Return ``findings`` as reported: without Tier 2 the catalog notes replace its findings."""
    if tier2:
        return findings
    return notes + [f for f in findings if not f.startswith("tier2/")]


def evaluate(environ: Mapping[str, str]) -> dict[str, Any]:
    """Pull the skill, run Tiers 1 to 3, attach the trial bundle and the report.

    Returns the termination payload. An error raised once Tier 3 started still attaches the
    bundle and the report (best effort) and carries the report digest (``EvalError.report``).
    """
    config = load_config(environ)
    check_judge(config, environ)
    if config["AGENT"] == "opencode":
        check_gateway_key(config["MODEL"], environ)
    with tempfile.TemporaryDirectory(prefix="skill-eval-") as tmp:
        work = Path(tmp)
        # skills/<name> is the layout SkillEvaluator expects for a skill.
        skill_dir = pull_skill(config, work / "skills")
        env = tool_env(environ)
        tier2, catalog, notes = prepare_catalog(config, work, env)
        static = "1,2" if tier2 else "1"
        # The gate: Tier 1 and 2 only, so a Rejected skill costs no sandbox and no tokens.
        out = work / "validate-static"
        cmd = validate_command(config, skill_dir, out, tiers=static, catalog=catalog)
        report = read_validate(out, run_static(cmd, env))
        payload: dict[str, Any] = {"agent": config["AGENT"], "model": config["MODEL"]}
        # What was asked for; replaced by what the trials report.
        agent_ver = config["AGENT_VERSION"]
        run_dir: Path | None = None
        tiers = {**report.tiers, "tier3": "skipped"}
        findings = report.findings
        flows = FlowWatch(environ, config["SKILLEVAL_NAME"], work / "flows")
        results = work / "results"
        full_out = work / "validate"
        tier3_started = False
        try:
            if "failed" in (report.tiers["tier1"], report.tiers["tier2"]):
                print("skill-eval: Tier 1 or Tier 2 failed, skipping Tier 3", flush=True)
            elif not has_tier3_source(skill_dir):
                # Nothing for Tier 3 to run: no sandboxes, no tokens.
                print(f"skill-eval: gate {report.tiers}, findings {report.findings}", flush=True)
                raise EvalError(NO_DATASET_ERROR.format(skill=config["SKILL_NAME"]))
            else:
                # One validate run over every tier: its report carries SkillEvaluator's own
                # publication assessment. Output streams straight to the container log. A
                # non-zero exit is not fatal: a failed trial can still leave scores for the
                # other arm. The network flows are collected for exactly that run.
                tiers = {**report.tiers, "tier3": "incomplete"}  # until Tier 3 reports
                cmd = validate_command(
                    config, skill_dir, full_out, tiers=f"{static},3", catalog=catalog,
                    results=results,
                )  # fmt: skip
                tier3_started = True
                if bundle_enabled(environ):
                    flows.start()
                try:
                    proc = subprocess.run(cmd, check=False, env=trial_retry_env(env, config, work))
                finally:
                    flows.stop()
                try:
                    payload = read_scores(results, config["AGENT"])
                except EvalError as err:
                    raise EvalError(f"skillevaluator exited {proc.returncode}: {err}") from err
                run_dir = newest_result(results).parent
                payload["model"] = config["MODEL"]  # not the gateway form SkillEvaluator ran
                agent_ver = used_agent_version(run_dir) or agent_ver
                full = read_validate(full_out, proc)
                if full.json_path is None:  # Tier 3 left scores but no combined report
                    full = ValidateReport(
                        report.tiers, full.findings, "incomplete", None, None, None
                    )
                report = full
                tier3 = tier3_tier(results)
                tiers = {**report.tiers, "tier3": tier3.status}
                findings = report.findings + tier3.findings
        except Exception as err:
            # The gate ran, so the error keeps its tiers and findings (CONTRACT.md, Job contract).
            gate = gate_findings(findings, notes, tier2)[:MAX_FINDINGS]
            failure = err if isinstance(err, EvalError) else None
            if failure is None:
                failure = EvalError(str(err) or type(err).__name__, tiers, gate)
            elif failure.tiers is None:
                failure.tiers, failure.findings = dict(tiers), gate
            if tier3_started:
                # A failed run keeps what Tier 3 left: its trials and its report.
                failed_dir = run_dir or find_run_dir(results)
                last = read_validate(full_out, None)
                last = last if last.json_path is not None else report
                failure.report = publish(
                    config, environ, work, last, failed_dir,
                    publication=final_publication(last, failure.tiers),
                    agent_version=(failed_dir and used_agent_version(failed_dir)) or agent_ver,
                    lift=None,
                    flows=flows,
                )  # fmt: skip
            if failure is err:
                raise
            raise failure from err
        findings = gate_findings(findings, notes, tier2)
        publication = final_publication(report, tiers)
        if agent_ver:
            payload["agentVersion"] = agent_ver
        digest = publish(
            config, environ, work, report, run_dir,
            publication=publication, agent_version=agent_ver, lift=payload.get("lift"),
            flows=flows,
        )  # fmt: skip
        if digest:
            payload["report"] = digest
        payload |= {"tiers": tiers, "publication": publication, "findings": findings[:MAX_FINDINGS]}
        return payload


def write_termination(payload: dict[str, Any], path: str | None = None) -> None:
    """Write ``payload`` as JSON to the termination log, within the size limit.

    An oversized message loses findings first, from the last one; an oversized error is then
    cut in half until it fits. The tiers always stay.
    """
    path = path or os.environ.get("TERMINATION_LOG", TERMINATION_LOG)
    text = json.dumps(payload)
    while len(text.encode()) > MAX_MESSAGE_BYTES and payload.get("findings"):
        payload = {**payload, "findings": payload["findings"][:-1]}
        text = json.dumps(payload)
    while len(text.encode()) > MAX_MESSAGE_BYTES and "error" in payload:
        payload = {**payload, "error": payload["error"][: len(payload["error"]) // 2] + "..."}
        text = json.dumps(payload)
    Path(path).write_text(text)


def error_payload(err: Exception) -> dict[str, Any]:
    """Return the termination payload of a failed run: the error and, after the gate, its tiers.

    ``{"error": ...}`` alone when the run failed before the Tier 1 + 2 gate; otherwise also
    ``tiers`` (Tier 3 ``skipped``, or ``incomplete`` when it started) and the gate's
    ``findings``, so the controller can still show the gate's outcome; ``report`` when Tier 3
    started and its report was attached anyway.
    """
    payload: dict[str, Any] = {"error": redact(str(err))}
    if isinstance(err, EvalError) and err.tiers is not None:
        payload["tiers"] = err.tiers
        payload["findings"] = [redact(f) for f in err.findings[:MAX_FINDINGS]]
    if isinstance(err, EvalError) and err.report:
        payload["report"] = err.report
    return payload


def redact(text: str) -> str:
    """Remove API key values from ``text`` so they never reach logs or the message."""
    for name in REDACTED_ENV:
        if value := os.environ.get(name):
            text = text.replace(value, "[redacted]")
    return text


def main() -> int:
    """Entrypoint: returns the process exit code."""
    try:
        payload = evaluate(os.environ)
    except Exception as err:
        failure = error_payload(err)
        print(f"skill-eval: {failure['error']}", file=sys.stderr)
        write_termination(failure)
        return 1
    payload["findings"] = [redact(f) for f in payload.get("findings", [])]
    print(f"skill-eval: {payload}", flush=True)
    write_termination(payload)
    return 0


if __name__ == "__main__":
    sys.exit(main())
