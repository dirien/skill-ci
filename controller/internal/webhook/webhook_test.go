package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/platform"
)

const (
	digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func payload(eventType, fullName string, tags ...string) Payload {
	var p Payload
	p.Type = eventType
	p.EventData.Repository.RepoFullName = fullName
	for _, tag := range tags {
		p.EventData.Resources = append(p.EventData.Resources, Resource{Digest: digestA, Tag: tag})
	}
	return p
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name string
		p    Payload
		want []Push
	}{
		{"release push", payload("PUSH_ARTIFACT", "skills/acme", "v1.0.0"), []Push{{"acme", "v1.0.0", digestA}}},
		{"beta tag ignored", payload("PUSH_ARTIFACT", "skills/acme", "beta"), nil},
		{"stable tag ignored", payload("PUSH_ARTIFACT", "skills/acme", "stable"), nil},
		{"prerelease ignored", payload("PUSH_ARTIFACT", "skills/acme", "v1.0.0-rc1"), nil},
		{"untagged push ignored", payload("PUSH_ARTIFACT", "skills/acme", ""), nil},
		{"other project ignored", payload("PUSH_ARTIFACT", "images/acme", "v1.0.0"), nil},
		{"other event ignored", payload("DELETE_ARTIFACT", "skills/acme", "v1.0.0"), nil},
		{"mixed tags keep releases", payload("PUSH_ARTIFACT", "skills/acme", "beta", "v2.1.0"), []Push{{"acme", "v2.1.0", digestA}}},
		{"nested repository", payload("PUSH_ARTIFACT", "skills/team/acme", "v1.0.0"), []Push{{"team/acme", "v1.0.0", digestA}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.p, "skills"); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Decide() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecideRealPayload(t *testing.T) {
	raw := `{"type":"PUSH_ARTIFACT","occur_at":1,"operator":"admin","event_data":{"resources":[{"digest":"` + digestA + `","tag":"v1.0.0","resource_url":"x"}],"repository":{"name":"acme","namespace":"skills","repo_full_name":"skills/acme","repo_type":"private"}}}`
	var p Payload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if got := Decide(p, "skills"); len(got) != 1 || got[0].Skill != "acme" {
		t.Errorf("Decide() = %v", got)
	}
}

func newHandler(t *testing.T, token string, existing ...client.Object) (*Handler, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{
		Name: "skill-ci-models", Namespace: "skill-ci",
		Data: map[string]string{"models.yaml": "models:\n- {agent: claude-code, model: claude-haiku-5-5, required: true}\n- {agent: claude-code, model: claude-sonnet-5-5, required: true, sandboxProfile: big}\n"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(append(existing, cm)...).Build()
	return &Handler{Client: c, Namespace: "skill-ci", Project: "skills", ModelsConfigMap: "skill-ci-models",
		Token: token, Attempts: 1, Log: logr.Discard()}, c
}

func post(h *Handler, auth string, p Payload) *httptest.ResponseRecorder {
	body, _ := json.Marshal(p)
	req := httptest.NewRequest(http.MethodPost, "/webhook/harbor", strings.NewReader(string(body)))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func count(t *testing.T, c client.Client) int {
	t.Helper()
	var list v1alpha1.SkillEvalList
	if err := c.List(context.Background(), &list, client.InNamespace("skill-ci")); err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

func TestHandlerAuth(t *testing.T) {
	tests := []struct {
		name, token, header string
		wantStatus          int
		wantEvals           int
	}{
		{"matching token", "s3cret", "Bearer s3cret", http.StatusOK, 2},
		{"wrong token", "s3cret", "Bearer nope", http.StatusUnauthorized, 0},
		{"missing header", "s3cret", "", http.StatusUnauthorized, 0},
		{"empty token accepts all", "", "", http.StatusOK, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, c := newHandler(t, tt.token)
			rec := post(h, tt.header, payload("PUSH_ARTIFACT", "skills/acme", "v1.0.0"))
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := count(t, c); got != tt.wantEvals {
				t.Errorf("skillevals = %d, want %d", got, tt.wantEvals)
			}
		})
	}
}

func TestHandlerDedupe(t *testing.T) {
	h, c := newHandler(t, "")
	push := payload("PUSH_ARTIFACT", "skills/acme", "v1.0.0")

	post(h, "", push)
	if got := count(t, c); got != 2 {
		t.Fatalf("after first push: %d skillevals, want 2 (one per model)", got)
	}
	post(h, "", push)                                                      // Harbor retry
	post(h, "", payload("PUSH_ARTIFACT", "skills/acme", "v1.0.0", "beta")) // promotion retags the same digest
	if got := count(t, c); got != 2 {
		t.Errorf("after duplicates: %d skillevals, want 2", got)
	}

	next := payload("PUSH_ARTIFACT", "skills/acme", "v1.1.0")
	next.EventData.Resources[0].Digest = digestB
	post(h, "", next)
	if got := count(t, c); got != 4 {
		t.Errorf("after new digest: %d skillevals, want 4", got)
	}

	var list v1alpha1.SkillEvalList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	for _, se := range list.Items {
		if se.Spec.Trigger != v1alpha1.TriggerPush || !se.Spec.Required || se.Spec.Attempts != 1 {
			t.Errorf("unexpected spec %+v", se.Spec)
		}
		wantProfile := map[string]string{"claude-haiku-5-5": "default", "claude-sonnet-5-5": "big"}[se.Spec.Model]
		if se.Spec.SandboxProfile != wantProfile {
			t.Errorf("%s: sandboxProfile %q, want %q", se.Name, se.Spec.SandboxProfile, wantProfile)
		}
	}
}

func TestHandlerMethodAndPayload(t *testing.T) {
	h, _ := newHandler(t, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/webhook/harbor", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/webhook/harbor", strings.NewReader("{")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad json status = %d", rec.Code)
	}
}

func TestHandlerDefaultProfileFromConfig(t *testing.T) {
	tests := []struct {
		name   string
		config *v1alpha1.SkillCIConfig
		want   string
	}{
		{"no SkillCIConfig: built-in default", nil, "default"},
		{"SkillCIConfig default", &v1alpha1.SkillCIConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "skill-ci"},
			Spec:       v1alpha1.SkillCIConfigSpec{DefaultSandboxProfile: "small"},
		}, "small"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []client.Object
			if tt.config != nil {
				objs = append(objs, tt.config)
			}
			h, c := newHandler(t, "", objs...)
			h.Platform = &platform.Source{Reader: c, Namespace: "skill-ci"}
			if rec := post(h, "", payload("PUSH_ARTIFACT", "skills/acme", "v1.0.0")); rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			var list v1alpha1.SkillEvalList
			if err := c.List(context.Background(), &list, client.InNamespace("skill-ci")); err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, se := range list.Items {
				got[se.Spec.Model] = se.Spec.SandboxProfile
			}
			if got["claude-haiku-5-5"] != tt.want || got["claude-sonnet-5-5"] != "big" {
				t.Errorf("profiles %v, want haiku on %q and sonnet on its own big", got, tt.want)
			}
		})
	}
}
