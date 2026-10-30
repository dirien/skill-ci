package evals

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/models"
)

const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestName(t *testing.T) {
	long := strings.Repeat("very-long-skill-name-", 4)
	tests := []struct {
		name                           string
		skill, version, agent, agentVe string
		model, want                    string
		wantLen, wantHashSuffix        int
	}{
		{name: "short", skill: "acme-release-notes", version: "v1.0.0", agent: "claude-code", model: "claude-haiku-5-5",
			want: "acme-release-notes-v1-0-0-claude-code-claude-haiku-5-5"},
		{name: "pinned agent version", skill: "acme", version: "v1.1.0", agent: "claude-code", agentVe: "2.1.292", model: "claude-opus-5-5",
			want: "acme-v1-1-0-claude-code-2-1-292-claude-opus-5-5"},
		{name: "odd characters", skill: "Acme_Notes", version: "v1.2.3", agent: "codex", model: "openai/gpt-5",
			want: "acme-notes-v1-2-3-codex-openai-gpt-5"},
		{name: "model id with a colon", skill: "acme", version: "v1.0.0", agent: "opencode", agentVe: "1.19.1", model: "ollama-cloud/gpt-oss:120b",
			want: "acme-v1-0-0-opencode-1-19-1-ollama-cloud-gpt-oss-120b"},
		{name: "long", skill: long, version: "v1.0.0", agent: "claude-code", model: "claude-sonnet-5-5", wantLen: 63, wantHashSuffix: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Name(tt.skill, tt.version, tt.agent, tt.agentVe, tt.model)
			if tt.want != "" && got != tt.want {
				t.Errorf("Name() = %q, want %q", got, tt.want)
			}
			if tt.wantLen != 0 && len(got) != tt.wantLen {
				t.Errorf("len(Name()) = %d, want %d (%q)", len(got), tt.wantLen, got)
			}
			if tt.wantHashSuffix != 0 && len(got[strings.LastIndex(got, "-")+1:]) != tt.wantHashSuffix {
				t.Errorf("Name() = %q, want a %d char hash suffix", got, tt.wantHashSuffix)
			}
		})
	}
	if Name(long, "v1.0.0", "a", "", "model-a") == Name(long, "v1.0.0", "a", "", "model-b") {
		t.Error("long names that differ in the cut-off part must stay distinct")
	}
	if Name(long, "v1.0.0", "claude-code", "2.1.292", "m") == Name(long, "v1.0.0", "claude-code", "2.1.300", "m") {
		t.Error("long names that differ only in the agent version must stay distinct")
	}
	first := Name(long, "v1.0.0", "a", "1", "m")
	if again := Name(long, "v1.0.0", "a", "1", "m"); first != again {
		t.Errorf("Name() must be deterministic, got %q then %q", first, again)
	}
}

func TestNameSeparatesAgents(t *testing.T) {
	names := map[string]bool{}
	for _, m := range []models.Model{
		{Agent: "claude-code", Model: "claude-opus-5-5"},
		{Agent: "opencode", Model: "claude-opus-5-5"},
		{Agent: "claude-code", AgentVersion: "2.1.292", Model: "claude-opus-5-5"},
		{Agent: "claude-code", AgentVersion: "2.1.300", Model: "claude-opus-5-5"},
	} {
		names[Name("acme-release-notes", "v1.0.0", m.Agent, m.AgentVersion, m.Model)] = true
	}
	if len(names) != 4 {
		t.Errorf("entries with a different agent or agent version need different names, got %v", names)
	}
	for n := range names {
		if len(n) > maxName {
			t.Errorf("name %q is longer than %d", n, maxName)
		}
	}
}

func TestLabels(t *testing.T) {
	got := Labels("Acme", digest, "claude-code", "2.1.292", "claude-opus-5-5")
	want := map[string]string{
		v1alpha1.LabelSkill:        "acme",
		v1alpha1.LabelDigest:       "0123456789abcdef0123456789abcdef",
		v1alpha1.LabelAgent:        "claude-code",
		v1alpha1.LabelAgentVersion: "2.1.292",
		v1alpha1.LabelModel:        "claude-opus-5-5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Labels() = %v, want %v", got, want)
	}
	if _, ok := Labels("acme", digest, "claude-code", "", "m")[v1alpha1.LabelAgentVersion]; ok {
		t.Error("an unpinned entry has no agent version label")
	}
}

