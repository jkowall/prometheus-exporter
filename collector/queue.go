package collector

import (
	"cmp"
	"context"
	"slices"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// searchInput is the SearchInput variable of the schedulable runs search. It
// sets no page size: the search ignores `first` and reports hasNextPage false
// regardless, so completeness is judged against the search's own total
// instead (see summarizeQueue).
type searchInput struct {
	OrderBy []searchQueryOrder `json:"orderBy"`
}

// GetGraphQLType names the variable's type in the document, which the client
// would otherwise derive from the Go type name.
func (searchInput) GetGraphQLType() string { return "SearchInput" }

type searchQueryOrder struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

// searchSuggestionsInput is the SearchSuggestionsInput variable of the
// suggestions query, which is read only for its filteredCount. An empty field
// list asks for no per-field suggestions.
type searchSuggestionsInput struct {
	Fields []string `json:"fields"`
}

// GetGraphQLType names the variable's type in the document.
func (searchSuggestionsInput) GetGraphQLType() string { return "SearchSuggestionsInput" }

// queueVariables asks for the runs oldest first and for the total alone. The
// order does not change complete counts or the oldest creation time. The order
// of a truncated result has not been observed, so the oldest creation time is
// not taken from one (see summarizeQueue).
var queueVariables = map[string]any{
	"input": searchInput{OrderBy: []searchQueryOrder{{Field: "createdAt", Direction: "ASC"}}},
	"total": searchSuggestionsInput{Fields: []string{}},
}

// schedulableRunsTotal is the number of runs the search matches, which the
// search result itself does not carry.
type schedulableRunsTotal struct {
	FilteredCount int `graphql:"filteredCount"`
}

// schedulableRuns is the part of a pool's schedulable runs the search
// returned: runs waiting for a worker and runs a worker is executing.
type schedulableRuns struct {
	Edges []struct {
		Node struct {
			Run struct {
				CreatedAt int64  `graphql:"createdAt"`
				State     string `graphql:"state"`
				Type      string `graphql:"type"`
			} `graphql:"run"`
		} `graphql:"node"`
	} `graphql:"edges"`
	PageInfo struct {
		HasNextPage bool `graphql:"hasNextPage"`
	} `graphql:"pageInfo"`
}

type runKey struct {
	state, runType string
}

type runCount struct {
	runKey
	count int
}

// queueSummary is what the metrics report about a queue.
type queueSummary struct {
	// counts holds one entry per state and type returned, sorted by both.
	counts []runCount

	// oldest is the earliest creation time returned, or nil when the search
	// returned no runs or only some of them.
	oldest *int64

	truncated bool
}

// summarizeQueue counts the runs the search returned by state and type, and
// judges them against total, the number the search matched. Fewer returned
// than matched, or a next page, means the counts are partial. The oldest
// creation time is then withheld: the runs are requested oldest first, but
// the order of a truncated result has not been observed, so the oldest
// creation time is not taken from one.
//
// The search and the total are separate fields of one request, and nothing
// says they are read at the same instant. Runs starting or finishing between
// them can make a complete result read as truncated, or a partial one read as
// complete, for that scrape.
func summarizeQueue(runs schedulableRuns, total int) queueSummary {
	counts := map[runKey]int{}
	var summary queueSummary

	for _, edge := range runs.Edges {
		run := edge.Node.Run
		counts[runKey{state: run.State, runType: run.Type}]++

		if summary.oldest == nil || run.CreatedAt < *summary.oldest {
			summary.oldest = &run.CreatedAt
		}
	}

	for key, count := range counts {
		summary.counts = append(summary.counts, runCount{runKey: key, count: count})
	}
	slices.SortFunc(summary.counts, func(a, b runCount) int {
		return cmp.Or(cmp.Compare(a.state, b.state), cmp.Compare(a.runType, b.runType))
	})

	summary.truncated = runs.PageInfo.HasNextPage || len(runs.Edges) < total
	if summary.truncated {
		summary.oldest = nil
	}

	return summary
}

// queueDescs are the descriptors one pool's queue reports.
type queueDescs struct {
	runs, oldest, truncated *prometheus.Desc
}

func newQueueDescs(prefix, pool string, labels []string) queueDescs {
	return queueDescs{
		runs: prometheus.NewDesc(
			prefix+"_schedulable_runs",
			"The number of runs waiting for or executing on a worker in "+pool+", by the run's state and type; partial when "+prefix+"_schedulable_runs_truncated is 1",
			append(slices.Clone(labels), "state", "type"),
			nil),
		oldest: prometheus.NewDesc(
			prefix+"_oldest_schedulable_run_created_timestamp_seconds",
			"The Unix creation time of the oldest run waiting for or executing on a worker in "+pool+", absent when there is none or when the search returned only some of them",
			labels,
			nil),
		truncated: prometheus.NewDesc(
			prefix+"_schedulable_runs_truncated",
			"Whether the search returned only some of the schedulable runs in "+pool+" (1), so the by-state counts are partial and the oldest-run time is withheld, or all of them (0)",
			labels,
			nil),
	}
}

func (d queueDescs) describe(ch chan<- *prometheus.Desc) {
	ch <- d.runs
	ch <- d.oldest
	ch <- d.truncated
}

func (d queueDescs) metrics(runs schedulableRuns, total int, labelValues ...string) []prometheus.Metric {
	summary := summarizeQueue(runs, total)
	out := make([]prometheus.Metric, 0, len(summary.counts)+2)

	for _, c := range summary.counts {
		out = append(out, prometheus.MustNewConstMetric(
			d.runs, prometheus.GaugeValue, float64(c.count), append(slices.Clone(labelValues), c.state, c.runType)...))
	}

	if summary.oldest != nil {
		out = append(out, prometheus.MustNewConstMetric(d.oldest, prometheus.GaugeValue, float64(*summary.oldest), labelValues...))
	}

	return append(out, prometheus.MustNewConstMetric(d.truncated, prometheus.GaugeValue, boolValue(summary.truncated), labelValues...))
}

// queue collects the schedulable runs of every private worker pool. Its one
// request selects a search and a count for every pool, which is why it is off
// by default.
type queue struct {
	descs queueDescs
}

// NewQueue returns the private worker pool queue collector.
func NewQueue() Collector {
	return &queue{descs: newQueueDescs(
		"spacelift_worker_pool", "a worker pool", []string{"worker_pool_id", "worker_pool_name"})}
}

func (c *queue) Name() string { return "queue" }

func (c *queue) Describe(ch chan<- *prometheus.Desc) { c.descs.describe(ch) }

// searchFields are the search fields queue and publicQueue select, which an
// older backend's schema may lack. Both declare them version-gated.
var searchFields = []string{"searchSchedulableRuns", "searchSchedulableRunsSuggestions"}

// VersionGatedFields declares the search fields.
func (c *queue) VersionGatedFields() []string { return slices.Clone(searchFields) }

type queueQuery struct {
	WorkerPools []struct {
		ID                    string                `graphql:"id"`
		Name                  string                `graphql:"name"`
		SearchSchedulableRuns *schedulableRuns      `graphql:"searchSchedulableRuns(input:$input)"`
		Total                 *schedulableRunsTotal `graphql:"searchSchedulableRunsSuggestions(input:$total)"`
	} `graphql:"workerPools"`
}

func (c *queue) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	var query queueQuery
	if err := api.QueryNamed(ctx, &query, queueVariables, "Queue"); err != nil {
		return nil, err
	}

	var metrics []prometheus.Metric
	for _, pool := range query.WorkerPools {
		// Both fields are nullable. A null without an error means the API
		// has no answer for this pool, which then reports nothing rather
		// than an empty or unjudged queue. A null with an error never
		// reaches here: the client returns the error alone, which fails
		// the collector for every pool.
		if pool.SearchSchedulableRuns == nil || pool.Total == nil {
			continue
		}
		metrics = append(metrics, c.descs.metrics(*pool.SearchSchedulableRuns, pool.Total.FilteredCount, pool.ID, pool.Name)...)
	}

	return metrics, nil
}

