package main

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestCollectGolden pins the exact exposition output of the collector for each
// deployment shape we support. A diff here is the metric-surface review: any
// change to a metric name, type, label set, HELP string or value shows up as a
// reviewable text diff rather than having to be inferred from Go code.
//
// Regenerate with: go test . -update-golden
func TestCollectGolden(t *testing.T) {
	for _, shape := range []string{"saas", "self-hosted", "empty-account"} {
		t.Run(shape, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, shape))
			assertGolden(t, shape, gather(t, stub.collector(t)))
		})
	}
}

// TestDescribeMatchesCollect asserts that every descriptor announced by
// Describe is actually emitted by Collect, and vice versa.
//
// This is a regression test for a real defect: spacelift_worker_pool_workers
// was declared in Describe but never sent to the metric channel, and
// spacelift_scrape_duration_seconds was emitted but never described. Both were
// fixed in #75. A pedantic registry does not catch either case on its own.
func TestDescribeMatchesCollect(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	collector := stub.collector(t)

	described := make(chan *prometheus.Desc, 256)
	collector.Describe(described)
	close(described)

	describedNames := map[string]bool{}
	for desc := range described {
		describedNames[fqName(t, desc.String())] = true
	}

	collected := make(chan prometheus.Metric, 256)
	collector.Collect(collected)
	close(collected)

	collectedNames := map[string]bool{}
	for metric := range collected {
		collectedNames[fqName(t, metric.Desc().String())] = true
	}

	for name := range describedNames {
		if !collectedNames[name] {
			t.Errorf("%s is announced by Describe but never emitted by Collect", name)
		}
	}

	for name := range collectedNames {
		if !describedNames[name] {
			t.Errorf("%s is emitted by Collect but never announced by Describe", name)
		}
	}
}

// lintBaseline lists the promlint findings that already exist on the shipped
// metric surface. They are grandfathered because renaming a published metric
// breaks every dashboard and alert built on it; they are not a licence to add
// more.
//
// Nothing should ever be added to this map. A new promlint finding means the
// metric being added does not follow Prometheus conventions, and the fix is to
// name it correctly before it ships.
var lintBaseline = map[string]string{
	// Named before the convention was applied. The value is a mean, not a
	// histogram count, so the _count suffix is misleading. Renaming it
	// would break existing consumers.
	"spacelift_current_avg_stack_size_by_resource_count": `non-histogram and non-summary metrics should not have "_count" suffix`,
}

// TestCollectLint enforces the Prometheus naming and unit conventions on
// everything except the grandfathered baseline: base units, _total only on
// counters, no reserved suffixes, consistent HELP.
//
// This is the gate that stops a large metrics PR from shipping convention bugs
// that a human reviewer would have to catch by eye.
func TestCollectLint(t *testing.T) {
	for _, shape := range []string{"saas", "self-hosted", "empty-account"} {
		t.Run(shape, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, shape))

			problems, err := testutil.CollectAndLint(stub.collector(t))
			if err != nil {
				t.Fatalf("linting collector output: %v", err)
			}

			for _, problem := range problems {
				if lintBaseline[problem.Metric] == problem.Text {
					continue
				}
				t.Errorf("promlint: %s: %s", problem.Metric, problem.Text)
			}
		})
	}
}

// TestLintBaselineIsNotStale fails if a grandfathered finding has been fixed,
// so the baseline shrinks as names are corrected and never silently rots.
func TestLintBaselineIsNotStale(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))

	problems, err := testutil.CollectAndLint(stub.collector(t))
	if err != nil {
		t.Fatalf("linting collector output: %v", err)
	}

	found := map[string]string{}
	for _, problem := range problems {
		found[problem.Metric] = problem.Text
	}

	for metric, text := range lintBaseline {
		if found[metric] != text {
			t.Errorf("%s is in lintBaseline but promlint no longer reports %q; remove the baseline entry", metric, text)
		}
	}
}

