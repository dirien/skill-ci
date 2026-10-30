package controller

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/models"
	"github.com/dirien/skill-ci/controller/internal/policy"
)

// maxPasses bounds the simulated work queue; a reconcile storm would exceed it.
const maxPasses = 20

// snapshot lists the SkillEvals by name.
func (e *env) snapshot() map[string]*v1alpha1.SkillEval {
	e.t.Helper()
	var list v1alpha1.SkillEvalList
	if err := e.client.List(context.Background(), &list, client.InNamespace(ns)); err != nil {
		e.t.Fatal(err)
	}
	out := map[string]*v1alpha1.SkillEval{}
	for i := range list.Items {
		out[list.Items[i].Name] = &list.Items[i]
	}
	return out
}

// settle plays the manager's part for one reconcile: it reconciles se, feeds
// every resulting SkillEval update through the sibling watch (predicate and
// mapper), and keeps reconciling what that enqueues until the queue is empty.
// It returns how many reconciles ran after the first one.
func (e *env) settle(se *v1alpha1.SkillEval) int {
	e.t.Helper()
	queue := []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(se)}}
	extra := 0
	for pass := 0; len(queue) > 0; pass++ {
		if pass >= maxPasses {
			e.t.Fatalf("still reconciling after %d passes: reconcile storm", maxPasses)
		}
		req := queue[0]
		queue = queue[1:]
		before := e.snapshot()
		if _, err := e.r.Reconcile(context.Background(), req); err != nil {
			e.t.Fatalf("Reconcile(%s): %v", req.Name, err)
		}
		if pass > 0 {
			extra++
		}
		for name, now := range e.snapshot() {
			old, ok := before[name]
			if !ok || !resultChanged().Update(event.UpdateEvent{ObjectOld: old, ObjectNew: now}) {
				continue
			}
			for _, next := range e.r.siblingRequests(context.Background(), now) {
				if !slices.Contains(queue, next) {
					queue = append(queue, next)
				}
			}
		}
	}
	return extra
}

// runWatched is run with the sibling watch wired in: the Job result is
// reconciled and so is everything that result enqueues.
func (e *env) runWatched(se *v1alpha1.SkillEval, message string) {
	e.t.Helper()
	e.reconcile(se) // Pending -> Running
	e.finishJob(se, message, false)
	e.settle(se) // Running -> Succeeded, then the siblings
}

// TestSiblingsReconvergeWhenScoresArriveLate reproduces the live bug: the
// candidate (v1.2.0) finishes all its models before the old version (v1.0.0)
// finishes its last one. v1.2.0 first becomes stable because nothing competes;
// when v1.0.0 completes it takes stable over, and v1.2.0, whose own
// SkillEvals are not touched by a Job event, has to be judged against it.
func TestSiblingsReconvergeWhenScoresArriveLate(t *testing.T) {
	e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
		map[string][]harbor.Artifact{"acme": {
			{Digest: digestA, Tags: []string{"v1.0.0"}},
			{Digest: digestB, Tags: []string{"v1.2.0"}},
		}})
	stableHaiku := e.create("acme", "v1.0.0", digestA, haiku)
	stableSonnet := e.create("acme", "v1.0.0", digestA, sonnet)
	candHaiku := e.create("acme", "v1.2.0", digestB, haiku)
	candSonnet := e.create("acme", "v1.2.0", digestB, sonnet)

	e.runWatched(stableHaiku, scoreWith(0.95, 0.36))
	e.runWatched(candHaiku, scoreWith(0.70, 0.36))
	e.runWatched(candSonnet, scoreWith(0.71, 0.36))
	if got := tags(t, e.harbor, "acme", digestB); !slices.Contains(got, "stable") {
		t.Fatalf("v1.2.0 tags = %v, want stable while it is the only complete version", got)
	}
	e.runWatched(stableSonnet, scoreWith(0.95, 0.36)) // the late result

	if got := tags(t, e.harbor, "acme", digestA); !slices.Contains(got, "stable") {
		t.Errorf("v1.0.0 tags = %v, want stable", got)
	}
	if got := tags(t, e.harbor, "acme", digestB); slices.Contains(got, "stable") || !slices.Contains(got, "beta") {
		t.Errorf("v1.2.0 tags = %v, want beta without stable", got)
	}
	for _, se := range []*v1alpha1.SkillEval{candHaiku, candSonnet} {
		got := e.get(se)
		if got.Status.Verdict != v1alpha1.VerdictRegressed {
			t.Errorf("%s verdict = %s, want Regressed", se.Name, got.Status.Verdict)
		}
		if findCondition(got.Status.Conditions, ConditionRegressed) == nil {
			t.Errorf("%s has no Regressed condition", se.Name)
		}
	}
}

