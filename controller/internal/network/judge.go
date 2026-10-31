package network

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
)

// JudgeProviders are the judge providers SkillEvaluator 0.6.0 supports
// (SKILL_EVAL_LLM_PROVIDER), the default first.
var JudgeProviders = []string{
	v1alpha1.JudgeAnthropic, v1alpha1.JudgeOpenAI, v1alpha1.JudgeOpenAICompatible,
	v1alpha1.JudgeNVBuild,
}

// Judge is the LLM judge of a run (SkillCIConfig spec.judge). Empty fields
// mean the defaults: provider anthropic, SkillEvaluator's default model, the
// provider's own endpoint and the llm-keys key anthropic.
type Judge struct {
	// Provider is one of JudgeProviders; empty means anthropic.
	Provider string
	// Model is the judge model; empty means SkillEvaluator's default.
	Model string
	// BaseURL is the judge's API root; empty means the provider's default.
	BaseURL string
	// SecretKey is the llm-keys key that holds the judge's API key; empty means anthropic.
	SecretKey string
}

// DefaultJudge returns the built-in judge: Anthropic, its key in llm-keys anthropic.
func DefaultJudge() Judge {
	return Judge{Provider: v1alpha1.JudgeAnthropic, SecretKey: v1alpha1.DefaultJudgeSecretKey}
}

// ProviderName returns the provider, anthropic when unset.
func (j Judge) ProviderName() string {
	if j.Provider == "" {
		return v1alpha1.JudgeAnthropic
	}
	return j.Provider
}

// Key returns the llm-keys key of the judge's API key, anthropic when unset.
func (j Judge) Key() string {
	if j.SecretKey == "" {
		return v1alpha1.DefaultJudgeSecretKey
	}
	return j.SecretKey
}

var judgeModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$`)

// maxJudgeModel and maxJudgeBaseURL are the lengths the CRD allows.
const (
	maxJudgeModel   = 256
	maxJudgeBaseURL = 2048
)

// Validate checks the judge with the messages the SkillCIConfig Ready
// condition shows: the CRD rules, again, for objects stored without them.
func (j Judge) Validate() error {
	p := j.ProviderName()
	if !slices.Contains(JudgeProviders, p) {
		return fmt.Errorf("judge.provider %q must be one of %s", j.Provider, strings.Join(JudgeProviders, ", "))
	}
	if j.Model != "" && (len(j.Model) > maxJudgeModel || !judgeModel.MatchString(j.Model)) {
		return fmt.Errorf("judge.model %q must be at most %d letters, digits and . _ : / @ + -, starting with a letter or digit", j.Model, maxJudgeModel)
	}
	if errs := validation.IsConfigMapKey(j.Key()); len(errs) > 0 {
		return fmt.Errorf("judge.secretKey %q is not a valid Secret key: %s", j.SecretKey, strings.Join(errs, "; "))
	}
	switch {
	case p == v1alpha1.JudgeOpenAICompatible && j.BaseURL == "":
		return fmt.Errorf("judge.baseURL is required for provider %s", p)
	case p == v1alpha1.JudgeNVBuild && j.BaseURL != "":
		return fmt.Errorf("judge.baseURL is used only with the providers anthropic, openai and openai-compatible, not %s", p)
	}
	if j.BaseURL != "" {
		if _, err := baseURLHost(j.BaseURL); err != nil {
			return err
		}
	}
	return nil
}

// baseURLHost returns the lower-case host of an http or https API root
// without credentials, query or fragment.
func baseURLHost(raw string) (string, error) {
	bad := fmt.Errorf("judge.baseURL %q must be an http or https URL with a host and no credentials, query or fragment (at most %d characters)", raw, maxJudgeBaseURL)
	if len(raw) > maxJudgeBaseURL || strings.ContainsAny(raw, " \t\r\n\\") {
		return "", bad
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return "", bad
	}
	return strings.ToLower(u.Hostname()), nil
}

// Host returns the judge's API host, the one the verifier in the trial pod
// calls: the host of BaseURL when it is set, else the provider's default
// (anthropic api.anthropic.com; openai the host of llm-keys openai-base-url
// when it is set, else api.openai.com, because SkillEvaluator falls back to
// OPENAI_BASE_URL; nv_build integrate.api.nvidia.com).
func (j Judge) Host(openAIBaseURL string) (string, error) {
	if j.BaseURL != "" {
		return baseURLHost(j.BaseURL)
	}
	switch p := j.ProviderName(); p {
	case v1alpha1.JudgeAnthropic:
		return hostAnthropic, nil
	case v1alpha1.JudgeOpenAI:
		hosts, err := openAIHost(openAIBaseURL)
		if err != nil {
			return "", err
		}
		return hosts[0], nil
	case v1alpha1.JudgeNVBuild:
		return hostNvidia, nil
	default:
		return "", fmt.Errorf("no judge API host is known for judge provider %q", p)
	}
}

// NeedsOpenAIBaseURL reports whether the judge's host depends on the llm-keys
// openai-base-url value.
func (j Judge) NeedsOpenAIBaseURL() bool {
	return j.ProviderName() == v1alpha1.JudgeOpenAI && j.BaseURL == ""
}

// judgeHost is the host Resolve allows for the judge in none and allowlist.
func judgeHost(j Judge, openAIBaseURL string) (string, error) {
	host, err := j.Host(openAIBaseURL)
	var nerr *Error
	switch {
	case errors.As(err, &nerr):
		return "", nerr
	case err != nil:
		return "", &Error{Reason: ReasonUnknownModelHost, Message: err.Error()}
	}
	return host, nil
}

// judgeAgents are the judge providers the runner accepts per agent, the
// combinations SkillEvaluator 0.6.0 can stage for that agent's route. An
// agent missing here is left to the runner.
var judgeAgents = map[string][]string{
	"claude-code": {v1alpha1.JudgeAnthropic, v1alpha1.JudgeOpenAI, v1alpha1.JudgeOpenAICompatible, v1alpha1.JudgeNVBuild},
	"codex":       {v1alpha1.JudgeAnthropic, v1alpha1.JudgeOpenAI, v1alpha1.JudgeNVBuild},
	"opencode":    {v1alpha1.JudgeAnthropic},
}

// CheckAgent reports whether the runner can run the agent under this judge.
// The error is the failure message of a run that cannot start.
func (j Judge) CheckAgent(agent string) error {
	allowed, known := judgeAgents[agent]
	if !known || slices.Contains(allowed, j.ProviderName()) {
		return nil
	}
	return fmt.Errorf("judge provider %q cannot score agent %q: the runner supports the judge providers %s for it (SkillCIConfig spec.judge)",
		j.ProviderName(), agent, strings.Join(allowed, ", "))
}
