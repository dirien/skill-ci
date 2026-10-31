package retry

import (
	"strings"
	"testing"
)

// rateLimitRun is the status.message of a real OpenCode run through
// OpenRouter where one of three trials was rate-limited. SkillEvaluator keeps
// the first 600 characters of each trial error, which cuts the stdout.
const rateLimitRun = `skillevaluator exited 0: no scores in result.json: Harbor job did not complete successfully: 1 errored; ` +
	`Agent runtime failed in banner-block__FXnvFq5: ApiRateLimitError: Command failed (exit 1): [ -f ~/.nvm/nvm.sh ] && . ~/.nvm/nvm.sh; opencode --model=skillevaluator-gateway/qwen/qwen3.8-flash run --format=json --thinking --dangerously-skip-permissions -- 'Make an ASCII art banner of the text '"'"'skill ci'"'"' in the block font and save it to banner.txt. ' 2>&1 </dev/null | stdbuf -oL tee /logs/agent/opencode.txt stdout: {"type":"step_start","timestamp":1793452792198,"sessionID":"ses_e6de255e8ffemc8cs6kenROjzz","part":{"id":"prt_1921dd84f001GnkywK7f4aAW2H","messageID":"msg_1921db2d6001CSTAJ2zk4fpXAv","sessionID":"ses_e6de255e8ffemc8cs6kenRO; ` +
	`Unscoreable reward in banner-block__FXnvFq5: ApiRateLimitError: Command failed (exit 1): [ -f ~/.nvm/nvm.sh ] && . ~/.nvm/nvm.sh; opencode --model=skillevaluator-gateway/qwen/qwen3.8-flash run --format=json --thinking --dangerously-skip-permissions -- 'Make an ASCII art banner of the text '"'"'skill ci'"'"' in the block font and save it to banner.txt. ' 2>&1 </dev/null | stdbuf -oL tee /logs/agent/opencode.txt stdout: {"type":"step_start","timestamp":1793452792198,"sessionID":"ses_e6de255e8ffemc8cs6kenROjzz","part":{"id":"prt_1921dd84f001GnkywK7f4aAW2H","messageID":"msg_1921db2d6001CSTAJ2zk4fpXAv","sessionID":"ses_e6de255e8ffemc8cs6kenRO; ` +
	`Missing scored attempts for cases: banner-block; Scored attempt coverage is 2/3; opencode without-skill Harbor run failed: Harbor job did not complete successfully: 1 errored`

// rateLimitTrial is one rate-limited trial in the shape of rateLimitRun.
func rateLimitTrial(trial, task string) string {
	return "Agent runtime failed in " + trial + ": ApiRateLimitError: Command failed (exit 1): [ -f ~/.nvm/nvm.sh ] && . ~/.nvm/nvm.sh; " +
		"opencode --model=skillevaluator-gateway/qwen/qwen3.8-flash run --format=json --thinking --dangerously-skip-permissions -- '" + task + "' " +
		`2>&1 </dev/null | stdbuf -oL tee /logs/agent/opencode.txt stdout: {"type":"step_start","timestamp":1793452792198}`
}

// harborRun wraps trial errors the way the runner and SkillEvaluator do.
func harborRun(trials ...string) string {
	return "skillevaluator exited 0: no scores in result.json: Harbor job did not complete successfully: 2 errored; " +
		strings.Join(trials, "; ") +
		"; Missing scored attempts for cases: rn-basic, rn-multiple; Scored attempt coverage is 7/9; opencode with-skill Harbor run failed: Harbor job did not complete successfully: 2 errored"
}

// cut ends message before sep the way the runner shortens a message that is
// too long for the termination log.
func cut(message, sep string) string {
	before, _, _ := strings.Cut(message, sep)
	return before + "..."
}

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
			name:       "agent setup timeout (CLI install too slow)",
			message:    prefix + "AgentSetupTimeoutError: Agent setup timed out after 360.0 seconds; AgentSetupTimeoutError: Agent setup timed out after 360.0 seconds",
			wantReason: ReasonAgentSetup, wantTransent: true,
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
			name:       "real OpenCode run with one rate-limited trial",
			message:    rateLimitRun,
			wantReason: ReasonRateLimit, wantTransent: true,
		},
		{
			name:       "two rate-limited trials",
			message:    harborRun(rateLimitTrial("rn-basic__a1", "Write release notes."), rateLimitTrial("rn-multiple__b2", "Write release notes for two tags.")),
			wantReason: ReasonRateLimit, wantTransent: true,
		},
		{
			name:       "rate limit and an environment start timeout",
			message:    harborRun(rateLimitTrial("rn-basic__a1", "Write release notes."), "Unscoreable reward in rn-multiple__b2: EnvironmentStartTimeoutError: Environment start timed out after 600.0 seconds"),
			wantReason: ReasonRateLimit, wantTransent: true,
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
		{
			name:    "rate limit mixed with an agent timeout",
			message: harborRun(rateLimitTrial("rn-basic__a1", "Write release notes."), "Agent runtime failed in rn-multiple__b2: AgentTimeoutError: Agent execution timed out after 900.0 seconds"),
		},
		{
			name: "rate limit mixed with a failed agent task command",
			message: harborRun(rateLimitTrial("rn-basic__a1", "Write release notes."),
				"Agent runtime failed in rn-multiple__b2: NonZeroAgentExitCodeError: Command failed (exit 1): [ -f ~/.nvm/nvm.sh ] && . ~/.nvm/nvm.sh; opencode run -- 'Write release notes for two tags.' stdout: {}"),
		},
		{
			// Harbor scans the agent's transcript for "rate limit": a task about
			// rate limits makes any failure of the agent look like one.
			name:    "rate limit for a task that mentions rate limits",
			message: harborRun(rateLimitTrial("api-client__a1", "Add a Rate-Limit aware retry to the HTTP client.")),
		},
		{
			name:    "rate limit for a task that mentions too many requests",
			message: harborRun(rateLimitTrial("api-client__a1", "Handle 429 Too Many Requests in fetch.py.")),
		},
		{
			name:    "rate limit whose command is cut before its end",
			message: prefix + "Agent runtime failed in long__a1: ApiRateLimitError: Command failed (exit 1): opencode run -- 'A long task that SkillEvaluator cut before the rate limit part",
		},
		{
			name:    "rate limit without Harbor's failed command",
			message: prefix + "ApiRateLimitError: the agent printed this",
		},
		{
			name:    "rate limit next to a trial failure without an error token",
			message: harborRun(rateLimitTrial("rn-basic__a1", "Write release notes."), "Agent runtime failed in rn-multiple__b2: API Error: 500 Internal server error"),
		},
		{
			name:    "rate limit in a message the runner cut",
			message: cut(harborRun(rateLimitTrial("rn-basic__a1", "Write release notes."), rateLimitTrial("rn-multiple__b2", "Write release notes for two tags.")), "; Missing"),
		},
		{
			name:    "rate limit with failure details left out",
			message: harborRun(rateLimitTrial("rn-basic__a1", "Write release notes."), "Agent runtime failure details were truncated (showing 32 of 40)"),
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
