package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const testSecret = "correct-horse-battery-staple"

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}

	return body
}

// newTestHandler returns a handler with a fixed clock, so the
// last-delivery-timestamp gauge is deterministic.
func newTestHandler(t *testing.T) (*Handler, *prometheus.Registry) {
	t.Helper()

	registry := prometheus.NewPedanticRegistry()
	handler, err := NewHandler(context.Background(), NewMetrics(registry, nil), testSecret)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	handler.now = fixedTime

	return handler, registry
}

func post(t *testing.T, handler *Handler, body []byte, signature string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	if signature != "" {
		request.Header.Set(SignatureHeader, signature)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

func TestNewHandlerRequiresASecret(t *testing.T) {
	if _, err := NewHandler(context.Background(), NewMetrics(prometheus.NewPedanticRegistry(), nil), ""); err == nil {
		t.Fatal("NewHandler accepted an empty secret; an unauthenticated receiver lets anyone inject run metrics")
	}
}

func TestAcceptsSignedTerminalRun(t *testing.T) {
	handler, registry := newTestHandler(t)
	body := fixture(t, "terminal-run")

	if got := post(t, handler, body, Sign(body, testSecret)).Code; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}

	// 42 + 5 + 18 + 90 + 12 + 20 = 187 seconds.
	expected := `
# HELP spacelift_runs_total Total number of runs that reached a terminal state, by stack and outcome.
# TYPE spacelift_runs_total counter
spacelift_runs_total{drift_detection="false",final_state="FINISHED",run_type="TRACKED",space="production",stack="acme-production-network",worker_pool="production"} 1
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(expected), "spacelift_runs_total"); err != nil {
		t.Error(err)
	}

	expectedChanges := `
# HELP spacelift_run_resource_changes_total Total number of plan-phase resource changes by change type. Import, forget, no-op, read and Ansible outcomes are not counted.
# TYPE spacelift_run_resource_changes_total counter
spacelift_run_resource_changes_total{change_type="added",run_type="TRACKED",space="production",stack="acme-production-network"} 2
spacelift_run_resource_changes_total{change_type="changed",run_type="TRACKED",space="production",stack="acme-production-network"} 1
spacelift_run_resource_changes_total{change_type="deleted",run_type="TRACKED",space="production",stack="acme-production-network"} 1
spacelift_run_resource_changes_total{change_type="replaced",run_type="TRACKED",space="production",stack="acme-production-network"} 2
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(expectedChanges), "spacelift_run_resource_changes_total"); err != nil {
		t.Error(err)
	}

	expectedPolicies := `
# HELP spacelift_run_policy_evaluations_total Total number of policy evaluations recorded against terminated runs, by type and outcome.
# TYPE spacelift_run_policy_evaluations_total counter
spacelift_run_policy_evaluations_total{policy_outcome="allow",policy_type="PLAN",space="production",stack="acme-production-network"} 1
spacelift_run_policy_evaluations_total{policy_outcome="approve",policy_type="APPROVAL",space="production",stack="acme-production-network"} 1
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(expectedPolicies), "spacelift_run_policy_evaluations_total"); err != nil {
		t.Error(err)
	}

	output := gatherText(t, registry)
	if !strings.Contains(output, `spacelift_run_duration_seconds_sum{run_type="TRACKED",space="production",stack="acme-production-network"} 187`) {
		t.Errorf("run duration sum of 187s not found in output:\n%s", output)
	}
}

// TestReplacedCountsBothDirections guards the substring match that folds
// create-Before-destroy-replaced and destroy-Before-create-replaced into one
// change type, the same way the Datadog policy does.
func TestReplacedCountsBothDirections(t *testing.T) {
	run := Run{Changes: []EntityChange{
		{Action: "create-Before-destroy-replaced", Phase: "plan"},
		{Action: "destroy-Before-create-replaced", Phase: "plan"},
	}}

	if got := run.PlanChangeCounts()["replaced"]; got != 2 {
		t.Errorf("replaced = %d, want 2", got)
	}
}

// TestApplyPhaseChangesAreNotCounted pins the plan-phase-only rule. Counting
// both phases would double-count every applied resource.
func TestApplyPhaseChangesAreNotCounted(t *testing.T) {
	run := Run{Changes: []EntityChange{
		{Action: "added", Phase: "plan"},
		{Action: "added", Phase: "apply"},
	}}

	if got := run.PlanChangeCounts()["added"]; got != 1 {
		t.Errorf("added = %d, want 1 (apply-phase changes must not be counted)", got)
	}
}

// TestUncountedChangeTypesAreIgnored covers the change types the Datadog
// integration silently drops, so our numbers match theirs.
func TestUncountedChangeTypesAreIgnored(t *testing.T) {
	run := Run{Changes: []EntityChange{
		{Action: "import", Phase: "plan"},
		{Action: "forget", Phase: "plan"},
		{Action: "no-op", Phase: "plan"},
		{Action: "read", Phase: "plan"},
	}}

	for changeType, count := range run.PlanChangeCounts() {
		if count != 0 {
			t.Errorf("%s = %d, want 0", changeType, count)
		}
	}
}

func TestPublicWorkerPoolFallback(t *testing.T) {
	handler, registry := newTestHandler(t)
	body := fixture(t, "public-pool-run")

	if got := post(t, handler, body, Sign(body, testSecret)).Code; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}

	// The stack has no worker_pool key at all, because it runs on the
	// public pool. It must still produce a series, labelled "public".
	expected := `
# HELP spacelift_runs_total Total number of runs that reached a terminal state, by stack and outcome.
# TYPE spacelift_runs_total counter
spacelift_runs_total{drift_detection="true",final_state="FAILED",run_type="PROPOSED",space="root",stack="acme-sandbox",worker_pool="public"} 1
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(expected), "spacelift_runs_total"); err != nil {
		t.Error(err)
	}
}

