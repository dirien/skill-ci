package runner

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
)

func TestParseResult(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name                string
		msg                 string
		with, without, lift *float64
		errField            string
		report              string
		wantErr             bool
	}{
		{name: "report digest", msg: `{"withSkill":0.9,"withoutSkill":0.4,"report":"sha256:abc"}`,
			with: f(0.9), without: f(0.4), lift: f(0.5), report: "sha256:abc"},
		{name: "report that is not a digest is dropped", msg: `{"withSkill":0.9,"withoutSkill":0.4,"report":"oops"}`,
			with: f(0.9), without: f(0.4), lift: f(0.5)},
		{name: "contract example", msg: `{"withSkill": 0.95, "withoutSkill": 0.36, "lift": 0.59, "agent": "claude-code", "model": "m"}`,
			with: f(0.95), without: f(0.36), lift: f(0.59)},
		{name: "trailing newline", msg: "{\"withSkill\":1,\"withoutSkill\":0.5,\"lift\":0.5}\n",
			with: f(1), without: f(0.5), lift: f(0.5)},
		{name: "lift derived when missing", msg: `{"withSkill":0.9,"withoutSkill":0.4}`,
			with: f(0.9), without: f(0.4), lift: f(0.5)},
		{name: "runner error", msg: `{"error": "no score produced"}`, errField: "no score produced"},
		{name: "json on last log line", msg: "starting\ntrial 1 done\n{\"withSkill\":1,\"withoutSkill\":0,\"lift\":1}",
			with: f(1), without: f(0), lift: f(1)},
		{name: "plain log tail", msg: "panic: boom\ngoroutine 1", wantErr: true},
		{name: "empty", msg: "  ", wantErr: true},
		{name: "scores missing", msg: `{"lift": 0.2}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := ParseResult(tt.msg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseResult() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if res.Report != tt.report {
				t.Errorf("Report = %q, want %q", res.Report, tt.report)
			}
			if res.Error != tt.errField {
				t.Errorf("Error = %q, want %q", res.Error, tt.errField)
			}
			for _, c := range []struct {
				name      string
				got, want *float64
			}{{"withSkill", res.WithSkill, tt.with}, {"withoutSkill", res.WithoutSkill, tt.without}, {"lift", res.Lift, tt.lift}} {
				if (c.got == nil) != (c.want == nil) || (c.got != nil && (*c.got-*c.want > 1e-9 || *c.want-*c.got > 1e-9)) {
					t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
				}
			}
		})
	}
}

func TestNewJob(t *testing.T) {
	se := &v1alpha1.SkillEval{
		Name: "acme-v1-0-0-haiku", Namespace: "skill-ci", UID: "uid-1",
		Labels: map[string]string{v1alpha1.LabelSkill: "acme", v1alpha1.LabelDigest: "abc", v1alpha1.LabelModel: "haiku"},
		Spec:   v1alpha1.SkillEvalSpec{Skill: "acme", Version: "v1.0.0", Digest: "sha256:abc", Agent: "claude-code", Model: "haiku", Attempts: 3},
	}
	job := NewJob(se, Config{Image: "reg/runner:1", Registry: "harbor.harbor.svc.cluster.local", Project: "skills"})

	if job.Name != se.Name || job.Namespace != "skill-ci" {
		t.Errorf("job identity = %s/%s", job.Namespace, job.Name)
	}
	if len(job.OwnerReferences) != 1 {
		t.Fatalf("want 1 owner reference, got %d", len(job.OwnerReferences))
	}
	owner := job.OwnerReferences[0]
	if owner.Kind != "SkillEval" || owner.Name != se.Name || owner.UID != "uid-1" || owner.Controller == nil || !*owner.Controller {
		t.Errorf("owner reference = %+v", owner)
	}
	if *job.Spec.BackoffLimit != 0 || *job.Spec.TTLSecondsAfterFinished != 3600 || *job.Spec.ActiveDeadlineSeconds != 7200 {
		t.Errorf("backoffLimit/ttl = %d/%d", *job.Spec.BackoffLimit, *job.Spec.TTLSecondsAfterFinished)
	}
	pod := job.Spec.Template.Spec
	if pod.ServiceAccountName != "skill-eval-runner" || pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("serviceAccount/restartPolicy = %s/%s", pod.ServiceAccountName, pod.RestartPolicy)
	}
	if !slices.Equal(pod.ImagePullSecrets, []corev1.LocalObjectReference{{Name: "docr"}}) {
		t.Errorf("imagePullSecrets = %v", pod.ImagePullSecrets)
	}
	c := pod.Containers[0]
	if c.Name != "skill-eval" || c.Image != "reg/runner:1" || len(c.Args) != 0 {
		t.Errorf("container = %s %s %v", c.Name, c.Image, c.Args)
	}
	if c.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError {
		t.Errorf("terminationMessagePolicy = %s", c.TerminationMessagePolicy)
	}

	plain := map[string]string{}
	secret := map[string]corev1.SecretKeySelector{}
	for _, e := range c.Env {
		if e.ValueFrom != nil {
			secret[e.Name] = *e.ValueFrom.SecretKeyRef
		} else {
			plain[e.Name] = e.Value
		}
	}
	wantPlain := map[string]string{
		"SKILL_REF": "harbor.harbor.svc.cluster.local/skills/acme@sha256:abc", "SKILL_NAME": "acme",
		"AGENT": "claude-code", "MODEL": "haiku", "ATTEMPTS": "3",
	}
	for k, v := range wantPlain {
		if plain[k] != v {
			t.Errorf("env %s = %q, want %q", k, plain[k], v)
		}
	}
	if s := secret["ANTHROPIC_API_KEY"]; s.Name != "llm-keys" || s.Key != "anthropic" || *s.Optional {
		t.Errorf("ANTHROPIC_API_KEY = %+v", s)
	}
	if s := secret["OPENAI_API_KEY"]; s.Name != "llm-keys" || s.Key != "openai" || !*s.Optional {
		t.Errorf("OPENAI_API_KEY = %+v", s)
	}

	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].MountPath != "/auth" || !c.VolumeMounts[0].ReadOnly {
		t.Errorf("volumeMounts = %+v", c.VolumeMounts)
	}
	vol := pod.Volumes[0].Secret
	if vol == nil || vol.SecretName != "harbor-pull" || len(vol.Items) != 1 ||
		vol.Items[0].Key != ".dockerconfigjson" || vol.Items[0].Path != "config.json" {
		t.Errorf("volume = %+v", pod.Volumes[0])
	}
}
