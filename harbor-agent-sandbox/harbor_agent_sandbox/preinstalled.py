"""Skip an agent CLI's install step when the sandbox image already has the requested release.

Harbor installs the agent CLI in every trial's sandbox. Claude Code and Codex first check
whether the CLI is there at the requested version and skip the install when it is. OpenCode has
no such check: it always runs nvm, Node and ``npm i -g opencode-ai`` again, which is the step
that timed out on a busy cluster. The prebuilt sandbox image (``sandbox/Dockerfile``) has all
three CLIs at the pinned releases, so :func:`skip_install_when_preinstalled` gives OpenCode the
same check. A release that differs from the preinstalled one is installed as before.
"""

from __future__ import annotations

import functools
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from harbor.agents.installed.base import BaseInstalledAgent
    from harbor.environments.base import BaseEnvironment

_MARKER = "_skill_ci_preinstalled_check"


async def installed_version_matches(
    agent: BaseInstalledAgent, environment: BaseEnvironment
) -> bool:
    """Return True when the agent CLI in the environment reports the version the agent asks for.

    False when no version is requested, the agent has no version command, the command fails or
    prints another version.
    """
    version = agent.version()
    command = agent.get_version_command()
    if not version or not command:
        return False
    result = await environment.exec(command=command)
    if result.return_code != 0 or not result.stdout:
        return False
    if agent.parse_version(result.stdout) == version:
        return True
    return any(line.strip() == version for line in result.stdout.splitlines())


def skip_install_when_preinstalled(agent_class: type[BaseInstalledAgent]) -> None:
    """Make ``agent_class.install`` return early when the requested release is already installed.

    Idempotent. A subclass that overrides ``install`` keeps its own behavior.
    """
    original = agent_class.install
    if getattr(original, _MARKER, False):
        return

    @functools.wraps(original)
    async def install(self: BaseInstalledAgent, environment: BaseEnvironment) -> None:
        if await installed_version_matches(self, environment):
            self.logger.debug(
                "%s %s is already installed in the sandbox", self.name(), self.version
            )
            return
        await original(self, environment)

    setattr(install, _MARKER, True)
    agent_class.install = install  # type: ignore[method-assign]


def apply() -> None:
    """Give OpenCode the preinstalled-CLI check that Claude Code and Codex have."""
    from harbor.agents.installed.opencode import OpenCode

    skip_install_when_preinstalled(OpenCode)


__all__ = ["apply", "installed_version_matches", "skip_install_when_preinstalled"]
