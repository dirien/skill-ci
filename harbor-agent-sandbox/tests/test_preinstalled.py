"""Tests for skipping the install of an agent CLI that the sandbox image already has."""

from __future__ import annotations

import asyncio
import logging

import pytest
from harbor.environments.base import ExecResult

import harbor_agent_sandbox  # noqa: F401  (applies the OpenCode check)
from harbor_agent_sandbox import preinstalled


class FakeAgent:
    """Just enough of an installed agent: a version, a version command and an install."""

    installs = 0
    logger = logging.getLogger("fake")

    def __init__(self, version: str | None) -> None:
        self._version = version

    @staticmethod
    def name() -> str:
        return "fake"

    def version(self) -> str | None:
        return self._version

    def get_version_command(self) -> str | None:
        return "fake --version"

    def parse_version(self, stdout: str) -> str:
        return stdout.strip()

    async def install(self, environment) -> None:
        type(self).installs += 1


class FakeEnvironment:
    """Answers the version command with a fixed result."""

    def __init__(self, stdout: str | None = "1.19.1\n", return_code: int = 0) -> None:
        self.stdout, self.return_code, self.commands = stdout, return_code, []

    async def exec(self, command, **kwargs):
        self.commands.append(command)
        return ExecResult(stdout=self.stdout, return_code=self.return_code)


@pytest.fixture
def agent_class():
    """A fresh agent class per test, with the check applied."""
    cls = type("Agent", (FakeAgent,), {"installs": 0})
    preinstalled.skip_install_when_preinstalled(cls)
    return cls


@pytest.mark.parametrize(
    ("requested", "stdout", "return_code", "installs"),
    [
        ("1.19.1", "1.19.1\n", 0, 0),  # already there
        ("1.19.1", "update available\n1.19.1\n", 0, 0),  # a banner line before the version
        ("1.19.0", "1.19.1\n", 0, 1),  # another release
        ("1.19.1", "", 0, 1),  # prints nothing
        ("1.19.1", "command not found", 127, 1),  # not installed
        (None, "1.19.1\n", 0, 1),  # no release requested: nothing to compare
    ],
)
def test_install_runs_only_when_the_release_differs(
    agent_class, requested, stdout, return_code, installs
):
    """The version command decides whether the install runs."""
    agent, env = agent_class(requested), FakeEnvironment(stdout, return_code)
    asyncio.run(agent.install(env))
    assert agent_class.installs == installs
    assert env.commands == (["fake --version"] if requested else [])


def test_check_is_applied_once():
    """Applying twice does not wrap twice."""
    cls = type("Agent", (FakeAgent,), {"installs": 0})
    preinstalled.skip_install_when_preinstalled(cls)
    wrapped = cls.install
    preinstalled.skip_install_when_preinstalled(cls)
    assert cls.install is wrapped
    env = FakeEnvironment("1.19.1")
    asyncio.run(cls("1.19.0").install(env))
    assert cls.installs == 1 and env.commands == ["fake --version"]


def test_opencode_gets_the_check_on_import():
    """Importing the package wraps OpenCode.install; Claude Code and Codex are left alone."""
    from harbor.agents.installed.claude_code import ClaudeCode
    from harbor.agents.installed.codex import Codex
    from harbor.agents.installed.opencode import OpenCode

    assert getattr(OpenCode.install, preinstalled._MARKER, False)
    assert not getattr(ClaudeCode.install, preinstalled._MARKER, False)
    assert not getattr(Codex.install, preinstalled._MARKER, False)


def test_real_opencode_skips_a_matching_install(tmp_path):
    """The real OpenCode agent returns after one version check when the CLI is at its release."""
    from harbor.agents.installed.opencode import OpenCode

    agent = OpenCode(logs_dir=tmp_path, model_name="ollama-cloud/gpt-oss:120b", version="1.19.1")
    env = FakeEnvironment("1.19.1\n")
    asyncio.run(agent.install(env))
    assert len(env.commands) == 1
    assert "opencode --version" in env.commands[0]
