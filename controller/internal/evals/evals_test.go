package evals

import (
	"context"
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
		name                    string
		skill, version, model   string
		want                    string
		wantLen, wantHashSuffix int
	}{
		{name: "short", skill: "acme-release-notes", version: "v1.0.0", model: "claude-haiku-5-5",
			want: "acme-release-notes-v1-0-0-claude-haiku-5-5"},
		{name: "odd characters", skill: "Acme_Notes", version: "v1.2.3", model: "openai/gpt-5",
			want: "acme-notes-v1-2-3-openai-gpt-5"},
		{name: "long", skill: long, version: "v1.0.0", model: "claude-sonnet-5-5", wantLen: 63, wantHashSuffix: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Name(tt.skill, tt.version, tt.model)
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
	if Name(long, "v1.0.0", "model-a") == Name(long, "v1.0.0", "model-b") {
		t.Error("long names that differ in the cut-off part must stay distinct")
	}
	first := Name(long, "v1.0.0", "m")
	if again := Name(long, "v1.0.0", "m"); first != again {
		t.Errorf("Name() must be deterministic, got %q then %q", first, again)
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

	tests := []struct {
		name        string
		req         Request
		wantCreated bool
	}{
		{"new digest creates", other, true},
		{"same digest and model dedupes", base, false},
		{"same digest under another tag dedupes", retagged, false},
		{"same digest other model creates", sonnetReq, true},
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

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}