// TestSiblingWatchDoesNotStorm checks the watch settles: once everything is
// reconciled, finishing nothing new enqueues nothing, and a re-run of the
// siblings changes no status.
func TestSiblingWatchDoesNotStorm(t *testing.T) {
	e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
		map[string][]harbor.Artifact{"acme": {
			{Digest: digestA, Tags: []string{"v1.0.0"}},
			{Digest: digestB, Tags: []string{"v1.2.0"}},
		}})
	var all []*v1alpha1.SkillEval
	for _, m := range []models.Model{haiku, sonnet} {
		all = append(all, e.create("acme", "v1.0.0", digestA, m), e.create("acme", "v1.2.0", digestB, m))
	}
	for i, se := range all {
		with := 0.95
		if i%2 == 1 {
			with = 0.70
		}
		e.runWatched(se, scoreWith(with, 0.36))
	}

	before := e.snapshot()
	for _, se := range all {
		if extra := e.settle(se); extra != 0 {
			t.Errorf("settled SkillEval %s enqueued %d more reconciles, want 0", se.Name, extra)
		}
	}
	for name, now := range e.snapshot() {
		if now.ResourceVersion != before[name].ResourceVersion {
			t.Errorf("%s was written again (resourceVersion %s -> %s)", name, before[name].ResourceVersion, now.ResourceVersion)
		}
	}
}

func TestResultChangedPredicate(t *testing.T) {
	running := &v1alpha1.SkillEval{Status: v1alpha1.SkillEvalStatus{Phase: v1alpha1.PhaseRunning}}
	succeeded := &v1alpha1.SkillEval{Status: v1alpha1.SkillEvalStatus{Phase: v1alpha1.PhaseSucceeded}}
	policyA := succeeded.DeepCopy()
	policyA.Status.Conditions = append(policyA.Status.Conditions, condition(ConditionPolicy, "no tag changes"))
	policyB := succeeded.DeepCopy()
	policyB.Status.Conditions = append(policyB.Status.Conditions, condition(ConditionPolicy, "tag changes: +stable"))
	regressed := policyA.DeepCopy()
	regressed.Status.Verdict = v1alpha1.VerdictRegressed

	tests := []struct {
		name     string
		old, new *v1alpha1.SkillEval
		want     bool
	}{
		{"reaches Succeeded", running, succeeded, true},
		{"policy outcome changes", policyA, policyB, true},
		{"policy outcome set the first time", succeeded, policyA, true},
		{"verdict-only change", policyA, regressed, false},
		{"still running", running, running, false},
		{"already Succeeded and unchanged", succeeded, succeeded, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resultChanged().Update(event.UpdateEvent{ObjectOld: tt.old, ObjectNew: tt.new})
			if got != tt.want {
				t.Errorf("Update = %v, want %v", got, tt.want)
			}
		})
	}
	if resultChanged().Create(event.CreateEvent{Object: succeeded}) {
		t.Error("Create passed, want ignored")
	}
}

func TestSiblingRequests(t *testing.T) {
	e := newEnv(t, Config{MaxConcurrentJobs: 8}, 200, nil)
	self := e.create("acme", "v1.0.0", digestA, haiku)
	done := e.create("acme", "v1.2.0", digestB, haiku)
	pending := e.create("acme", "v1.2.0", digestB, sonnet)
	other := e.create("other", "v1.0.0", digestA, haiku)
	for _, se := range []*v1alpha1.SkillEval{self, done, other} {
		se.Status.Phase = v1alpha1.PhaseSucceeded
		if err := e.client.Status().Update(context.Background(), se); err != nil {
			t.Fatal(err)
		}
	}

	reqs := e.r.siblingRequests(context.Background(), e.get(self))
	if len(reqs) != 1 || reqs[0].Name != done.Name {
		t.Errorf("requests = %v, want only %s (not self, not Pending %s, not another skill)", reqs, done.Name, pending.Name)
	}
}

// condition returns a true condition of the given type and message.
func condition(typ, msg string) metav1.Condition {
	return metav1.Condition{Type: typ, Status: metav1.ConditionTrue, Reason: "Test", Message: msg}
}

// TestOlderVersionDoesNotStealStable guards the loop the sibling watch would
// otherwise start: two equally good versions must not take stable from each
// other. The newer one keeps it.
func TestOlderVersionDoesNotStealStable(t *testing.T) {
	tests := []struct {
		name         string
		older, newer float64 // with-skill scores of v1.0.0 and v1.1.0
		wantStable   string
	}{
		{"equal scores: the newer keeps stable", 0.95, 0.95, digestB},
		{"older is a bit better, within tolerance: the newer keeps stable", 1.00, 0.95, digestB},
		{"older is clearly better: it takes stable back", 0.95, 0.80, digestA},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Config{MaxConcurrentJobs: 8, Thresholds: policy.DefaultThresholds()}, 200,
				map[string][]harbor.Artifact{"acme": {
					{Digest: digestA, Tags: []string{"v1.0.0", "stable"}},
					{Digest: digestB, Tags: []string{"v1.1.0"}},
				}})
			for _, m := range []models.Model{haiku, sonnet} {
				e.runWatched(e.create("acme", "v1.1.0", digestB, m), scoreWith(tt.newer, 0.36))
			}
			for _, m := range []models.Model{haiku, sonnet} {
				e.runWatched(e.create("acme", "v1.0.0", digestA, m), scoreWith(tt.older, 0.36))
			}
			if got := tags(t, e.harbor, "acme", tt.wantStable); !slices.Contains(got, "stable") {
				t.Errorf("tags of %s = %v, want stable", tt.wantStable[:10], got)
			}
		})
	}
}
