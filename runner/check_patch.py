"""Build-time check that the SkillEvaluator patch does what the OpenCode gateway route needs.

runner/Dockerfile runs this after installing the patched SkillEvaluator, so a patch that applies
but no longer takes effect fails the image build instead of every OpenCode eval on the cluster.
"""

import os
import sys
import tempfile
from pathlib import Path

from skillevaluator.tier3.harbor import runner
from skillevaluator.tier3.harbor.local_agents import SkillEvaluatorGatewayOpenCode

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

print("SkillEvaluator patch checks passed")
