"""Build-time check that the SkillEvaluator patch does what the sandbox, OpenCode and retry need.

runner/Dockerfile runs this after installing the patched SkillEvaluator, so a patch that applies
but no longer takes effect fails the image build instead of every OpenCode eval on the cluster.
"""

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

from skillevaluator.tier3.harbor import runner
from skillevaluator.tier3.harbor.local_agents import SkillEvaluatorGatewayOpenCode

# Hunk 4: without it Harbor never sees the sandbox settings the runner passes; with a sandbox
# profile the trials would create bare Sandboxes and lose the eval label of the network policy.
ack_env = getattr(runner, "_HARBOR_ENV_MODE_VARS", {}).get("ack", frozenset())
for name in (
    "HARBOR_SANDBOX_IMAGE",
    "HARBOR_SANDBOX_CPUS",
    "HARBOR_SANDBOX_MEMORY_MB",
    "HARBOR_SANDBOX_WARM_POOL",
    "SKILLEVAL_NAME",
):
    if name not in ack_env:
        sys.exit(f"patch hunk 4: {name} does not reach Harbor on the ack backend")

# Hunk 6: without it the ack backend refuses the wrapper and Tier 3 is skipped.
allowed = getattr(runner, "_NATIVE_BACKEND_AGENT_IMPORT_PATHS", frozenset())
if getattr(runner, "_GATEWAY_OPENCODE_IMPORT_PATH", None) not in allowed:
    sys.exit("patch hunk 6: the OpenCode gateway wrapper is not allowed on the ack backend")

# Hunk 12: without it the agent gets no key and every trial fails with HTTP 401.
os.environ["OPENAI_API_KEY"] = "build-check"
os.environ["OPENAI_BASE_URL"] = "https://gateway.invalid/v1"
with tempfile.TemporaryDirectory() as logs:
    agent = SkillEvaluatorGatewayOpenCode(logs_dir=Path(logs), model_name="openai/some/model")
    if agent.model_connection.env.get("OPENAI_API_KEY") != "build-check":
        sys.exit("patch hunk 12: the OpenCode gateway wrapper does not forward OPENAI_API_KEY")

# Hunk 13: without it a provider rate limit fails the trial at once and TRIAL_RETRIES does nothing.
# Harbor resolves the command here (--print-config runs nothing), which also checks that the
# Harbor in the image accepts a JobConfig file with only `retry:` and keeps the flags over it.
with tempfile.TemporaryDirectory() as work:
    backoff = Path(work, "harbor-retry.json")
    backoff.write_text(
        json.dumps({"retry": {"wait_multiplier": 2, "min_wait_sec": 20, "max_wait_sec": 300}})
    )
    os.environ["SKILLEVALUATOR_TRIAL_RETRIES"] = "2"
    os.environ["SKILLEVALUATOR_HARBOR_CONFIG"] = str(backoff)
    command = runner.build_harbor_run_command(
        dataset_path=Path(work), agent="claude-code", job_name="build-check", env_mode="ack"
    )
    if "--max-retries" not in command or "-c" not in command:
        sys.exit("patch hunk 13: the Harbor command has no trial retries")
    resolved = subprocess.run(  # noqa: S603 (the command SkillEvaluator itself would run)
        [*command, "--print-config"], capture_output=True, text=True, cwd=work, check=False
    )
    if resolved.returncode != 0:
        sys.exit(f"patch hunk 13: Harbor refused the retry settings: {resolved.stderr.strip()}")
    retry = json.loads(resolved.stdout[resolved.stdout.index("{") :]).get("retry", {})
    expected = {
        "max_retries": 2,
        "include_exceptions": ["ApiRateLimitError"],
        "wait_multiplier": 2.0,
        "min_wait_sec": 20.0,
        "max_wait_sec": 300.0,
    }
    if retry != expected:
        sys.exit(f"patch hunk 13: Harbor resolved the retry settings to {retry}")

print("SkillEvaluator patch checks passed")