// TestZeroFilledChangeTypes checks that a run with no changes still reports all
// four change types as 0, so rate() over a quiet stack returns 0 rather than no
// data.
func TestZeroFilledChangeTypes(t *testing.T) {
	handler, registry := newTestHandler(t)
	body := fixture(t, "public-pool-run")

	post(t, handler, body, Sign(body, testSecret))

	output := gatherText(t, registry)
	for _, changeType := range changeTypes {
		want := `change_type="` + changeType + `"`
		if !strings.Contains(output, want) {
			t.Errorf("change type %s missing from output; zero values must still be emitted:\n%s", changeType, output)
		}
	}
}

func TestIgnoresInFlightRuns(t *testing.T) {
	handler, registry := newTestHandler(t)
	body := fixture(t, "in-flight-run")

	// A non-2xx response counts towards the consecutive-failure limit that
	// makes Spacelift disable the webhook, so an uninteresting delivery must
	// still be acknowledged.
	if got := post(t, handler, body, Sign(body, testSecret)).Code; got != http.StatusOK {
		t.Errorf("status = %d, want 200 so Spacelift does not count this as a failure", got)
	}

	if got := testutil.CollectAndCount(handler.metrics.runs); got != 0 {
		t.Errorf("an in-flight run produced %d run series, want 0", got)
	}

	assertDeliveryResult(t, registry, resultIgnored, 1)
}

func TestRejectsBadSignature(t *testing.T) {
	body := fixture(t, "terminal-run")

	cases := []struct {
		name      string
		signature string
	}{
		{"missing", ""},
		{"wrong secret", Sign(body, "wrong-secret")},
		{"not prefixed", "deadbeef"},
		{"not hex", "sha256=nothexatall"},
		{"empty digest", "sha256="},
		{"truncated digest", Sign(body, testSecret)[:20]},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, registry := newTestHandler(t)

			if got := post(t, handler, body, tc.signature).Code; got != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", got)
			}

			if got := testutil.CollectAndCount(handler.metrics.runs); got != 0 {
				t.Errorf("a rejected delivery produced %d run series, want 0", got)
			}

			assertDeliveryResult(t, registry, resultBadSignature, 1)
		})
	}
}

// TestSignatureCoversTheWholeBody checks that tampering with the payload after
// signing is detected, which is the property the signature exists for.
func TestSignatureCoversTheWholeBody(t *testing.T) {
	handler, _ := newTestHandler(t)
	body := fixture(t, "terminal-run")
	signature := Sign(body, testSecret)

	tampered := strings.Replace(string(body), `"acme-production-network"`, `"attacker-stack-aaaaa"`, 1)
	if tampered == string(body) {
		t.Fatal("fixture did not contain the stack id, test cannot tamper with it")
	}

	if got := post(t, handler, []byte(tampered), signature).Code; got != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a tampered body", got)
	}
}

func TestRejectsNonPost(t *testing.T) {
	handler, registry := newTestHandler(t)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/webhook", nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", recorder.Code)
	}

	assertDeliveryResult(t, registry, resultMethodNotAllowed, 1)
}

func TestRejectsMalformedPayload(t *testing.T) {
	handler, registry := newTestHandler(t)
	body := []byte(`{"run_updated": not json`)

	if got := post(t, handler, body, Sign(body, testSecret)).Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", got)
	}

	assertDeliveryResult(t, registry, resultMalformed, 1)
}

// TestCountersAccumulate is the behaviour that separates this from the
// scrape-time collector: repeated deliveries add up between scrapes.
func TestCountersAccumulate(t *testing.T) {
	handler, registry := newTestHandler(t)
	body := fixture(t, "terminal-run")
	signature := Sign(body, testSecret)

	for range 3 {
		post(t, handler, body, signature)
	}

	output := gatherText(t, registry)
	if !strings.Contains(output, `worker_pool="production"} 3`) {
		t.Errorf("three deliveries did not accumulate to 3:\n%s", output)
	}

	if !strings.Contains(output, `spacelift_run_duration_seconds_count{run_type="TRACKED",space="production",stack="acme-production-network"} 3`) {
		t.Errorf("histogram did not observe three runs:\n%s", output)
	}
}

func TestLint(t *testing.T) {
	handler, registry := newTestHandler(t)
	body := fixture(t, "terminal-run")
	post(t, handler, body, Sign(body, testSecret))

	problems, err := testutil.GatherAndLint(registry)
	if err != nil {
		t.Fatalf("linting webhook metrics: %v", err)
	}

	for _, problem := range problems {
		t.Errorf("promlint: %s: %s", problem.Metric, problem.Text)
	}
}

func TestSignMatchesSpaceliftFormat(t *testing.T) {
	// Spacelift computes fmt.Sprintf("sha256=%x", hmac.New(sha256.New,
	// secret).Sum(body)), so the value is the prefix plus 64 lowercase hex
	// characters.
	signature := Sign([]byte("payload"), "secret")

	if !strings.HasPrefix(signature, "sha256=") {
		t.Errorf("signature %q is missing the sha256= prefix", signature)
	}

	if got := len(strings.TrimPrefix(signature, "sha256=")); got != 64 {
		t.Errorf("digest length = %d, want 64 hex characters", got)
	}

	if signature != strings.ToLower(signature) {
		t.Errorf("digest %q is not lowercase hex", signature)
	}
}