// TestQueryShape locks down the GraphQL document the exporter sends. The API
// is expensive for Spacelift to serve for metrics, so the cost of a scrape is
// part of this exporter's contract: a reviewer should be able to see, from a
// test diff, that a change adds a field or a round trip.
func TestQueryShape(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))

	metrics := make(chan prometheus.Metric, 256)
	stub.collector(t).Collect(metrics)
	close(metrics)

	// One request per enabled collector, and no more. Isolation costs
	// requests, so the count is part of the contract with Spacelift's
	// backend and a reviewer should see it change in a diff.
	if got, want := len(stub.queries), len(defaultCollectors); got != want {
		t.Errorf("a scrape issued %d GraphQL requests, want %d (one per enabled collector)", got, want)
	}

	// Every request carries a collector-specific operation name, so API load
	// can be attributed to a subsystem rather than to "the exporter", and each
	// document is pinned exactly: a change to any field selection is a change
	// to the API-cost contract and must show up in this diff.
	expectedQueries := map[string]string{
		"PrometheusExporterPublicWorkerPool": `query PrometheusExporterPublicWorkerPool{publicWorkerPool{parallelism,busyWorkers,pendingRuns}}`,
		"PrometheusExporterWorkerPools":      `query PrometheusExporterWorkerPools{workerPools{id,name,pendingRuns,busyWorkers,workers{id,drained}}}`,
		"PrometheusExporterUsage":            `query PrometheusExporterUsage{usage{billingPeriodStart,billingPeriodEnd,usedPrivateMinutes,usedPublicMinutes,usedSeats}}`,
		"PrometheusExporterAggregates":       `query PrometheusExporterAggregates{metrics{stacksCountByState{value,labels},resourcesCountByDrift{value,labels},avgStackSizeByResourceCount{value,labels},averageRunDuration{value,labels},medianRunDuration{value,labels}}}`,
	}

	seen := map[string]string{}
	for _, query := range stub.queries {
		seen[operationOf(query)] = query
	}

	for operation, want := range expectedQueries {
		got, ok := seen[operation]
		if !ok {
			t.Errorf("no request was named %s; got %v", operation, operationNames(stub.queries))
			continue
		}
		if got != want {
			t.Errorf("%s changed without updating the API-cost contract\nwant: %s\n got: %s", operation, want, got)
		}
	}

	// The envelope operationName must match the document, one per collector,
	// or Spacelift's APM attribution sees anonymous queries.
	if len(stub.operationNames) != len(expectedQueries) {
		t.Errorf("operationName envelope fields = %v, want one per collector", stub.operationNames)
	}
	for _, name := range stub.operationNames {
		if _, ok := expectedQueries[name]; !ok {
			t.Errorf("unexpected envelope operationName %q", name)
		}
	}

	// Range fields return a bucket per day over a server-chosen window.
	// Prometheus should be given point-in-time values and left to do its
	// own windowing, so none of these belong in a scrape.
	for _, query := range stub.queries {
		for _, forbidden := range []string{"metricsRange", "Range{", "Range(", "averageRunDurationRange", "stackFailuresRange"} {
			if strings.Contains(query, forbidden) {
				t.Errorf("query selects the windowed field %q; Prometheus must do its own windowing", forbidden)
			}
		}
	}
}

// TestCollectorsAreIsolated is the point of the refactor: one failing domain
// must not suppress the others.
//
// The stub fails every request, but the fixture the aggregates collector would
// have received is irrelevant — what matters is that a scrape still produces
// the metrics of the collectors that did work, and reports precisely which one
// did not.
func TestCollectorsAreIsolated(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	stub.failOperation("PrometheusExporterAggregates", `{"errors":[{"message":"internal error"}]}`)

	output := gather(t, stub.collector(t))

	// The failing collector is reported, and only it.
	for _, want := range []string{
		`spacelift_scrape_collector_success{collector="aggregates"} 0`,
		`spacelift_scrape_collector_success{collector="workerpools"} 1`,
		`spacelift_scrape_collector_success{collector="usage"} 1`,
		`spacelift_scrape_collector_success{collector="publicworkerpool"} 1`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in:\n%s", want, output)
		}
	}

	// The healthy collectors still produced their metrics.
	for _, want := range []string{
		"spacelift_worker_pool_runs_pending{",
		"spacelift_current_billing_period_used_seats ",
		"spacelift_public_worker_pool_parallelism ",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("a failure in one collector suppressed %q:\n%s", want, output)
		}
	}

	// The failing collector's metrics are absent rather than zeroed, so a
	// stale value is never mistaken for a live one.
	if strings.Contains(output, "spacelift_current_stacks_count_by_state{") {
		t.Error("the failed collector emitted metrics anyway")
	}
}

