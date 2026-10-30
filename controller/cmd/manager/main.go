// Command manager runs the skill-ci controller: the Harbor webhook receiver,
// the SkillEval and model list reconcilers, and the metrics endpoint.
package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/controller"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/metrics"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/reportsite"
	"github.com/dirien/skill-ci/controller/internal/reportstore"
	"github.com/dirien/skill-ci/controller/internal/runner"
	"github.com/dirien/skill-ci/controller/internal/webhook"
)

// Build information, set with -ldflags "-X main.version=..." by GoReleaser.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// newReportSite builds the read-only reporting website over the manager's
// cached client, the Harbor API and the report referrers in the registry.
func newReportSite(mgr ctrl.Manager, addr, harborURL string, cfg controller.Config, registry harbor.Client) (*reportsite.Server, error) {
	reports, err := reportstore.New(reportstore.Options{
		RegistryURL: harborURL, Project: cfg.Project,
		Username: os.Getenv("HARBOR_USERNAME"), Password: os.Getenv("HARBOR_PASSWORD"),
	})
	if err != nil {
		return nil, fmt.Errorf("creating report store: %w", err)
	}
	site, err := reportsite.New(reportsite.Config{
		Addr: addr, Reader: mgr.GetClient(), Namespace: cfg.Namespace, Project: cfg.Project,
		Harbor: registry, Reports: reports, Thresholds: cfg.Thresholds, Log: ctrl.Log.WithName("reports"),
	})
	if err != nil {
		return nil, fmt.Errorf("creating reports site: %w", err)
	}
	return site, nil
}

func run() error {
	th := policy.DefaultThresholds()
	cfg := controller.Config{Thresholds: th}
	var (
		harborURL, runnerImage, modelsCM, webhookAddr, metricsAddr, reportsAddr string
		trialBudget                                                             int
		leaderElect                                                             bool
	)
	flag.StringVar(&cfg.Namespace, "namespace", "skill-ci", "Namespace for SkillEvals, Jobs and the model list.")
	flag.StringVar(&harborURL, "harbor-url", "http://harbor.harbor.svc.cluster.local", "Harbor base URL.")
	flag.StringVar(&cfg.Project, "harbor-project", "skills", "Harbor project that holds skills.")
	flag.StringVar(&runnerImage, "runner-image", "registry.digitalocean.com/skill-ci-dirien/skill-ci-runner:0.6.0", "Runner image.")
	flag.StringVar(&modelsCM, "models-configmap", "skill-ci-models", "ConfigMap with the model list (key models.yaml).")
	flag.IntVar(&cfg.Attempts, "default-attempts", 1, "Trials per arm for SkillEvals created by the controller.")
	flag.Float64Var(&th.Helps, "helps-threshold", th.Helps, "Lift at or above which a verdict is Helps.")
	flag.Float64Var(&th.Hurts, "hurts-threshold", th.Hurts, "Lift at or below which a verdict is Hurts.")
	flag.Float64Var(&th.Promote, "promote-threshold", th.Promote, "Lift every required model needs for the stable tag.")
	flag.Float64Var(&th.Demote, "demote-threshold", th.Demote, "Lift on any required model that removes the stable tag.")
	flag.Float64Var(&th.Obsolete, "obsolete-baseline", th.Obsolete, "Without-skill score at which a Neutral result is Obsolete.")
	flag.Float64Var(&th.Regression, "regression-tolerance", th.Regression, "With-skill drop below the stable version, per required model, that still allows promotion.")
	flag.Float64Var(&th.Confidence, "confidence", th.Confidence, "Confidence level of the intervals the gate uses (0 to 1, exclusive). A lift must clear zero, and a drop below stable must stay below zero, at this level.")
	flag.IntVar(&cfg.MaxConcurrentJobs, "max-concurrent-jobs", 4, "Running SkillEvals at once; the rest stay Pending.")
	flag.IntVar(&trialBudget, "trial-budget", 200, "Max trials (attempts x 2 arms) per controller lifetime; 0 is unlimited.")
	flag.IntVar(&cfg.MaxRetries, "max-retries", 1, "Fresh Jobs a run gets after an infrastructure failure (environment start timeout, lost connection, agent install, lost pod); 0 disables. Real results are never retried.")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "Mark SkillEvals Planned and create no Jobs.")
	flag.StringVar(&webhookAddr, "webhook-addr", ":8080", "Address for the webhook and health endpoints.")
	flag.StringVar(&metricsAddr, "metrics-addr", ":8081", "Address for the Prometheus metrics endpoint.")
	flag.StringVar(&reportsAddr, "reports-addr", ":8082", "Address for the reporting website; empty disables it.")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election (needed only with several replicas).")
	logOpts := zap.Options{Development: false}
	logOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	cfg.Thresholds = th
	if th.Confidence <= 0 || th.Confidence >= 1 {
		return fmt.Errorf("invalid --confidence %v: must be between 0 and 1", th.Confidence)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&logOpts)))
	setupLog := ctrl.Log.WithName("setup")
	setupLog.Info("starting skill-ci controller", "version", version, "commit", commit, "date", date)

	u, err := url.Parse(harborURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid --harbor-url %q", harborURL)
	}
	cfg.Runner = runner.Config{Image: runnerImage, Registry: u.Host, Project: cfg.Project}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	metrics.Register(ctrlmetrics.Registry)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Cache:                   cache.Options{DefaultNamespaces: map[string]cache.Config{cfg.Namespace: {}}},
		Metrics:                 metricsserver.Options{BindAddress: metricsAddr}, // plain HTTP on :8081/metrics
		HealthProbeBindAddress:  "0",                                             // probes are served by the webhook server
		LeaderElection:          leaderElect,
		LeaderElectionID:        "skill-ci-controller",
		LeaderElectionNamespace: cfg.Namespace,
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	registry := harbor.New(harborURL, cfg.Project, os.Getenv("HARBOR_USERNAME"), os.Getenv("HARBOR_PASSWORD"))

	if err := (&controller.SkillEvalReconciler{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Harbor: registry,
		Recorder: mgr.GetEventRecorder("skill-ci-controller"), Config: cfg, Budget: controller.NewBudget(trialBudget),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("registering skilleval controller: %w", err)
	}
	if err := (&controller.ModelsReconciler{
		Client: mgr.GetClient(), Harbor: registry, ConfigMap: modelsCM, Config: cfg,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("registering models controller: %w", err)
	}

	if err := mgr.Add(&controller.PhaseGauge{Reader: mgr.GetClient(), Namespace: cfg.Namespace}); err != nil {
		return fmt.Errorf("adding phase gauge: %w", err)
	}

	hook := &webhook.Handler{
		Client: mgr.GetClient(), Namespace: cfg.Namespace, Project: cfg.Project, ModelsConfigMap: modelsCM,
		Token: os.Getenv("WEBHOOK_TOKEN"), Attempts: cfg.Attempts, Log: ctrl.Log.WithName("webhook"),
	}
	if err := mgr.Add(webhook.NewServer(webhookAddr, hook)); err != nil {
		return fmt.Errorf("adding webhook server: %w", err)
	}

	if reportsAddr != "" {
		site, err := newReportSite(mgr, reportsAddr, harborURL, cfg, registry)
		if err != nil {
			return err
		}
		if err := mgr.Add(site); err != nil {
			return fmt.Errorf("adding reports site: %w", err)
		}
	}

	setupLog.Info("starting manager", "namespace", cfg.Namespace, "dryRun", cfg.DryRun, "runnerImage", runnerImage)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("running manager: %w", err)
	}
	return nil
}
