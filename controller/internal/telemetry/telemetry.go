// Package telemetry pushes the controller's skillci_* metrics to an OpenTelemetry
// (OTLP) endpoint, next to the Prometheus scrape endpoint.
//
// The metrics stay Prometheus collectors (internal/metrics); a Prometheus bridge
// reads them at every export, so both paths carry the same names, labels and values.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	promBridge "go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Protocols the exporter speaks, as in OTEL_EXPORTER_OTLP_PROTOCOL.
const (
	ProtocolHTTPProtobuf = "http/protobuf"
	ProtocolGRPC         = "grpc"
)

// ServiceName is the service.name resource attribute; OTEL_SERVICE_NAME overrides it.
const ServiceName = "skill-ci-controller"

// MetricPrefix selects the metric families that are exported: the controller's
// own, not the Go runtime or controller-runtime internals of the scrape endpoint.
const MetricPrefix = "skillci_"

// defaultURLPath is the OTLP/HTTP metrics path the SDK uses without a URL path.
const defaultURLPath = "/v1/metrics"

// shutdownTimeout bounds the final flush when the manager stops.
const shutdownTimeout = 10 * time.Second

// Config is the OTLP exporter configuration. An empty Endpoint disables the export.
type Config struct {
	// Endpoint is host:port, or a URL (http:// turns TLS off, a path is the
	// base the metrics path is appended to, as for OTEL_EXPORTER_OTLP_ENDPOINT).
	Endpoint string
	// Protocol is http/protobuf or grpc.
	Protocol string
	// URLPath is the full metrics path for http/protobuf, for example
	// /api/v1/otlp/v1/metrics for the Prometheus OTLP receiver.
	URLPath string
	// Insecure disables TLS.
	Insecure bool
	// Headers are sent with every export, for example an API key.
	Headers map[string]string
	// Interval between exports; zero leaves the SDK default
	// (OTEL_METRIC_EXPORT_INTERVAL, else 60s).
	Interval time.Duration
	// ServiceVersion is the service.version resource attribute.
	ServiceVersion string
}

// Enabled reports whether an endpoint is configured.
func (c Config) Enabled() bool { return strings.TrimSpace(c.Endpoint) != "" }

// Normalize validates the configuration and resolves a URL endpoint into
// host:port, TLS and URL path.
func (c *Config) Normalize() error {
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Protocol = strings.TrimSpace(c.Protocol)
	if c.Protocol == "" {
		c.Protocol = ProtocolHTTPProtobuf
	}
	if c.Protocol != ProtocolHTTPProtobuf && c.Protocol != ProtocolGRPC {
		return fmt.Errorf("unsupported OTLP protocol %q (must be %s or %s)", c.Protocol, ProtocolHTTPProtobuf, ProtocolGRPC)
	}
	if c.Interval < 0 {
		return fmt.Errorf("OTLP export interval must not be negative, got %s", c.Interval)
	}
	if c.URLPath != "" && c.Protocol == ProtocolGRPC {
		return errors.New("an OTLP URL path applies to http/protobuf only")
	}
	if !strings.Contains(c.Endpoint, "://") {
		if c.Endpoint != "" && strings.Contains(c.Endpoint, "/") {
			return fmt.Errorf("OTLP endpoint %q: use host:port, or a URL with a scheme", c.Endpoint)
		}
		return nil
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return fmt.Errorf("parsing OTLP endpoint %q: %w", c.Endpoint, err)
	}
	switch u.Scheme {
	case "http":
		c.Insecure = true
	case "https":
	default:
		return fmt.Errorf("OTLP endpoint %q: scheme must be http or https", c.Endpoint)
	}
	if u.Host == "" {
		return fmt.Errorf("OTLP endpoint %q has no host", c.Endpoint)
	}
	c.Endpoint = u.Host
	if base := strings.TrimSuffix(u.Path, "/"); base != "" && c.URLPath == "" && c.Protocol == ProtocolHTTPProtobuf {
		c.URLPath = base + defaultURLPath
	}
	return nil
}

