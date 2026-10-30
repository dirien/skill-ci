// Package retry decides whether a failed run failed because of the
// infrastructure it ran on, and so deserves one more try.
//
// The rule mirrors Harbor's error classification: errors in the environment
// (it did not start, the connection dropped, the agent could not be installed)
// are retried, errors of the agent itself (it timed out, its own task command
// failed) are not. A run that produced scores, or that Tier 1 or Tier 2
// rejected, is a real result and never reaches this package: retrying bad
// answers until they pass would bias the score. Callers must not ask about
// such runs.
//
// Anything not recognised is not transient. The list is short on purpose; a
// missed infrastructure failure costs one rerun by hand, a wrongly retried
// agent failure costs a skewed score.
package retry

import (
	"regexp"
	"strings"
)

// Reason is a short class of infrastructure failure, used as the value of the
// reason label of skillci_retries_total.
type Reason string

// Reasons of a transient failure.
const (
	// ReasonEnvStartTimeout means the sandbox environment did not start in time.
	ReasonEnvStartTimeout Reason = "env_start_timeout"
	// ReasonConnectionLost means the connection to the sandbox dropped.
	ReasonConnectionLost Reason = "connection_lost"
	// ReasonAgentInstall means installing the agent in the sandbox failed.
	ReasonAgentInstall Reason = "agent_install"
	// ReasonAgentSetup means the agent's setup command failed before it ran.
	ReasonAgentSetup Reason = "agent_setup"
	// ReasonJobLost means the Job or its pod was lost: evicted, never
	// scheduled, image pull failure, deadline exceeded, or it failed without
	// leaving a termination message.
	ReasonJobLost Reason = "job_lost"
)

// Reasons lists every Reason, so metrics can publish them at zero.
func Reasons() []Reason {
	return []Reason{ReasonEnvStartTimeout, ReasonConnectionLost, ReasonAgentInstall, ReasonAgentSetup, ReasonJobLost}
}

// Transient reports whether a failed run failed for an infrastructure reason.
// message is the failure message (the runner's error, or the Job failure
// reason); jobFailedWithoutMessage is true when the Job failed and the runner
// left no termination message at all, as when the pod was evicted or the node
// went away.
func Transient(message string, jobFailedWithoutMessage bool) bool {
	_, ok := Classify(message, jobFailedWithoutMessage)
	return ok
}

// Classify is Transient with the class of the failure. The reason is empty
// when ok is false.
//
// The runner joins the SkillEvaluator execution errors of all trials into one
// message ("no scores in result.json: AErr: text; BErr: text"). The message is
// split at every "<Name>Error:" token and every part must be transient: one
// agent error in the mix makes the whole run a real result.
func Classify(message string, jobFailedWithoutMessage bool) (reason Reason, ok bool) {
	if jobFailedWithoutMessage {
		return ReasonJobLost, true
	}
	parts := split(message)
	if len(parts) == 0 {
		if jobLevel.MatchString(message) {
			return ReasonJobLost, true
		}
		return "", false
	}
	for _, p := range parts {
		r, ok := classifyPart(p)
		if !ok {
			return "", false
		}
		if reason == "" {
			reason = r
		}
	}
	return reason, true
}

// errorToken finds the exception names SkillEvaluator puts in front of every
// execution error, such as "EnvironmentStartTimeoutError:".
var errorToken = regexp.MustCompile(`\b([A-Z][A-Za-z]*Error):`)

// connectionLost is the transport error of the sandbox exec channel. It says
// nothing about the agent whatever command was running.
const connectionLost = "Connection to remote host was lost"

// jobLevel matches Kubernetes-level reasons for a Job that never got to run
// or lost its pod: scheduling, image pull, eviction, deadline.
var jobLevel = regexp.MustCompile(`DeadlineExceeded|Evicted|Preempt|FailedScheduling|Unschedulable|ImagePullBackOff|ErrImagePull|ErrImageNeverPull`)

// setupCommand matches the start of the commands the Harbor agent runs to
// install and prepare the agent, after "Command failed (exit N): ". Only the
// start counts: the agent's own task command carries the task text, which may
// mention anything, including "npm install".
var setupCommand = regexp.MustCompile(`^(?:` +
	`apt-get |apt |apk |yum |dnf |` + // package manager
	`set -euo pipefail; if command -v apk|` + // the agent install script
	`curl [^|]*\| *(?:ba)?sh|npm (?:install|i) |pip3? install ` + // installers
	`)`)

// agentSetupCommand matches the per-run setup that precedes the agent command.
var agentSetupCommand = regexp.MustCompile(`^mkdir -p \$CLAUDE_CONFIG_DIR`)

// failedCommand extracts the command from a NonZeroAgentExitCodeError text.
var failedCommand = regexp.MustCompile(`Command failed \(exit \d+\):\s*(.*)`)

// part is one "<Name>Error: text" piece of a message.
type part struct{ name, text string }

// split cuts a message into its error parts. Text before the first token is
// context ("no scores in result.json:") and is dropped.
func split(message string) []part {
	locs := errorToken.FindAllStringSubmatchIndex(message, -1)
	parts := make([]part, 0, len(locs))
	for i, loc := range locs {
		end := len(message)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		parts = append(parts, part{name: message[loc[2]:loc[3]], text: message[loc[1]:end]})
	}
	return parts
}

// classifyPart decides one error part.
func classifyPart(p part) (Reason, bool) {
	switch {
	case p.name == "AgentTimeoutError":
		return "", false // the agent ran out of time on its task: a real result
	case p.name == "EnvironmentStartTimeoutError":
		return ReasonEnvStartTimeout, true
	case p.name == "AgentSetupTimeoutError":
		return ReasonAgentSetup, true // installing the agent CLI in the sandbox took too long
	case strings.Contains(p.text, connectionLost):
		return ReasonConnectionLost, true
	case p.name == "NonZeroAgentExitCodeError":
		m := failedCommand.FindStringSubmatch(p.text)
		if m == nil {
			return "", false
		}
		switch cmd := strings.TrimSpace(m[1]); {
		case setupCommand.MatchString(cmd):
			return ReasonAgentInstall, true
		case agentSetupCommand.MatchString(cmd):
			return ReasonAgentSetup, true
		}
		return "", false // the agent's own command failed
	case jobLevel.MatchString(p.text):
		return ReasonJobLost, true
	}
	return "", false
}
