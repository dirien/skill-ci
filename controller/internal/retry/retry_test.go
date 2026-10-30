package retry

import "testing"

func TestClassify(t *testing.T) {
	const prefix = "no scores in result.json: "
	tests := []struct {
		name         string
		message      string
		noMessage    bool
		wantReason   Reason
		wantTransent bool
	}{
		{
			name:       "environment start timeout",
			message:    prefix + "EnvironmentStartTimeoutError: Environment start timed out after 600.0 seconds",
			wantReason: ReasonEnvStartTimeout, wantTransent: true,
		},
		{
			name:       "connection lost during apt-get",
			message:    prefix + "NonZeroAgentExitCodeError: Command failed (exit 1): apt-get update && apt-get install -y nodejs npm procps ... Connection to remote host was lost.",
			wantReason: ReasonConnectionLost, wantTransent: true,
		},
		{
			name:       "connection lost during the agent run",
			message:    prefix + "NonZeroAgentExitCodeError: Command failed (exit 255): claude --verbose -p 'do it' Connection to remote host was lost.",
			wantReason: ReasonConnectionLost, wantTransent: true,
		},
		{
			name:       "agent install step",
			message:    prefix + "NonZeroAgentExitCodeError: Command failed (exit 1): set -euo pipefail; if command -v apk >/dev/null; then apk add nodejs npm; fi; npm install -g @anthropic-ai/claude-code",
			wantReason: ReasonAgentInstall, wantTransent: true,
		},
		{
			name:       "apt-get install step without connection hint",
			message:    prefix + "NonZeroAgentExitCodeError: Command failed (exit 100): apt-get update && apt-get install -y nodejs npm procps",
			wantReason: ReasonAgentInstall, wantTransent: true,
		},
		{
			name:       "agent setup step",
			message:    prefix + "NonZeroAgentExitCodeError: Command failed (exit 1): mkdir -p $CLAUDE_CONFIG_DIR/debug $CLAUDE_CONFIG_DIR/projects/-app",
			wantReason: ReasonAgentSetup, wantTransent: true,
		},
		{
			name:       "all trials failed the same transient way",
			message:    prefix + "EnvironmentStartTimeoutError: Environment start timed out after 600.0 seconds; EnvironmentStartTimeoutError: Environment start timed out after 600.0 seconds",
			wantReason: ReasonEnvStartTimeout, wantTransent: true,
		},
		{
			name:       "two different transient causes",
			message:    prefix + "EnvironmentStartTimeoutError: timed out; NonZeroAgentExitCodeError: Command failed (exit 1): apt-get update",
			wantReason: ReasonEnvStartTimeout, wantTransent: true,
		},
		{
			name:       "job deadline exceeded",
			message:    "job failed: DeadlineExceeded: Job was active longer than specified deadline",
			wantReason: ReasonJobLost, wantTransent: true,
		},
		{
			name:       "pod evicted",
			message:    "job failed: Evicted: The node was low on resource: ephemeral-storage",
			wantReason: ReasonJobLost, wantTransent: true,
		},
		{
			name:       "image pull",
			message:    "job failed: ImagePullBackOff: Back-off pulling image",
			wantReason: ReasonJobLost, wantTransent: true,
		},
		{
			name:       "pod never scheduled",
			message:    "job failed: FailedScheduling: 0/3 nodes are available: Unschedulable",
			wantReason: ReasonJobLost, wantTransent: true,
		},
		{
			name:       "job failed without a termination message",
			message:    "job failed: BackoffLimitExceeded: Job has reached the specified backoff limit",
			noMessage:  true,
			wantReason: ReasonJobLost, wantTransent: true,
		},

		// Real results and anything unrecognised: never retried.
		{
			name:    "the agent timed out on its task",
			message: prefix + "AgentTimeoutError: Agent execution timed out after 900.0 seconds",
		},
		{
			name:    "the agent command itself failed",
			message: prefix + "NonZeroAgentExitCodeError: Command failed (exit 1): claude --verbose -p 'Run npm install and fix the tests'",
		},
		{
			name:    "agent timeout mixed with a transient error",
			message: prefix + "EnvironmentStartTimeoutError: timed out; AgentTimeoutError: Agent execution timed out after 900.0 seconds",
		},
		{
			name:    "agent failure mixed with a transient error",
			message: prefix + "NonZeroAgentExitCodeError: Command failed (exit 1): apt-get update; NonZeroAgentExitCodeError: Command failed (exit 2): claude -p 'x'",
		},
		{
			name:    "agent timeout that mentions a lost connection in its output",
			message: prefix + "AgentTimeoutError: Agent execution timed out; last output: Connection to remote host was lost",
		},
		{
			name:    "unknown error class",
			message: prefix + "RewardFileNotFoundError: no reward file",
		},
		{
			name:    "non-zero exit without a command",
			message: prefix + "NonZeroAgentExitCodeError: agent exited",
		},
		{name: "runner configuration error", message: "missing required env: AGENT"},
		{name: "oras pull failed", message: "oras pull failed (1): not found"},
		{name: "garbage termination message", message: "termination message is not a result: Traceback (most recent call last)"},
		{name: "job failed with a result", message: "job failed: BackoffLimitExceeded: Job has reached the specified backoff limit"},
		{name: "runner reported no scores", message: "runner reported no scores and no failed tier"},
		{name: "empty", message: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, ok := Classify(tt.message, tt.noMessage)
			if ok != tt.wantTransent || reason != tt.wantReason {
				t.Errorf("Classify(%q, %v) = (%q, %v), want (%q, %v)", tt.message, tt.noMessage, reason, ok, tt.wantReason, tt.wantTransent)
			}
			if got := Transient(tt.message, tt.noMessage); got != tt.wantTransent {
				t.Errorf("Transient(%q, %v) = %v, want %v", tt.message, tt.noMessage, got, tt.wantTransent)
			}
		})
	}
}

func TestReasonsAreDistinct(t *testing.T) {
	seen := map[Reason]bool{}
	for _, r := range Reasons() {
		if r == "" || seen[r] {
			t.Errorf("reason %q is empty or repeated", r)
		}
		seen[r] = true
	}
}
