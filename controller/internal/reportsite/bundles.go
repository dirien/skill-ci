package reportsite

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dirien/skill-ci/controller/internal/reportsite/apiv1"
	"github.com/dirien/skill-ci/controller/internal/reportstore"
	"github.com/dirien/skill-ci/controller/internal/session"
)

const (
	// listAnnotationsTimeout bounds the registry reads a list response waits
	// for; runs not read by then show trials as unknown.
	listAnnotationsTimeout = 3 * time.Second
	// listAnnotationsWorkers is how many report manifests a list reads at once.
	listAnnotationsWorkers = 8
)

// Reasons in skillci.dev/trials-withheld that mean the run has no trials to
// keep, rather than trials the runner held back.
const (
	withheldNoTrials = "no-trials"
	withheldDisabled = "disabled"
)

// bundleRef is what a report referrer says about its trial bundle.
type bundleRef struct {
	// state is a Trials constant: stored, withheld, none or unknown.
	state string
	// digest is the bundle digest (stored).
	digest string
	// reason is the skillci.dev/trials-withheld value (withheld, or none for no-trials and disabled).
	reason string
	// gone reports that the report manifest is no longer in the registry.
	gone bool
	// err is why the manifest could not be read (unknown).
	err error
}

// bundleOf reads the report manifest of reportDigest and returns its bundle
// reference. The digest always comes from SkillEval status, never from a request.
func (s *Server) bundleOf(ctx context.Context, skill, reportDigest string) bundleRef {
	if reportDigest == "" {
		return bundleRef{state: apiv1.TrialsNone}
	}
	if s.cfg.Bundles == nil {
		return bundleRef{state: apiv1.TrialsUnknown, err: errors.New("this controller reads no trial bundles")}
	}
	if !digestPattern.MatchString(reportDigest) {
		return bundleRef{state: apiv1.TrialsUnknown, err: fmt.Errorf("status.reportDigest %q is not a sha256 digest", reportDigest)}
	}
	if ref, ok := s.bundles.get(reportDigest); ok {
		return ref
	}
	ann, err := s.cfg.Bundles.Annotations(ctx, skill, reportDigest)
	switch {
	case errors.Is(err, reportstore.ErrNotFound):
		return bundleRef{state: apiv1.TrialsUnknown, gone: true, err: err}
	case err != nil:
		return bundleRef{state: apiv1.TrialsUnknown, err: err}
	}
	ref := refFromAnnotations(ann)
	s.bundles.put(reportDigest, ref)
	return ref
}

// refFromAnnotations reads skillci.dev/trials and skillci.dev/trials-withheld.
// A report without either comes from a runner before trial bundles.
func refFromAnnotations(ann map[string]string) bundleRef {
	if d, ok := ann[session.AnnotationTrials]; ok {
		if !digestPattern.MatchString(d) {
			return bundleRef{state: apiv1.TrialsUnknown, err: fmt.Errorf("the report names an invalid trial bundle digest %q", d)}
		}
		return bundleRef{state: apiv1.TrialsStored, digest: d}
	}
	if reason, ok := ann[session.AnnotationTrialsWithheld]; ok {
		if reason == withheldNoTrials || reason == withheldDisabled {
			return bundleRef{state: apiv1.TrialsNone, reason: reason}
		}
		return bundleRef{state: apiv1.TrialsWithheld, reason: reason}
	}
	return bundleRef{state: apiv1.TrialsNone}
}

// trialStates returns the bundle reference of every run, read concurrently
// from the report manifests of the runs with a report. A manifest not read
// within listAnnotationsTimeout leaves the run unknown.
func (s *Server) trialStates(ctx context.Context, runs []Run) map[string]bundleRef {
	out := make(map[string]bundleRef, len(runs))
	var mu sync.Mutex
	ctx, cancel := context.WithTimeout(ctx, listAnnotationsTimeout)
	defer cancel()
	jobs := make(chan Run)
	var wg sync.WaitGroup
	for range listAnnotationsWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range jobs {
				ref := s.bundleOf(ctx, r.Skill, r.ReportDigest)
				mu.Lock()
				out[r.Name] = ref
				mu.Unlock()
			}
		}()
	}
	for _, r := range runs {
		if r.ReportDigest == "" {
			mu.Lock()
			out[r.Name] = bundleRef{state: apiv1.TrialsNone}
			mu.Unlock()
			continue
		}
		if ref, ok := s.bundles.get(r.ReportDigest); ok {
			mu.Lock()
			out[r.Name] = ref
			mu.Unlock()
			continue
		}
		jobs <- r
	}
	close(jobs)
	wg.Wait()
	return out
}

// withheldText says why the runner kept a bundle back, in a sentence.
func withheldText(reason string) string {
	switch reason {
	case "secret-scan":
		return "the secret scan found a value in the index after redaction, so no trial was stored"
	case "size":
		return "the bundle was over its size limit"
	case "build-failed":
		return "the bundle could not be built"
	case "attach-failed":
		return "the bundle could not be attached to the registry"
	case withheldNoTrials:
		return "Tier 3 produced no trials to store"
	case withheldDisabled:
		return "trial bundles were turned off for this run (TRIAL_BUNDLE=off)"
	default:
		return "of \"" + reason + "\""
	}
}
