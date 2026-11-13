package collector

import (
	"encoding/json"
	"reflect"
	"testing"
)

func pageOf(t *testing.T, body string) schedulableRuns {
	t.Helper()

	var runs schedulableRuns
	if err := json.Unmarshal([]byte(body), &runs); err != nil {
		t.Fatalf("decoding page: %v", err)
	}

	return runs
}

func TestSummarizeQueue(t *testing.T) {
	int64p := func(v int64) *int64 { return &v }

	// The oldest run is neither the first nor the last edge.
	const mixed = `{"edges":[
		{"node":{"run":{"createdAt":300,"state":"READY","type":"TRACKED"}}},
		{"node":{"run":{"createdAt":100,"state":"PLANNING","type":"PROPOSED"}}},
		{"node":{"run":{"createdAt":200,"state":"READY","type":"TRACKED"}}},
		{"node":{"run":{"createdAt":400,"state":"READY","type":"PROPOSED"}}}
	],"pageInfo":{"hasNextPage":false}}`
	mixedCounts := []runCount{
		{runKey: runKey{state: "PLANNING", runType: "PROPOSED"}, count: 1},
		{runKey: runKey{state: "READY", runType: "PROPOSED"}, count: 1},
		{runKey: runKey{state: "READY", runType: "TRACKED"}, count: 2},
	}

	for _, test := range []struct {
		name  string
		page  string
		total int
		want  queueSummary
	}{
		{
			name: "empty: no oldest",
			page: `{"edges":[],"pageInfo":{"hasNextPage":false}}`,
			want: queueSummary{},
		},
		{
			name:  "complete: oldest found whatever the order",
			page:  mixed,
			total: 4,
			want:  queueSummary{counts: mixedCounts, oldest: int64p(100)},
		},
		{
			name:  "one short: truncated, oldest withheld",
			page:  mixed,
			total: 5,
			want:  queueSummary{counts: mixedCounts, truncated: true},
		},
		{
			// The search reports hasNextPage false regardless, so only the
			// total says that runs are missing.
			name:  "partial with hasNextPage false: truncated, oldest withheld",
			page:  mixed,
			total: 750,
			want:  queueSummary{counts: mixedCounts, truncated: true},
		},
		{
			name:  "partial by hasNextPage alone: oldest withheld",
			page:  `{"edges":[{"node":{"run":{"createdAt":100,"state":"READY","type":"TRACKED"}}}],"pageInfo":{"hasNextPage":true}}`,
			total: 1,
			want: queueSummary{
				counts:    []runCount{{runKey: runKey{state: "READY", runType: "TRACKED"}, count: 1}},
				truncated: true,
			},
		},
		{
			// The total comes from a separate field, so a run starting or
			// finishing between the two can make it differ from the search.
			// More returned than counted is still everything.
			name:  "more returned than counted",
			page:  mixed,
			total: 3,
			want:  queueSummary{counts: mixedCounts, oldest: int64p(100)},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := summarizeQueue(pageOf(t, test.page), test.total); !reflect.DeepEqual(got, test.want) {
				t.Errorf("summarizeQueue = %+v, want %+v", got, test.want)
			}
		})
	}
}
