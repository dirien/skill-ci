package network

import (
	"errors"
	"strings"
	"testing"
)

func TestJudgeValidate(t *testing.T) {
	tests := []struct {
		name  string
		judge Judge
		err   string // substring; "" means valid
	}{
		{name: "zero value is the default judge", judge: Judge{}},
		{name: "default judge", judge: DefaultJudge()},
		{name: "openai with a model and its own key", judge: Judge{Provider: "openai", Model: "gpt-6.1-sol", SecretKey: "openai"}},
		{name: "openai-compatible with a base URL", judge: Judge{Provider: "openai-compatible", BaseURL: "https://llm.example.com/v1", SecretKey: "gateway"}},
		{name: "anthropic behind a proxy", judge: Judge{Provider: "anthropic", BaseURL: "http://proxy.corp.example:8080"}},
		{name: "model id with dots and a colon", judge: Judge{Provider: "openai-compatible", Model: "us.vendor.model-5:1", BaseURL: "https://llm.example.com/v1"}},
		{name: "bedrock is not supported", judge: Judge{Provider: "bedrock"}, err: `judge.provider "bedrock" must be one of anthropic, openai, openai-compatible, nv_build`},
		{name: "unknown provider", judge: Judge{Provider: "gemini"}, err: `judge.provider "gemini" must be one of anthropic, openai, openai-compatible, nv_build`},
		{name: "openai-compatible needs a base URL", judge: Judge{Provider: "openai-compatible"}, err: "judge.baseURL is required for provider openai-compatible"},
		{name: "nv_build takes no base URL", judge: Judge{Provider: "nv_build", BaseURL: "https://x.example.com"}, err: "not nv_build"},
		{name: "base URL without scheme", judge: Judge{Provider: "openai", BaseURL: "llm.example.com/v1"}, err: "must be an http or https URL"},
		{name: "base URL with credentials", judge: Judge{Provider: "openai", BaseURL: "https://user:pw@llm.example.com"}, err: "must be an http or https URL"},
		{name: "base URL with a query", judge: Judge{Provider: "openai", BaseURL: "https://llm.example.com/v1?key=x"}, err: "must be an http or https URL"},
		{name: "base URL with a fragment", judge: Judge{Provider: "openai", BaseURL: "https://llm.example.com/v1#x"}, err: "must be an http or https URL"},
		{name: "model with a space", judge: Judge{Model: "claude opus"}, err: "judge.model"},
		{name: "secret key with a slash", judge: Judge{SecretKey: "a/b"}, err: "judge.secretKey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.judge.Validate()
			switch {
			case tt.err == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)):
				t.Fatalf("Validate() = %v, want an error containing %q", err, tt.err)
			}
		})
	}
}

func TestJudgeHost(t *testing.T) {
	tests := []struct {
		name          string
		judge         Judge
		openAIBaseURL string
		want          string
	}{
		{name: "default", want: "api.anthropic.com"},
		{name: "anthropic base URL", judge: Judge{BaseURL: "https://Proxy.Corp.Example:8443/anthropic"}, want: "proxy.corp.example"},
		{name: "openai", judge: Judge{Provider: "openai"}, want: "api.openai.com"},
		{name: "openai follows openai-base-url", judge: Judge{Provider: "openai"}, openAIBaseURL: "https://gw.example.com/v1", want: "gw.example.com"},
		{name: "openai base URL wins over openai-base-url", judge: Judge{Provider: "openai", BaseURL: "https://judge.example.com/v1"},
			openAIBaseURL: "https://gw.example.com/v1", want: "judge.example.com"},
		{name: "openai-compatible", judge: Judge{Provider: "openai-compatible", BaseURL: "https://llm.example.com/v1"}, want: "llm.example.com"},
		{name: "nv_build", judge: Judge{Provider: "nv_build"}, want: "integrate.api.nvidia.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.judge.Host(tt.openAIBaseURL)
			if err != nil || got != tt.want {
				t.Fatalf("Host() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestJudgeNeedsOpenAIBaseURL(t *testing.T) {
	tests := []struct {
		judge Judge
		want  bool
	}{
		{Judge{}, false},
		{Judge{Provider: "openai"}, true},
		{Judge{Provider: "openai", BaseURL: "https://judge.example.com/v1"}, false},
		{Judge{Provider: "openai-compatible", BaseURL: "https://judge.example.com/v1"}, false},
	}
	for _, tt := range tests {
		if got := tt.judge.NeedsOpenAIBaseURL(); got != tt.want {
			t.Errorf("%+v.NeedsOpenAIBaseURL() = %v, want %v", tt.judge, got, tt.want)
		}
	}
}

func TestJudgeCheckAgent(t *testing.T) {
	tests := []struct {
		provider, agent string
		ok              bool
	}{
		{"anthropic", "claude-code", true},
		{"anthropic", "codex", true},
		{"anthropic", "opencode", true},
		{"openai", "claude-code", true},
		{"openai", "codex", true},
		{"openai", "opencode", false},
		{"openai-compatible", "claude-code", true},
		{"openai-compatible", "codex", false},
		{"nv_build", "codex", true},
		{"nv_build", "opencode", false},
		{"", "opencode", true},
		{"openai", "some-new-agent", true},
	}
	for _, tt := range tests {
		err := Judge{Provider: tt.provider}.CheckAgent(tt.agent)
		if (err == nil) != tt.ok {
			t.Errorf("Judge{%s}.CheckAgent(%s) = %v, want ok=%v", tt.provider, tt.agent, err, tt.ok)
		}
	}
	err := Judge{Provider: "openai"}.CheckAgent("opencode")
	want := `judge provider "openai" cannot score agent "opencode": the runner supports the judge providers anthropic for it (SkillCIConfig spec.judge)`
	if err == nil || err.Error() != want {
		t.Errorf("message %v, want %q", err, want)
	}
}

func TestJudgeHostOfAnUnknownProvider(t *testing.T) {
	if _, err := (Judge{Provider: "bedrock"}).Host(""); err == nil {
		t.Error("Host() of bedrock succeeded, want an error")
	}
	_, err := Resolve(defaultPolicy(), Input{Spec: spec("none"), Entry: claude, Judge: Judge{Provider: "bedrock"}, CiliumCRD: true})
	var nerr *Error
	if !errors.As(err, &nerr) || nerr.Reason != ReasonUnknownModelHost {
		t.Errorf("Resolve() = %v, want %s", err, ReasonUnknownModelHost)
	}
}
