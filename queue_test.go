package main

import (
	"strings"
	"testing"
)

// TestQueueWithoutAnAnswer covers the nullable search and total. A pool whose
// search or total the API returns as null, without an error, must report
// nothing rather than an empty queue: a truncated=0 series would claim the
// pool's queue was read in full. The pools after it must still report.
func TestQueueWithoutAnAnswer(t *testing.T) {
	for _, test := range []struct {
		name, operation, response string
		// present must appear in the output; absent must not appear
		// anywhere in it.
		present, absent []string
	}{
		{
			name:      "private pools",
			operation: "PrometheusExporter_Queue",
			response: `{"data":{"workerPools":[` +
				`{"id":"pool-unanswered","name":"unanswered","searchSchedulableRuns":null,"searchSchedulableRunsSuggestions":{"filteredCount":0}},` +
				`{"id":"pool-uncounted","name":"uncounted","searchSchedulableRuns":{"edges":[],"pageInfo":{"hasNextPage":false}},"searchSchedulableRunsSuggestions":null},` +
				`{"id":"pool-ok","name":"ok","searchSchedulableRuns":{"edges":[{"node":{"run":{"state":"READY","type":"TRACKED"}}}],"pageInfo":{"hasNextPage":false}},"searchSchedulableRunsSuggestions":{"filteredCount":1}}]}}`,
			present: []string{
				`spacelift_scrape_collector_success{collector="queue"} 1`,
				`spacelift_worker_pool_schedulable_runs{state="READY",type="TRACKED",worker_pool_id="pool-ok",worker_pool_name="ok"} 1`,
				`spacelift_worker_pool_schedulable_runs_truncated{worker_pool_id="pool-ok",worker_pool_name="ok"} 0`,
			},
			absent: []string{"pool-unanswered", "pool-uncounted"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, "saas"))
			stub.failOperation(test.operation, test.response)

			output := gather(t, stub.allCollectors(t))

			for _, want := range test.present {
				if !strings.Contains(output, want) {
					t.Errorf("missing %q in:\n%s", want, output)
				}
			}

			for _, unwanted := range test.absent {
				if strings.Contains(output, unwanted) {
					t.Errorf("a search with no answer still produced series mentioning %q:\n%s", unwanted, output)
				}
			}
		})
	}
}
