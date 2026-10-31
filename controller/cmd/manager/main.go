// Command manager runs the skill-ci controller: the Harbor webhook receiver,
// the SkillEval and model list reconcilers, and the metrics endpoint.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

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
	"github.com/dirien/skill-ci/controller/internal/crds"
	"github.com/dirien/skill-ci/controller/internal/harbor"
	"github.com/dirien/skill-ci/controller/internal/metrics"
	"github.com/dirien/skill-ci/controller/internal/network"
	"github.com/dirien/skill-ci/controller/internal/policy"
	"github.com/dirien/skill-ci/controller/internal/reportsite"
	"github.com/dirien/skill-ci/controller/internal/reportstore"
	"github.com/dirien/skill-ci/controller/internal/runner"
	"github.com/dirien/skill-ci/controller/internal/telemetry"
	"github.com/dirien/skill-ci/controller/internal/webhook"
)

// Build information, set with -ldflags "-X main.version=..." by GoReleaser.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// defaultImage returns the GHCR image of this release, for example
// ghcr.io/dirien/skill-ci-runner:0.7.0, or :latest for a build without a version.
func defaultImage(name string) string {
	tag := strings.TrimPrefix(version, "v")
	if tag == "" || tag == "dev" {
		tag = "latest"
	}
	return "ghcr.io/dirien/" + name + ":" + tag
}

// envBool reads a boolean environment variable; anything unparsable is false.
func envBool(name string) bool {
	v, _ := strconv.ParseBool(os.Getenv(name))
	return v
}

// otlpFlags holds the OTLP exporter flags until they are parsed into a telemetry.Config.
type otlpFlags struct {
	cfg     telemetry.Config
	headers string
}

