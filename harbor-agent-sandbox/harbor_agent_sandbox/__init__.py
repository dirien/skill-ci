"""Kubernetes Agent Sandbox environment for Harbor."""

from harbor_agent_sandbox import preinstalled
from harbor_agent_sandbox.environment import AgentSandboxEnvironment

# OpenCode reinstalls itself in every sandbox unless this check is added (see preinstalled).
preinstalled.apply()

__all__ = ["AgentSandboxEnvironment"]