// ParseHeaders parses comma separated key=value pairs, as in OTEL_EXPORTER_OTLP_HEADERS.
// Values may be URL encoded.
func ParseHeaders(raw string) (map[string]string, error) {
	headers := map[string]string{}
	for pair := range strings.SplitSeq(raw, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("OTLP header %q is not key=value", strings.TrimSpace(pair))
		}
		decoded, err := url.QueryUnescape(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("OTLP header %q: %w", key, err)
		}
		headers[key] = decoded
	}
	return headers, nil
}

// PrefixGatherer returns only the metric families whose name starts with Prefix.
type PrefixGatherer struct {
	Gatherer prometheus.Gatherer
	Prefix   string
}

// Gather implements prometheus.Gatherer.
func (g PrefixGatherer) Gather() ([]*dto.MetricFamily, error) {
	families, err := g.Gatherer.Gather()
	kept := families[:0]
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), g.Prefix) {
			kept = append(kept, f)
		}
	}
	if err != nil {
		return kept, fmt.Errorf("gathering metrics: %w", err)
	}
	return kept, nil
}

// Exporter pushes the gathered metrics on an interval. It is a manager.Runnable:
// Start blocks until the context ends, then flushes and shuts the exporter down.
type Exporter struct {
	provider *sdkmetric.MeterProvider
	log      logr.Logger
}

// New builds the OTLP exporter for cfg (normalized) over gatherer.
func New(ctx context.Context, cfg Config, gatherer prometheus.Gatherer, log logr.Logger) (*Exporter, error) {
	exp, err := newMetricExporter(ctx, cfg)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx,
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(semconv.ServiceName(ServiceName), semconv.ServiceVersion(cfg.ServiceVersion)),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(), // OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES win
	)
	if err != nil {
		return nil, fmt.Errorf("creating OTel resource: %w", err)
	}
	readerOpts := []sdkmetric.PeriodicReaderOption{
		sdkmetric.WithProducer(promBridge.NewMetricProducer(promBridge.WithGatherer(gatherer))),
	}
	if cfg.Interval > 0 {
		readerOpts = append(readerOpts, sdkmetric.WithInterval(cfg.Interval))
	}
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Error(err, "OpenTelemetry export failed")
	}))
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, readerOpts...)),
	)
	return &Exporter{provider: provider, log: log}, nil
}

func newMetricExporter(ctx context.Context, cfg Config) (sdkmetric.Exporter, error) {
	switch cfg.Protocol {
	case ProtocolGRPC:
		opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlpmetricgrpc.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlpmetricgrpc.WithHeaders(cfg.Headers))
		}
		exp, err := otlpmetricgrpc.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("creating OTLP gRPC exporter: %w", err)
		}
		return exp, nil
	case ProtocolHTTPProtobuf:
		opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlpmetrichttp.WithInsecure())
		}
		if cfg.URLPath != "" {
			opts = append(opts, otlpmetrichttp.WithURLPath(cfg.URLPath))
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlpmetrichttp.WithHeaders(cfg.Headers))
		}
		exp, err := otlpmetrichttp.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("creating OTLP HTTP exporter: %w", err)
		}
		return exp, nil
	default:
		return nil, fmt.Errorf("unsupported OTLP protocol %q", cfg.Protocol)
	}
}

// Start implements manager.Runnable.
func (e *Exporter) Start(ctx context.Context) error {
	<-ctx.Done()
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := e.provider.Shutdown(flushCtx); err != nil {
		return fmt.Errorf("shutting down the OTLP exporter: %w", err)
	}
	e.log.Info("OTLP exporter flushed and stopped")
	return nil
}

// NeedLeaderElection implements manager.LeaderElectionRunnable: every replica
// exports, the same as every replica serves /metrics.
func (e *Exporter) NeedLeaderElection() bool { return false }
