package telemetry

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name    string
		in      Config
		want    Config
		wantErr string
	}{
		{
			name: "host and port keep their settings",
			in:   Config{Endpoint: " collector:4318 "},
			want: Config{Endpoint: "collector:4318", Protocol: ProtocolHTTPProtobuf},
		},
		{
			name: "http URL turns TLS off",
			in:   Config{Endpoint: "http://otel-collector.monitoring:4318"},
			want: Config{Endpoint: "otel-collector.monitoring:4318", Protocol: ProtocolHTTPProtobuf, Insecure: true},
		},
		{
			name: "https URL keeps TLS",
			in:   Config{Endpoint: "https://otlp.example.com", Protocol: ProtocolGRPC},
			want: Config{Endpoint: "otlp.example.com", Protocol: ProtocolGRPC},
		},
		{
			name: "URL path is a base, as for OTEL_EXPORTER_OTLP_ENDPOINT",
			in:   Config{Endpoint: "http://prometheus:9090/api/v1/otlp/"},
			want: Config{Endpoint: "prometheus:9090", Protocol: ProtocolHTTPProtobuf, Insecure: true, URLPath: "/api/v1/otlp/v1/metrics"},
		},
		{
			name: "an explicit URL path wins over the URL",
			in:   Config{Endpoint: "http://prometheus:9090/ignored", URLPath: "/custom"},
			want: Config{Endpoint: "prometheus:9090", Protocol: ProtocolHTTPProtobuf, Insecure: true, URLPath: "/custom"},
		},
		{
			name: "grpc URL ignores the path",
			in:   Config{Endpoint: "http://collector:4317/", Protocol: ProtocolGRPC},
			want: Config{Endpoint: "collector:4317", Protocol: ProtocolGRPC, Insecure: true},
		},
		{name: "unknown protocol", in: Config{Endpoint: "c:4318", Protocol: "http/json"}, wantErr: "unsupported OTLP protocol"},
		{name: "URL path with grpc", in: Config{Endpoint: "c:4317", Protocol: ProtocolGRPC, URLPath: "/v1/metrics"}, wantErr: "http/protobuf only"},
		{name: "path without scheme", in: Config{Endpoint: "collector:4318/v1/metrics"}, wantErr: "use host:port"},
		{name: "unknown scheme", in: Config{Endpoint: "ftp://collector:4318"}, wantErr: "scheme must be http or https"},
		{name: "negative interval", in: Config{Endpoint: "c:4318", Interval: -time.Second}, wantErr: "must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in
			err := got.Normalize()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Normalize() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if got.Endpoint != tt.want.Endpoint || got.Protocol != tt.want.Protocol ||
				got.Insecure != tt.want.Insecure || got.URLPath != tt.want.URLPath {
				t.Errorf("Normalize() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEnabled(t *testing.T) {
	if (Config{Endpoint: "  "}).Enabled() {
		t.Error("a blank endpoint must disable the export")
	}
	if !(Config{Endpoint: "c:4318"}).Enabled() {
		t.Error("an endpoint must enable the export")
	}
}

func TestParseHeaders(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    map[string]string
		wantErr bool
	}{
		{name: "empty", raw: "", want: map[string]string{}},
		{name: "one", raw: "x-honeycomb-team=abc", want: map[string]string{"x-honeycomb-team": "abc"}},
		{name: "several with spaces", raw: " a = 1 , b=2,", want: map[string]string{"a": "1", "b": "2"}},
		{name: "value with = and URL encoding", raw: "Authorization=Api-Token%20x=y", want: map[string]string{"Authorization": "Api-Token x=y"}},
		{name: "missing value separator", raw: "novalue", wantErr: true},
		{name: "empty key", raw: "=v", wantErr: true},
		{name: "bad escape", raw: "k=%zz", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHeaders(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseHeaders(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ParseHeaders(%q) = %v, want %v", tt.raw, got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("ParseHeaders(%q)[%q] = %q, want %q", tt.raw, k, got[k], v)
				}
			}
		})
	}
}

// testRegistry holds one skillci_ gauge with labels, one skillci_ counter and a metric that must not be exported.
func testRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	lift := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "skillci_lift", Help: "lift"}, []string{"skill", "agent"})
	lift.WithLabelValues("acme-release-notes", "claude-code").Set(0.59)
	runs := prometheus.NewCounter(prometheus.CounterOpts{Name: "skillci_skillevals_total", Help: "runs"})
	runs.Add(3)
	other := prometheus.NewGauge(prometheus.GaugeOpts{Name: "workqueue_depth", Help: "not ours"})
	other.Set(7)
	reg.MustRegister(lift, runs, other)
	return reg
}

