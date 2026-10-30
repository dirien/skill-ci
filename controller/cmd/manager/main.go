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
	"github.com/dirien/skill-ci/controller/internal/runner"
	"github.com/dirien/skill-ci/controller/internal/webhook"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	th := policy.DefaultThresholds()
	cfg := controller.Config{Thresholds: th}
	var (
		harborURL, runnerImage, modelsCM, webhookAddr, metricsAddr string
		trialBudget                                                int
		leaderElect                                                bool
	)
	flag.StringVar(&cfg.Namespace, "namespace", "skill-ci", "Namespace for SkillEvals, Jobs and the model list.")
	flag.StringVar(&harborURL, "harbor-url", "http://harbor.harbor.svc.cluster.local", "Harbor base URL.")
	flag.StringVar(&cfg.Project, "harbor-project", "skills", "Harbor project that holds skills.")
	flag.StringVar(&runnerImage, "runner-image", "registry.digitalocean.com/skill-ci-dirien/skill-ci-runner:0.2.1", "Runner image.")
	flag.StringVar(&modelsCM, "models-configmap", "skill-ci-models", "ConfigMap with the model list (key models.yaml).")
	flag.IntVar(&cfg.Attempts, "default-attempts", 1, "Trials per arm for SkillEvals created by the controller.")
	flag.Float64Var(&th.Helps, "helps-threshold", th.Helps, "Lift at or above which a verdict is Helps.")
	flag.Float64Var(&th.Hurts, "hurts-threshold", th.Hurts, "Lift at or below which a verdict is Hurts.")
	flag.Float64Var(&th.Promote, "promote-threshold", th.Promote, "Lift every required model needs for the stable tag.")
	flag.Float64Var(&th.Demote, "demote-threshold", th.Demote, "Lift on any required model that removes the stable tag.")
	flag.Float64Var(&th.Obsolete, "obsolete-baseline", th.Obsolete, "Without-skill score at which a Neutral result is Obsolete.")
	flag.IntVar(&cfg.MaxConcurrentJobs, "max-concurrent-jobs", 4, "Running SkillEvals at once; the rest stay Pending.")
	flag.IntVar(&trialBudget, "trial-budget", 200, "Max trials (attempts x 2 arms) per controller lifetime; 0 is unlimited.")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "Mark SkillEvals Planned and create no Jobs.")
	flag.StringVar(&webhookAddr, "webhook-addr", ":8080", "Address for the webhook and health endpoints.")
	flag.StringVar(&metricsAddr, "metrics-addr", ":8081", "Address for the Prometheus metrics endpoint.")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election (needed only with several replicas).")
	logOpts := zap.Options{Development: false}
	logOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	cfg.Thresholds = th

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&logOpts)))
	setupLog := ctrl.Log.WithName("setup")

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

	hook := &webhook.Handler{
		Client: mgr.GetClient(), Namespace: cfg.Namespace, Project: cfg.Project, ModelsConfigMap: modelsCM,
		Token: os.Getenv("WEBHOOK_TOKEN"), Attempts: cfg.Attempts, Log: ctrl.Log.WithName("webhook"),
	}
	if err := mgr.Add(webhook.NewServer(webhookAddr, hook)); err != nil {
		return fmt.Errorf("adding webhook server: %w", err)
	}

	setupLog.Info("starting manager", "namespace", cfg.Namespace, "dryRun", cfg.DryRun, "runnerImage", runnerImage)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("running manager: %w", err)
	}
	return nil
}
