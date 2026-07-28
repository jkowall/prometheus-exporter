package webhook

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// TestEndToEndOverHTTP wires the receiver and the /metrics endpoint onto one
// registry behind a real HTTP server, posts a signed delivery the way Spacelift
// would, and scrapes the result.
//
// The unit tests above assert against the registry directly; this one proves the
// whole path, including that the metrics actually render in the exposition
// format a Prometheus server will read.
func TestEndToEndOverHTTP(t *testing.T) {
	registry := prometheus.NewRegistry()

	handler, err := NewHandler(context.Background(), NewMetrics(registry, nil), testSecret)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/webhook", handler)
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{EnableOpenMetrics: true}))

	server := httptest.NewServer(mux)
	defer server.Close()

	body := fixture(t, "terminal-run")

	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost, server.URL+"/webhook", bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(SignatureHeader, Sign(body, testSecret))

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("posting delivery: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("delivery returned %d: %s", response.StatusCode, payload)
	}

	scraped := scrape(t, server)

	for _, want := range []string{
		`spacelift_runs_total{drift_detection="false",final_state="FINISHED",run_type="TRACKED",space="production",stack="acme-production-network",worker_pool="production"} 1`,
		`spacelift_run_duration_seconds_sum{run_type="TRACKED",space="production",stack="acme-production-network"} 187`,
		`spacelift_run_resource_changes_total{change_type="replaced",run_type="TRACKED",space="production",stack="acme-production-network"} 2`,
		`spacelift_run_policy_evaluations_total{policy_outcome="allow",policy_type="PLAN",space="production",stack="acme-production-network"} 1`,
		`spacelift_webhook_deliveries_total{result="accepted"} 1`,
		`spacelift_webhook_last_delivery_timestamp_seconds `,
	} {
		if !strings.Contains(scraped, want) {
			t.Errorf("scraped output is missing %q\n\n%s", want, scraped)
		}
	}

	// The run took 187s, so it belongs in the 300s bucket but not the 120s
	// one. This is what makes the non-default buckets worth having: with the
	// Prometheus defaults every IaC run lands in +Inf.
	for _, want := range []string{
		`spacelift_run_duration_seconds_bucket{run_type="TRACKED",space="production",stack="acme-production-network",le="120"} 0`,
		`spacelift_run_duration_seconds_bucket{run_type="TRACKED",space="production",stack="acme-production-network",le="300"} 1`,
	} {
		if !strings.Contains(scraped, want) {
			t.Errorf("scraped output is missing %q\n\n%s", want, scraped)
		}
	}
}

// TestUnsignedDeliveryIsRejectedOverHTTP is the security assertion at the
// transport level: an unsigned POST must not be able to create series.
func TestUnsignedDeliveryIsRejectedOverHTTP(t *testing.T) {
	registry := prometheus.NewRegistry()

	handler, err := NewHandler(context.Background(), NewMetrics(registry, nil), testSecret)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/webhook", handler)
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	server := httptest.NewServer(mux)
	defer server.Close()

	response, err := server.Client().Post(
		server.URL+"/webhook", "application/json", bytes.NewReader(fixture(t, "terminal-run")),
	)
	if err != nil {
		t.Fatalf("posting delivery: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("unsigned delivery returned %d, want 401", response.StatusCode)
	}

	if scraped := scrape(t, server); strings.Contains(scraped, "spacelift_runs_total{") {
		t.Errorf("an unsigned delivery created run series:\n%s", scraped)
	}
}

func scrape(t *testing.T, server *httptest.Server) string {
	t.Helper()

	response, err := server.Client().Get(server.URL + "/metrics")
	if err != nil {
		t.Fatalf("scraping /metrics: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("/metrics returned %d", response.StatusCode)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading /metrics body: %v", err)
	}

	return string(body)
}
