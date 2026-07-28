package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// delivery mirrors one element of the notification policy's webhook set.
type delivery struct {
	EndpointID string          `json:"endpoint_id"`
	Payload    json.RawMessage `json:"payload"`
}

// TestPolicyOutputIsConsumable is the contract test between the two halves of
// this feature: the Rego notification policy in contrib/ and the Go receiver
// here.
//
// The fixtures under contrib/testdata are the real output of
//
//	opa eval -d contrib/notification-policy.rego -i <input> data.spacelift.webhook
//
// so this asserts the receiver consumes exactly what the policy emits. Without
// it, the two artifacts can drift silently and the failure only shows up in a
// customer's account. CI regenerates the outputs and fails if they are stale;
// see .github/workflows/test.yml.
func TestPolicyOutputIsConsumable(t *testing.T) {
	cases := []struct {
		fixture string
		assert  func(t *testing.T, registry *prometheus.Registry)
	}{
		{
			fixture: "policy-output-private-pool",
			assert: func(t *testing.T, registry *prometheus.Registry) {
				expected := `
# HELP spacelift_runs_total Total number of runs that reached a terminal state, by stack and outcome.
# TYPE spacelift_runs_total counter
spacelift_runs_total{drift_detection="false",final_state="FINISHED",run_type="TRACKED",space="production",stack="acme-production-network",worker_pool="production"} 1
`
				if err := testutil.GatherAndCompare(registry, strings.NewReader(expected), "spacelift_runs_total"); err != nil {
					t.Error(err)
				}

				// 42s queued + 90s planning.
				output := gatherText(t, registry)
				if !strings.Contains(output, `spacelift_run_duration_seconds_sum{run_type="TRACKED",space="production",stack="acme-production-network"} 132`) {
					t.Errorf("expected a 132s duration:\n%s", output)
				}

				// One plan-phase "added" and one replacement; the
				// apply-phase "added" must not be counted.
				if !strings.Contains(output, `spacelift_run_resource_changes_total{change_type="added",run_type="TRACKED",space="production",stack="acme-production-network"} 1`) {
					t.Errorf("expected exactly one plan-phase add:\n%s", output)
				}
				if !strings.Contains(output, `spacelift_run_resource_changes_total{change_type="replaced",run_type="TRACKED",space="production",stack="acme-production-network"} 1`) {
					t.Errorf("expected one replacement:\n%s", output)
				}
			},
		},
		{
			fixture: "policy-output-public-pool",
			assert: func(t *testing.T, registry *prometheus.Registry) {
				// The policy omits worker_pool entirely for public-pool
				// stacks, and the receiver must fall back to "public".
				expected := `
# HELP spacelift_runs_total Total number of runs that reached a terminal state, by stack and outcome.
# TYPE spacelift_runs_total counter
spacelift_runs_total{drift_detection="true",final_state="FAILED",run_type="PROPOSED",space="root",stack="acme-sandbox",worker_pool="public"} 1
`
				if err := testutil.GatherAndCompare(registry, strings.NewReader(expected), "spacelift_runs_total"); err != nil {
					t.Error(err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			deliveries := policyOutput(t, tc.fixture)
			if len(deliveries) != 1 {
				t.Fatalf("expected exactly 1 delivery, got %d", len(deliveries))
			}

			if deliveries[0].EndpointID != "prom-hook" {
				t.Errorf("delivery went to %q, want the prometheus-labelled endpoint", deliveries[0].EndpointID)
			}

			registry := prometheus.NewPedanticRegistry()
			handler, err := NewHandler(context.Background(), NewMetrics(registry, nil), testSecret)
			if err != nil {
				t.Fatalf("NewHandler: %v", err)
			}
			handler.now = fixedTime

			body := deliveries[0].Payload
			if got := post(t, handler, body, Sign(body, testSecret)).Code; got != http.StatusOK {
				t.Fatalf("status = %d, want 200", got)
			}

			tc.assert(t, registry)
		})
	}
}

// TestPolicyIgnoresInFlightRuns asserts the filtering happens in the policy, not
// only in the receiver. Filtering at the source means Spacelift never makes the
// HTTP call at all for a run still in progress.
func TestPolicyIgnoresInFlightRuns(t *testing.T) {
	if got := policyOutput(t, "policy-output-in-flight"); len(got) != 0 {
		t.Errorf("the policy produced %d deliveries for an in-flight run, want 0", len(got))
	}
}

// TestPolicyStripsSensitiveFields checks that the projection in the Rego drops
// the parts of the notification input that have no business in a metrics
// pipeline. The inputs deliberately contain a commit message, a creator IP and
// a secret environment variable.
func TestPolicyStripsSensitiveFields(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "contrib", "testdata", "policy-output-private-pool.json"))
	if err != nil {
		t.Fatalf("reading policy output: %v", err)
	}

	for _, forbidden := range []string{
		"supersecret",     // runtime_config.environment.TF_TOKEN
		"creator_ip",      // creator_session
		"10.0.0.1",        // creator_session.creator_ip
		"secret stuff",    // run.commit.message
		"also secret",     // stack.tracked_commit.message
		"abc123",          // run.commit.hash
		"urls",            // run URL, unbounded cardinality
		"tracked_commit",  //
		"creator_session", //
		"runtime_config",  //
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the forwarded payload still contains %q; the Rego projection must drop it", forbidden)
		}
	}
}

func policyOutput(t *testing.T, name string) []delivery {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "contrib", "testdata", name+".json"))
	if err != nil {
		t.Fatalf("reading policy output %s: %v", name, err)
	}

	var deliveries []delivery
	if err := json.Unmarshal(raw, &deliveries); err != nil {
		t.Fatalf("unmarshalling policy output %s: %v", name, err)
	}

	return deliveries
}