// publicQueue collects the account's schedulable runs on the public worker
// pool, with the same search and count as queue.
type publicQueue struct {
	descs queueDescs
}

// NewPublicQueue returns the public worker pool queue collector.
func NewPublicQueue() Collector {
	return &publicQueue{descs: newQueueDescs("spacelift_public_worker_pool", "the public worker pool for this account", nil)}
}

func (c *publicQueue) Name() string { return "publicqueue" }

func (c *publicQueue) Describe(ch chan<- *prometheus.Desc) { c.descs.describe(ch) }

// VersionGatedFields declares the same search fields as queue does.
func (c *publicQueue) VersionGatedFields() []string { return slices.Clone(searchFields) }

type publicQueueQuery struct {
	PublicWorkerPool *struct {
		SearchSchedulableRuns *schedulableRuns      `graphql:"searchSchedulableRuns(input:$input)"`
		Total                 *schedulableRunsTotal `graphql:"searchSchedulableRunsSuggestions(input:$total)"`
	} `graphql:"publicWorkerPool"`
}

func (c *publicQueue) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	var query publicQueueQuery
	if err := api.QueryNamed(ctx, &query, queueVariables, "PublicQueue"); err != nil {
		return nil, err
	}

	// The pool is nullable as well as its search and total. As for a private
	// pool, a null without an error reports nothing.
	pool := query.PublicWorkerPool
	if pool == nil || pool.SearchSchedulableRuns == nil || pool.Total == nil {
		return nil, nil
	}

	return c.descs.metrics(*pool.SearchSchedulableRuns, pool.Total.FilteredCount), nil
}
