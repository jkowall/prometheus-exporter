package collector

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// WorkerPools collects per-pool worker metrics.
//
// Unlike publicWorkerPool and usage, the workerPools resolver has no
// machine-session gate, so this collector works with an API key that cannot
// read either of those.
type WorkerPools struct {
	runsPending    *prometheus.Desc
	workersBusy    *prometheus.Desc
	workers        *prometheus.Desc
	workersDrained *prometheus.Desc
}

// NewWorkerPools returns the private worker pool collector.
func NewWorkerPools() *WorkerPools {
	labels := []string{"worker_pool_id", "worker_pool_name"}

	return &WorkerPools{
		runsPending: prometheus.NewDesc(
			"spacelift_worker_pool_runs_pending",
			"The number of runs currently queued and waiting for a worker from a particular pool",
			labels,
			nil),
		workersBusy: prometheus.NewDesc(
			"spacelift_worker_pool_workers_busy",
			"The number of currently busy workers in a worker pool",
			labels,
			nil),
		workers: prometheus.NewDesc(
			"spacelift_worker_pool_workers",
			"The number of workers in a worker pool",
			labels,
			nil),
		workersDrained: prometheus.NewDesc(
			"spacelift_worker_pool_workers_drained",
			"The number of workers in a worker pool that have been drained",
			labels,
			nil),
	}
}

// Name implements Collector.
func (c *WorkerPools) Name() string { return "workerpools" }

// Describe implements Collector.
func (c *WorkerPools) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.runsPending
	ch <- c.workersBusy
	ch <- c.workers
	ch <- c.workersDrained
}

type workerPoolsQuery struct {
	WorkerPools []struct {
		ID          string `graphql:"id"`
		Name        string `graphql:"name"`
		PendingRuns int    `graphql:"pendingRuns"`
		BusyWorkers int    `graphql:"busyWorkers"`
		Workers     []struct {
			ID      string `graphql:"id"`
			Drained bool   `graphql:"drained"`
		} `graphql:"workers"`
	} `graphql:"workerPools"`
}

// Collect implements Collector.
func (c *WorkerPools) Collect(ctx context.Context, api client.Client, ch chan<- prometheus.Metric) error {
	var query workerPoolsQuery
	if err := api.Query(ctx, &query, nil, "WorkerPools"); err != nil {
		return classify(err, c.Name())
	}

	for _, pool := range query.WorkerPools {
		var drained int
		for _, worker := range pool.Workers {
			if worker.Drained {
				drained++
			}
		}

		ch <- prometheus.MustNewConstMetric(
			c.runsPending, prometheus.GaugeValue, float64(pool.PendingRuns), pool.ID, pool.Name)
		ch <- prometheus.MustNewConstMetric(
			c.workersBusy, prometheus.GaugeValue, float64(pool.BusyWorkers), pool.ID, pool.Name)
		ch <- prometheus.MustNewConstMetric(
			c.workers, prometheus.GaugeValue, float64(len(pool.Workers)), pool.ID, pool.Name)
		ch <- prometheus.MustNewConstMetric(
			c.workersDrained, prometheus.GaugeValue, float64(drained), pool.ID, pool.Name)
	}

	return nil
}