func TestDigestLabel(t *testing.T) {
	if got := DigestLabel(digest); got != "0123456789abcdef0123456789abcdef" {
		t.Errorf("DigestLabel() = %q", got)
	}
	if got := DigestLabel("sha256:ab"); got != "ab" {
		t.Errorf("DigestLabel(short) = %q", got)
	}
}

func TestEnsure(t *testing.T) {
	haiku := models.Model{Agent: "claude-code", Model: "claude-haiku-5-5", Required: true}
	sonnet := models.Model{Agent: "claude-code", Model: "claude-sonnet-5-5", Required: true}
	base := Request{Skill: "acme", Version: "v1.0.0", Digest: digest, Model: haiku, Attempts: 1, Trigger: v1alpha1.TriggerPush}
	other := base
	other.Digest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	other.Version = "v1.0.1"
	retagged := base // same digest and model under a new tag, as after a promotion
	retagged.Version = "v1.0.1"
	sonnetReq := base
	sonnetReq.Model = sonnet
	otherAgent := base // the same model under another harness
	otherAgent.Model.Agent = "opencode"
	pinned := base // pinning a version is another entry than the unpinned one
	pinned.Model.AgentVersion = "2.1.292"
	bumped := pinned // and so is every other version
	bumped.Model.AgentVersion = "2.1.300"

	tests := []struct {
		name        string
		req         Request
		wantCreated bool
	}{
		{"new digest creates", other, true},
		{"same digest and model dedupes", base, false},
		{"same digest under another tag dedupes", retagged, false},
		{"same digest other model creates", sonnetReq, true},
		{"same digest and model under another agent creates", otherAgent, true},
		{"same digest and model with a pinned agent version creates", pinned, true},
		{"a bumped agent version creates", bumped, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(New("skill-ci", base)).Build()
			created, err := Ensure(context.Background(), c, "skill-ci", tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if created != tt.wantCreated {
				t.Errorf("created = %v, want %v", created, tt.wantCreated)
			}
			var list v1alpha1.SkillEvalList
			if err := c.List(context.Background(), &list, client.InNamespace("skill-ci")); err != nil {
				t.Fatal(err)
			}
			want := 1
			if tt.wantCreated {
				want = 2
			}
			if len(list.Items) != want {
				t.Errorf("got %d skillevals, want %d", len(list.Items), want)
			}
		})
	}
}

func TestEnsureDedupesByAgentVersion(t *testing.T) {
	pinned := Request{
		Skill: "acme", Version: "v1.0.0", Digest: digest, Attempts: 1, Trigger: v1alpha1.TriggerPush,
		Model: models.Model{Agent: "claude-code", AgentVersion: "2.1.292", Model: "claude-opus-5-5", Required: true},
	}
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(New("skill-ci", pinned)).Build()
	created, err := Ensure(context.Background(), c, "skill-ci", pinned)
	if err != nil || created {
		t.Fatalf("same entry: created = %v, err = %v, want no new SkillEval", created, err)
	}
	unpinned := pinned
	unpinned.Model.AgentVersion = ""
	created, err = Ensure(context.Background(), c, "skill-ci", unpinned)
	if err != nil || !created {
		t.Fatalf("unpinned entry next to a pinned one: created = %v, err = %v, want a new SkillEval", created, err)
	}
	if created, err = Ensure(context.Background(), c, "skill-ci", unpinned); err != nil || created {
		t.Fatalf("unpinned entry twice: created = %v, err = %v, want it deduped", created, err)
	}
	var list v1alpha1.SkillEvalList
	if err := c.List(context.Background(), &list, client.InNamespace("skill-ci")); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Errorf("got %d skillevals, want 2", len(list.Items))
	}
}

func TestEnsureAdoptsLegacyObjects(t *testing.T) {
	// A SkillEval from before agent versions: old name, no agent version label.
	legacy := New("skill-ci", Request{
		Skill: "acme", Version: "v1.0.0", Digest: digest, Attempts: 1,
		Model: models.Model{Agent: "claude-code", Model: "claude-haiku-5-5", Required: true},
	})
	legacy.Name = "acme-v1-0-0-claude-haiku-5-5"
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(legacy).Build()
	created, err := Ensure(context.Background(), c, "skill-ci", Request{
		Skill: "acme", Version: "v1.0.0", Digest: digest, Attempts: 1,
		Model: models.Model{Agent: "claude-code", Model: "claude-haiku-5-5", Required: true},
	})
	if err != nil || created {
		t.Errorf("created = %v, err = %v, want the legacy object to count for the unpinned entry", created, err)
	}
}

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}