// TestGatherSucceedsWhenACollectorFails is the behaviour change this PR makes
// explicit: a failed collector no longer fails Gather(), so promhttp returns
// 200 and Prometheus's own up{} series stays 1. Operators who alerted on up
// must move to spacelift_scrape_collector_success.
func TestGatherSucceedsWhenACollectorFails(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	stub.failOperation("PrometheusExporterAggregates", `{"errors":[{"message":"internal error"}]}`)

	registry := prometheus.NewPedanticRegistry()
	if err := registry.Register(stub.collector(t)); err != nil {
		t.Fatalf("registering collector: %v", err)
	}

	if _, err := registry.Gather(); err != nil {
		t.Errorf("Gather() failed on a partial failure, so promhttp would return 500: %v", err)
	}
}

// legacyMetrics are the 19 families the exporter shipped before per-collector
// isolation. Every one must still be emitted.
//
// This exists because "the golden diff has 0 deletions" does not prove what it
// looks like it proves: a metric that stops being emitted in every fixture
// disappears from all of them, and a golden regeneration would happily record
// its absence. This asserts presence directly.
var legacyMetrics = []string{
	"spacelift_public_worker_pool_runs_pending",
	"spacelift_public_worker_pool_workers_busy",
	"spacelift_public_worker_pool_parallelism",
	"spacelift_worker_pool_runs_pending",
	"spacelift_worker_pool_workers_busy",
	"spacelift_worker_pool_workers",
	"spacelift_worker_pool_workers_drained",
	"spacelift_current_billing_period_start_timestamp_seconds",
	"spacelift_current_billing_period_end_timestamp_seconds",
	"spacelift_current_billing_period_used_private_seconds",
	"spacelift_current_billing_period_used_public_seconds",
	"spacelift_current_billing_period_used_seats",
	"spacelift_current_stacks_count_by_state",
	"spacelift_current_resources_count_by_drift",
	"spacelift_current_avg_stack_size_by_resource_count",
	"spacelift_current_average_run_duration",
	"spacelift_current_median_run_duration",
	"spacelift_scrape_duration_seconds",
	"spacelift_build_info",
}

// TestLegacyMetricsStillEmitted fails if any pre-existing metric family stops
// being exported, independently of what the golden files happen to contain.
func TestLegacyMetricsStillEmitted(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	output := gather(t, stub.collector(t))

	for _, name := range legacyMetrics {
		if !strings.Contains(output, "\n"+name+"{") && !strings.Contains(output, "\n"+name+" ") {
			t.Errorf("%s is no longer emitted; removing a shipped metric breaks every dashboard built on it", name)
		}
	}

	if got, want := len(legacyMetrics), 19; got != want {
		t.Errorf("legacyMetrics has %d entries, want %d; the shipped surface should not change", got, want)
	}
}

// TestUnknownCollectorIsRejected keeps a typo in --collector from silently
// exporting less than the operator asked for.
func TestUnknownCollectorIsRejected(t *testing.T) {
	if _, err := newCollectors(map[string]bool{"stacks": true}); err == nil {
		t.Error("newCollectors accepted an unknown collector name")
	}
}

// TestMachineKeyDegradesGracefully covers the third availability axis. A
// machine API key cannot read publicWorkerPool, usage or metrics, but can read
// workerPools. Under the old single query that meant no metrics at all.
func TestMachineKeyDegradesGracefully(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	machineError := `{"errors":[{"message":"not available for machine sessions"}]}`
	for _, operation := range []string{
		"PrometheusExporterPublicWorkerPool",
		"PrometheusExporterUsage",
		"PrometheusExporterAggregates",
	} {
		stub.failOperation(operation, machineError)
	}

	output := gather(t, stub.collector(t))

	// Gated collectors report unsupported, not failed: the key is fine,
	// the data simply is not available to it.
	for _, name := range []string{"publicworkerpool", "usage", "aggregates"} {
		for _, want := range []string{
			`spacelift_scrape_collector_supported{collector="` + name + `"} 0`,
			`spacelift_scrape_collector_success{collector="` + name + `"} 1`,
		} {
			if !strings.Contains(output, want) {
				t.Errorf("missing %q in:\n%s", want, output)
			}
		}
	}

	// And the ungated collector still works, which is the whole point.
	if !strings.Contains(output, "spacelift_worker_pool_runs_pending{") {
		t.Errorf("worker pool metrics should still be collected with a machine key:\n%s", output)
	}
}

