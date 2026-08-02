// Package collector implements the Spacelift metric collectors.
//
// Each collector owns exactly one GraphQL document. That is not a stylistic
// choice: GraphQL propagates a field error up to the nearest nullable parent,
// and several of the fields this exporter reads are non-null at the query root
// (`usage: Usage!`, `workerPools: [WorkerPool!]!`, `messageQueueStats:
// [MessageQueueStats!]!`), while every field of `SpaceliftMetrics` is non-null
// under a nullable `metrics`. One erroring field therefore nulls the entire
// response, so partial success is impossible inside a single selection set.
//
// The boundaries follow availability rather than aesthetics. `publicWorkerPool`
// and `usage` reject machine sessions and `usage` additionally requires read
// access to the root space, while `workerPools` has no such gate — so a machine
// key that can serve worker pool metrics perfectly well would, under a single
// document, produce no metrics at all.
package collector

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// ErrNotSupported is returned by a Collector whose data is not available on
// this deployment, tier, or for this API key. It is reported as
// spacelift_scrape_collector_supported=0 rather than as a failure, so that a
// self-hosted-only field on SaaS does not page anyone.
var ErrNotSupported = errors.New("not supported on this Spacelift deployment")

// Collector gathers one subsystem's metrics from a single GraphQL document.
type Collector interface {
	// Name is the value of the "collector" label, and the suffix of the
	// --collector.<name> flag.
	Name() string

	// Describe sends the descriptors of every metric this collector can
	// emit. Every descriptor announced here must be emitted by a successful
	// Collect, and vice versa.
	Describe(ch chan<- *prometheus.Desc)

	// Collect queries the API and emits metrics. Returning ErrNotSupported
	// marks the collector unsupported rather than failed.
	Collect(ctx context.Context, c client.Client, ch chan<- prometheus.Metric) error
}

// Exporter fans a scrape out across collectors, isolating their failures from
// each other.
type Exporter struct {
	ctx           context.Context
	logger        *zap.SugaredLogger
	client        client.Client
	scrapeTimeout time.Duration
	collectors    []Collector

	collectorSuccess   *prometheus.Desc
	collectorDuration  *prometheus.Desc
	collectorSupported *prometheus.Desc
	scrapeDuration     *prometheus.Desc
	buildInfo          *prometheus.Desc
}

// BuildInfo identifies the running exporter.
type BuildInfo struct {
	Version   string
	Commit    string
	GoVersion string
}

// New returns an Exporter over the given collectors.
func New(
	ctx context.Context,
	logger *zap.SugaredLogger,
	c client.Client,
	scrapeTimeout time.Duration,
	build BuildInfo,
	collectors []Collector,
) *Exporter {
	return &Exporter{
		ctx:           ctx,
		logger:        logger,
		client:        c,
		scrapeTimeout: scrapeTimeout,
		collectors:    collectors,

		collectorSuccess: prometheus.NewDesc(
			"spacelift_scrape_collector_success",
			"Whether a collector succeeded on the last scrape (1) or failed (0).",
			[]string{"collector"},
			nil),
		collectorDuration: prometheus.NewDesc(
			"spacelift_scrape_collector_duration_seconds",
			"Duration of a collector's Spacelift API request on the last scrape.",
			[]string{"collector"},
			nil),
		collectorSupported: prometheus.NewDesc(
			"spacelift_scrape_collector_supported",
			"Whether a collector's data is available on this deployment, tier and API key (1) or not (0). "+
				"An unsupported collector is not a failure.",
			[]string{"collector"},
			nil),
		scrapeDuration: prometheus.NewDesc(
			"spacelift_scrape_duration_seconds",
			"The duration in seconds of the request to the Spacelift API for metrics",
			nil,
			nil),
		buildInfo: prometheus.NewDesc(
			"spacelift_build_info",
			"Contains build information about the exporter",
			nil,
			prometheus.Labels{
				"version":   build.Version,
				"commit":    build.Commit,
				"goversion": build.GoVersion,
			}),
	}
}

// Describe implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- e.collectorSuccess
	ch <- e.collectorDuration
	ch <- e.collectorSupported
	ch <- e.scrapeDuration
	ch <- e.buildInfo

	for _, c := range e.collectors {
		c.Describe(ch)
	}
}

// Collect implements prometheus.Collector.
//
// Every collector runs, and one failing collector does not suppress the others.
// A failure is reported through spacelift_scrape_collector_success rather than
// by failing the scrape, so that a Prometheus target stays up and the operator
// can see precisely which subsystem is broken.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()

	ch <- prometheus.MustNewConstMetric(e.buildInfo, prometheus.GaugeValue, 1)

	for _, c := range e.collectors {
		e.collect(c, ch)
	}

	ch <- prometheus.MustNewConstMetric(
		e.scrapeDuration, prometheus.GaugeValue, time.Since(start).Seconds(),
	)
}

func (e *Exporter) collect(c Collector, ch chan<- prometheus.Metric) {
	start := time.Now()

	// Wrapped so that cancel runs as soon as this collector is done rather
	// than at the end of the whole scrape.
	err := func() error {
		ctx, cancel := context.WithTimeout(e.ctx, e.scrapeTimeout)
		defer cancel()

		return c.Collect(ctx, e.client, ch)
	}()

	duration := time.Since(start)

	success, supported := 1.0, 1.0
	switch {
	case errors.Is(err, ErrNotSupported):
		supported = 0
		e.logger.Debugw("Collector is not supported on this deployment", "collector", c.Name())
	case errors.Is(err, context.DeadlineExceeded):
		success = 0
		e.logger.Errorw("Collector timed out querying the Spacelift API",
			"collector", c.Name(), "timeout", e.scrapeTimeout)
	case err != nil:
		success = 0
		e.logger.Errorw("Collector failed", "collector", c.Name(), zap.Error(err))
	}

	ch <- prometheus.MustNewConstMetric(e.collectorSuccess, prometheus.GaugeValue, success, c.Name())
	ch <- prometheus.MustNewConstMetric(e.collectorSupported, prometheus.GaugeValue, supported, c.Name())
	ch <- prometheus.MustNewConstMetric(e.collectorDuration, prometheus.GaugeValue, duration.Seconds(), c.Name())
}