// bindOTLPFlags registers the OTLP flags. Each one defaults to its OTEL_* environment
// variable, so the usual OpenTelemetry settings work without flags.
func bindOTLPFlags(f *otlpFlags) {
	flag.StringVar(&f.cfg.Endpoint, "otlp-endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		"OTLP endpoint for the skillci_* metrics, host:port or a URL (http:// disables TLS); empty disables the export. Env OTEL_EXPORTER_OTLP_ENDPOINT.")
	flag.StringVar(&f.cfg.Protocol, "otlp-protocol", os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"),
		"OTLP protocol: http/protobuf (default) or grpc. Env OTEL_EXPORTER_OTLP_PROTOCOL.")
	flag.StringVar(&f.cfg.URLPath, "otlp-url-path", os.Getenv("OTEL_EXPORTER_OTLP_METRICS_URL_PATH"),
		"Full OTLP/HTTP metrics path, e.g. /api/v1/otlp/v1/metrics for the Prometheus OTLP receiver. Env OTEL_EXPORTER_OTLP_METRICS_URL_PATH.")
	flag.BoolVar(&f.cfg.Insecure, "otlp-insecure", envBool("OTEL_EXPORTER_OTLP_INSECURE"),
		"Disable TLS for the OTLP endpoint. Env OTEL_EXPORTER_OTLP_INSECURE.")
	flag.StringVar(&f.headers, "otlp-headers", os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"),
		"Comma separated key=value headers for every export, e.g. an API key. Env OTEL_EXPORTER_OTLP_HEADERS.")
	flag.DurationVar(&f.cfg.Interval, "otlp-interval", 0,
		"Time between exports; 0 keeps the SDK default (OTEL_METRIC_EXPORT_INTERVAL in milliseconds, else 60s).")
}

// addOTLPExporter starts pushing the skillci_* metrics when an OTLP endpoint is set.
func addOTLPExporter(mgr ctrl.Manager, f otlpFlags) error {
	cfg := f.cfg
	if !cfg.Enabled() {
		return nil
	}
	headers, err := telemetry.ParseHeaders(f.headers)
	if err != nil {
		return fmt.Errorf("invalid --otlp-headers: %w", err)
	}
	cfg.Headers = headers
	cfg.ServiceVersion = version
	if err := cfg.Normalize(); err != nil {
		return fmt.Errorf("invalid OTLP settings: %w", err)
	}
	log := ctrl.Log.WithName("otlp")
	gatherer := telemetry.PrefixGatherer{Gatherer: ctrlmetrics.Registry, Prefix: telemetry.MetricPrefix}
	exp, err := telemetry.New(context.Background(), cfg, gatherer, log)
	if err != nil {
		return fmt.Errorf("creating OTLP exporter: %w", err)
	}
	if err := mgr.Add(exp); err != nil {
		return fmt.Errorf("adding OTLP exporter: %w", err)
	}
	interval := "SDK default"
	if cfg.Interval > 0 {
		interval = cfg.Interval.Round(time.Millisecond).String()
	}
	log.Info("exporting metrics over OTLP", "endpoint", cfg.Endpoint, "protocol", cfg.Protocol,
		"urlPath", cfg.URLPath, "insecure", cfg.Insecure, "headers", len(cfg.Headers), "interval", interval)
	return nil
}

// networkFlags holds the --network-* flags until they are parsed into a network.Policy.
type networkFlags struct {
	def, modes, enforcement, alwaysAllow, bundles, preinstalled, installHosts string
}

// bindNetworkFlags registers the network mode flags.
func bindNetworkFlags(f *networkFlags) {
	flag.StringVar(&f.def, "network-default", "full", "Network mode of a skill that declares none: none, allowlist or full.")
	flag.StringVar(&f.modes, "network-modes", "none,allowlist,full", "Comma separated network modes a skill may declare; a run with another mode fails.")
	flag.StringVar(&f.enforcement, "network-enforcement", string(network.EnforcementAuto),
		"auto: enforce with CiliumNetworkPolicies when the ciliumnetworkpolicies.cilium.io CRD exists; cilium: require Cilium; off: no policies.")
	flag.StringVar(&f.alwaysAllow, "network-always-allow", "", "Comma separated hosts added to the none and allowlist modes, for example a proxy.")
	flag.StringVar(&f.bundles, "network-bundles", network.FormatBundles(network.DefaultBundles),
		"Host bundles a skill names as @<bundle>: name=host,host;name=host.")
	flag.StringVar(&f.preinstalled, "preinstalled-agents", strings.Join(network.DefaultPreinstalledAgents, ","),
		"Comma separated agent@version pins in the sandbox image; another agent version adds --agent-install-hosts to none and allowlist.")
	flag.StringVar(&f.installHosts, "agent-install-hosts", strings.Join(network.DefaultAgentInstallHosts, ","),
		"Comma separated hosts an agent install needs (Harbor installs the CLI with npm).")
}

// policy parses the network flags.
func (f networkFlags) policy() (network.Policy, error) {
	modes, err := network.ParseModes(f.modes)
	if err != nil {
		return network.Policy{}, fmt.Errorf("invalid --network-modes: %w", err)
	}
	bundles, err := network.ParseBundles(f.bundles)
	if err != nil {
		return network.Policy{}, fmt.Errorf("invalid --network-bundles: %w", err)
	}
	p := network.Policy{
		Default: strings.TrimSpace(f.def), Allowed: modes, Enforcement: network.Enforcement(strings.TrimSpace(f.enforcement)),
		AlwaysAllow: network.SplitList(f.alwaysAllow), Bundles: bundles,
		Preinstalled: network.SplitList(f.preinstalled), InstallHosts: network.SplitList(f.installHosts),
	}
	if err := p.Validate(); err != nil {
		return network.Policy{}, fmt.Errorf("invalid network flags: %w", err)
	}
	return p, nil
}

// setupSandboxProfiles registers the SandboxProfile reconciler and the
// baseline network policy. Without the SandboxProfile CRD (an upgrade that
// did not apply the new CRDs) it registers nothing and runs fall back to a
// bare Sandbox per trial.
func setupSandboxProfiles(mgr ctrl.Manager, detector crds.Checker, cfg controller.Config) error {
	log := ctrl.Log.WithName("setup")
	ctx := context.Background()
	if err := mgr.Add(&controller.BaselinePolicy{Client: mgr.GetClient(), CRDs: detector, Namespace: cfg.Namespace, Network: cfg.Network}); err != nil {
		return fmt.Errorf("adding baseline network policy: %w", err)
	}
	present, err := detector.Has(ctx, sandboxProfileCRD)
	if err != nil {
		// Most likely the ClusterRole that reads CRDs is missing; runs still work.
		log.Error(err, "detecting the SandboxProfile CRD, profiles are not reconciled", "crd", sandboxProfileCRD)
		return nil
	}
	if !present {
		log.Info("SandboxProfile CRD not found: profiles are not reconciled and runs start a bare Sandbox per trial", "crd", sandboxProfileCRD)
		return nil
	}
	if err := (&controller.SandboxProfileReconciler{
		Client: mgr.GetClient(), CRDs: detector, Config: cfg,
	}).SetupWithManager(ctx, mgr); err != nil {
		return fmt.Errorf("registering sandboxprofile controller: %w", err)
	}
	return nil
}

// sandboxProfileCRD is the CRD of the SandboxProfile kind.
const sandboxProfileCRD = "sandboxprofiles.skillci.dev"

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
		harborURL, runnerImage, sandboxImage, pullSecrets, modelsCM, webhookAddr, metricsAddr, reportsAddr string
		trialBudget                                                                                        int
		leaderElect                                                                                        bool
		otlp                                                                                               otlpFlags
		netFlags                                                                                           networkFlags
	)
	flag.StringVar(&cfg.Namespace, "namespace", "skill-ci", "Namespace for SkillEvals, Jobs and the model list.")
	flag.StringVar(&harborURL, "harbor-url", "http://harbor.harbor.svc.cluster.local", "Harbor base URL.")
	flag.StringVar(&cfg.Project, "harbor-project", "skills", "Harbor project that holds skills.")
	flag.StringVar(&runnerImage, "runner-image", defaultImage("skill-ci-runner"), "Runner image.")
	flag.StringVar(&sandboxImage, "sandbox-image", defaultImage("skill-ci-sandbox"), "Generic sandbox image the runner starts every trial from (SANDBOX_IMAGE); empty leaves the runner's own default.")
	flag.StringVar(&pullSecrets, "image-pull-secrets", "", "Comma separated names of image pull Secrets for the runner image and the sandbox image; empty when the images are public.")
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
	flag.StringVar(&cfg.DefaultSandboxProfile, "default-sandbox-profile", "default",
		"SandboxProfile of a model list entry without sandboxProfile; when it does not exist, runs start a bare Sandbox per trial.")
	bindNetworkFlags(&netFlags)
	bindOTLPFlags(&otlp)
	logOpts := zap.Options{Development: false}
	logOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	cfg.Thresholds = th
	if th.Confidence <= 0 || th.Confidence >= 1 {
		return fmt.Errorf("invalid --confidence %v: must be between 0 and 1", th.Confidence)
	}
	netPolicy, err := netFlags.policy()
	if err != nil {
		return err
	}
	cfg.Network = netPolicy

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&logOpts)))
	setupLog := ctrl.Log.WithName("setup")
	setupLog.Info("starting skill-ci controller", "version", version, "commit", commit, "date", date)

	u, err := url.Parse(harborURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid --harbor-url %q", harborURL)
	}
	secrets, err := runner.ParsePullSecrets(pullSecrets)
	if err != nil {
		return fmt.Errorf("invalid --image-pull-secrets: %w", err)
	}
	cfg.Runner = runner.Config{
		Image: runnerImage, SandboxImage: sandboxImage, ImagePullSecrets: secrets,
		Registry: u.Host, Project: cfg.Project,
	}

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
	detector := &crds.Detector{Reader: mgr.GetAPIReader()}

	if err := (&controller.SkillEvalReconciler{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Harbor: registry,
		Recorder: mgr.GetEventRecorder("skill-ci-controller"), Config: cfg, Budget: controller.NewBudget(trialBudget),
		CRDs: detector,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("registering skilleval controller: %w", err)
	}
	if err := (&controller.ModelsReconciler{
		Client: mgr.GetClient(), Harbor: registry, ConfigMap: modelsCM, Config: cfg,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("registering models controller: %w", err)
	}

	if err := setupSandboxProfiles(mgr, detector, cfg); err != nil {
		return err
	}

	if err := mgr.Add(&controller.PhaseGauge{Reader: mgr.GetClient(), Namespace: cfg.Namespace}); err != nil {
		return fmt.Errorf("adding phase gauge: %w", err)
	}
	if err := addOTLPExporter(mgr, otlp); err != nil {
		return err
	}

	hook := &webhook.Handler{
		Client: mgr.GetClient(), Namespace: cfg.Namespace, Project: cfg.Project, ModelsConfigMap: modelsCM,
		Token: os.Getenv("WEBHOOK_TOKEN"), Attempts: cfg.Attempts, DefaultSandboxProfile: cfg.DefaultSandboxProfile,
		Log: ctrl.Log.WithName("webhook"),
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

	setupLog.Info("starting manager", "namespace", cfg.Namespace, "dryRun", cfg.DryRun, "runnerImage", runnerImage, "sandboxImage", sandboxImage,
		"networkDefault", cfg.Network.DefaultMode(), "networkModes", cfg.Network.AllowedModes(), "networkEnforcement", cfg.Network.EnforcementMode(),
		"defaultSandboxProfile", cfg.DefaultSandboxProfile)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("running manager: %w", err)
	}
	return nil
}