// TestEveryCollectorFailingStillExports covers the worst case: the API is
// returning errors for everything.
//
// Previously this produced two metrics — a scrape duration and an invalid
// spacelift_error — and a failed Gather(). Now it produces a full set of
// per-collector health metrics, so an operator can see the exporter is alive
// and that every subsystem is failing, which are different facts.
func TestEveryCollectorFailingStillExports(t *testing.T) {
	stub := newGraphQLStub(t, `{"errors":[{"message":"internal error"}]}`)

	output := gather(t, stub.collector(t))

	for _, name := range defaultCollectors {
		want := `spacelift_scrape_collector_success{collector="` + name + `"} 0`
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in:\n%s", want, output)
		}
	}

	// The exporter still identifies itself and still reports how long the
	// scrape took, so the target is visibly up rather than silently absent.
	for _, want := range []string{"spacelift_build_info{", "spacelift_scrape_duration_seconds "} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in:\n%s", want, output)
		}
	}

	// spacelift_error is gone. It was an invalid metric, which is what made
	// Gather() fail and promhttp return 500.
	if strings.Contains(output, "spacelift_error") {
		t.Error("spacelift_error should have been replaced by spacelift_scrape_collector_success")
	}
}

// TestSessionRefreshedOnUnauthorized covers the retry path in client.Query,
// which refreshes the token and reissues the request when the API reports the
// session is no longer valid.
//
// Each collector retries independently, so an expired token costs one refresh
// per enabled collector on the scrape that discovers it. That is more calls
// than strictly necessary, but it happens once per token lifetime and keeping
// the collectors independent is worth more than deduplicating it.
func TestSessionRefreshedOnUnauthorized(t *testing.T) {
	stub := newGraphQLStub(t, `{"errors":[{"message":"unauthorized"}]}`)
	session := &fakeSession{endpoint: stub.server.URL}

	collector := collectorWithSession(t, stub, session)

	metrics := make(chan prometheus.Metric, 256)
	collector.Collect(metrics)
	close(metrics)

	if want := len(defaultCollectors); session.refreshCalls != want {
		t.Errorf("RefreshToken called %d times, want %d (one per collector)", session.refreshCalls, want)
	}

	if want := 2 * len(defaultCollectors); len(stub.queries) != want {
		t.Errorf("got %d GraphQL requests, want %d (one attempt plus one retry per collector)",
			len(stub.queries), want)
	}

	wantAuthorization := []string{"Bearer initial-token", "Bearer refreshed-token"}
	if len(stub.authorizationHeaders) != len(wantAuthorization) {
		t.Fatalf("got Authorization headers %v, want %v", stub.authorizationHeaders, wantAuthorization)
	}
	for i, want := range wantAuthorization {
		if got := stub.authorizationHeaders[i]; got != want {
			t.Errorf("request %d Authorization header = %q, want %q", i+1, got, want)
		}
	}
}

// TestRetryPreservesOperationName guards a real bug: the retry in
// client.Query reissues the request without graphql.OperationName, so the
// second attempt reaches Spacelift as an anonymous query and cannot be
// attributed to the exporter in their APM.
func TestRetryPreservesOperationName(t *testing.T) {
	stub := newGraphQLStub(t, `{"errors":[{"message":"unauthorized"}]}`)

	metrics := make(chan prometheus.Metric, 256)
	stub.collector(t).Collect(metrics)
	close(metrics)

	if len(stub.queries) < 2 {
		t.Fatalf("expected a retry, got %d request(s)", len(stub.queries))
	}

	for i, query := range stub.queries {
		if !strings.HasPrefix(query, "query PrometheusExporter{") {
			t.Errorf("request %d query document lost the operation name: %s", i+1, query)
		}
		if got := stub.operationNames[i]; got != "PrometheusExporter" {
			t.Errorf("request %d operationName envelope field = %q, want PrometheusExporter", i+1, got)
		}
	}
}

func operationNames(queries []string) []string {
	out := make([]string, 0, len(queries))
	for _, q := range queries {
		out = append(out, operationOf(q))
	}

	return out
}