func TestPrefixGatherer(t *testing.T) {
	families, err := PrefixGatherer{Gatherer: testRegistry(), Prefix: MetricPrefix}.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range families {
		names = append(names, f.GetName())
	}
	if strings.Join(names, ",") != "skillci_lift,skillci_skillevals_total" {
		t.Errorf("gathered %v, want only the skillci_ families", names)
	}
}

// received collects what an OTLP receiver got.
type received struct {
	mu       sync.Mutex
	requests []*colmetricspb.ExportMetricsServiceRequest
	headers  []string
}

func (r *received) add(req *colmetricspb.ExportMetricsServiceRequest, header string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
	r.headers = append(r.headers, header)
}

type grpcReceiver struct {
	colmetricspb.UnimplementedMetricsServiceServer
	got *received
}

func (g grpcReceiver) Export(ctx context.Context, req *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	g.got.add(req, strings.Join(md.Get("x-api-key"), ""))
	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}

// startReceiver starts an OTLP receiver for protocol and returns its host:port.
func startReceiver(t *testing.T, protocol string, got *received) string {
	t.Helper()
	if protocol == ProtocolGRPC {
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := grpc.NewServer()
		colmetricspb.RegisterMetricsServiceServer(srv, grpcReceiver{got: got})
		go func() { _ = srv.Serve(lis) }()
		t.Cleanup(srv.Stop)
		return lis.Addr().String()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/otlp/v1/metrics" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req := &colmetricspb.ExportMetricsServiceRequest{}
		if err := proto.Unmarshal(body, req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		got.add(req, r.Header.Get("x-api-key"))
		out, _ := proto.Marshal(&colmetricspb.ExportMetricsServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestExportReachesReceiver(t *testing.T) {
	for _, protocol := range []string{ProtocolHTTPProtobuf, ProtocolGRPC} {
		t.Run(protocol, func(t *testing.T) {
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "k8s.namespace.name=skill-ci")
			got := &received{}
			cfg := Config{
				Endpoint: startReceiver(t, protocol, got), Protocol: protocol, Insecure: true,
				Headers: map[string]string{"x-api-key": "secret"}, Interval: time.Hour, ServiceVersion: "1.2.3",
			}
			if protocol == ProtocolHTTPProtobuf {
				cfg.URLPath = "/api/v1/otlp/v1/metrics"
			}
			if err := cfg.Normalize(); err != nil {
				t.Fatal(err)
			}
			gatherer := PrefixGatherer{Gatherer: testRegistry(), Prefix: MetricPrefix}
			exp, err := New(context.Background(), cfg, gatherer, logr.Discard())
			if err != nil {
				t.Fatal(err)
			}
			if exp.NeedLeaderElection() {
				t.Error("every replica must export")
			}
			// A stopped manager context makes Start flush once and return.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := exp.Start(ctx); err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			got.mu.Lock()
			defer got.mu.Unlock()
			if len(got.requests) == 0 {
				t.Fatal("the receiver got no export")
			}
			if got.headers[0] != "secret" {
				t.Errorf("x-api-key header = %q, want the configured header", got.headers[0])
			}
			rm := got.requests[0].GetResourceMetrics()[0]
			attrs := map[string]string{}
			for _, kv := range rm.GetResource().GetAttributes() {
				attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
			}
			for k, want := range map[string]string{
				"service.name": ServiceName, "service.version": "1.2.3", "k8s.namespace.name": "skill-ci",
			} {
				if attrs[k] != want {
					t.Errorf("resource attribute %s = %q, want %q", k, attrs[k], want)
				}
			}
			metrics := map[string]*metricspb.Metric{}
			for _, sm := range rm.GetScopeMetrics() {
				for _, m := range sm.GetMetrics() {
					metrics[m.GetName()] = m
				}
			}
			if _, ok := metrics["workqueue_depth"]; ok {
				t.Error("a metric without the skillci_ prefix was exported")
			}
			lift, ok := metrics["skillci_lift"]
			if !ok {
				t.Fatalf("skillci_lift missing, got %v", keys(metrics))
			}
			point := lift.GetGauge().GetDataPoints()[0]
			if point.GetAsDouble() != 0.59 {
				t.Errorf("skillci_lift = %v, want 0.59", point.GetAsDouble())
			}
			labels := map[string]string{}
			for _, kv := range point.GetAttributes() {
				labels[kv.GetKey()] = kv.GetValue().GetStringValue()
			}
			if labels["skill"] != "acme-release-notes" || labels["agent"] != "claude-code" {
				t.Errorf("skillci_lift labels = %v", labels)
			}
			runs, ok := metrics["skillci_skillevals_total"]
			if !ok || !runs.GetSum().GetIsMonotonic() || runs.GetSum().GetDataPoints()[0].GetAsDouble() != 3 {
				t.Errorf("skillci_skillevals_total = %v, want a monotonic sum of 3", runs)
			}
		})
	}
}

func keys(m map[string]*metricspb.Metric) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
