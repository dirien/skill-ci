// Package controller reconciles SkillEvals and the model list.
package controller

import (
	"sync"

	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/runner"
)

// Config holds the settings both reconcilers share.
type Config struct {
	// Namespace is where SkillEvals, Jobs and the model list live.
	Namespace string
	// Project is the Harbor project that holds skills.
	Project string
	// Runner says how Jobs are rendered.
	Runner runner.Config
	// Attempts is the trials per arm for SkillEvals the controller creates.
	Attempts int
	// MaxConcurrentJobs caps Running SkillEvals; extra ones stay Pending.
	MaxConcurrentJobs int
	// MaxRetries is how many times a run that failed for an infrastructure
	// reason is started again with a fresh Job; zero disables retries.
	MaxRetries int
	// DryRun records SkillEvals as Planned and creates no Jobs.
	DryRun bool
	// Thresholds are the verdict and tag policy bands.
	Thresholds policy.Thresholds
}

// Budget caps the trials the controller starts during its lifetime.
type Budget struct {
	mu        sync.Mutex
	remaining int
	unlimited bool
}

// NewBudget returns a budget of n trials. Zero or less means unlimited.
func NewBudget(n int) *Budget {
	return &Budget{remaining: n, unlimited: n <= 0}
}

// Spend takes n trials from the budget and reports whether they were available.
func (b *Budget) Spend(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unlimited {
		return true
	}
	if n > b.remaining {
		return false
	}
	b.remaining -= n
	return true
}

// Refund gives n trials back, for example after a Job failed to start.
func (b *Budget) Refund(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.remaining += n
}
