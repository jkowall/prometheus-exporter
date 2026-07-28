package webhook

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Delivery outcomes reported by spacelift_webhook_deliveries_total. Operators
// need these to tell "no runs happened" apart from "deliveries are being
// rejected".
const (
	resultAccepted         = "accepted"
	resultBadSignature     = "bad_signature"
	resultMalformed        = "malformed"
	resultIgnored          = "ignored"
	resultMethodNotAllowed = "method_not_allowed"
)

// DefaultBuckets are the histogram buckets for run duration, in seconds.
//
// The Prometheus defaults top out at 10s, which is useless for IaC runs: a
// Terraform plan is routinely minutes and a large apply can be an hour. These
// span 10s to 2h with roughly even coverage on a log scale.
var DefaultBuckets = []float64{10, 30, 60, 120, 300, 600, 900, 1800, 2700, 3600, 7200}

// Metrics holds the metric families fed by incoming webhook deliveries.
//
// Unlike the scrape-time collector, these are real counters and histograms
// accumulated between scrapes, because each delivery is an event that must not
// be lost. Consequences, documented in the README: values reset when the
// exporter restarts (rate() and increase() handle this), and with more than one
// replica each instance holds only the deliveries routed to it, so queries must
// aggregate across instances.
type Metrics struct {
	runs            *prometheus.CounterVec
	runDuration     *prometheus.HistogramVec
	resourceChanges *prometheus.CounterVec
	policies        *prometheus.CounterVec

	deliveries    *prometheus.CounterVec
	lastDelivery  prometheus.Gauge
	payloadErrors prometheus.Counter
}

// NewMetrics registers the webhook metric families on the given registerer.
func NewMetrics(registerer prometheus.Registerer, buckets []float64) *Metrics {
	if len(buckets) == 0 {
		buckets = DefaultBuckets
	}

	m := &Metrics{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "spacelift_runs_total",
			Help: "Total number of runs that reached a terminal state, by stack and outcome.",
		}, []string{"space", "stack", "run_type", "final_state", "drift_detection", "worker_pool"}),

		runDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "spacelift_run_duration_seconds",
			Help: "End-to-end duration of terminated runs, as the sum of time spent in every run state. " +
				"Per-phase breakdowns are not available here; use the OpenTelemetry trace export for those.",
			Buckets: buckets,
		}, []string{"space", "stack", "run_type"}),

		resourceChanges: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "spacelift_run_resource_changes_total",
			Help: "Total number of plan-phase resource changes by change type. " +
				"Import, forget, no-op, read and Ansible outcomes are not counted.",
		}, []string{"space", "stack", "run_type", "change_type"}),

		policies: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "spacelift_run_policy_evaluations_total",
			Help: "Total number of policy evaluations recorded against terminated runs, by type and outcome.",
		}, []string{"space", "stack", "policy_type", "policy_outcome"}),

		deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "spacelift_webhook_deliveries_total",
			Help: "Total number of webhook deliveries received, by outcome.",
		}, []string{"result"}),

		lastDelivery: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "spacelift_webhook_last_delivery_timestamp_seconds",
			Help: "Unix timestamp of the last accepted webhook delivery. Alert on this going stale to catch a broken notification policy.",
		}),

		payloadErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "spacelift_webhook_payload_errors_total",
			Help: "Total number of deliveries that could not be decoded.",
		}),
	}

	registerer.MustRegister(
		m.runs,
		m.runDuration,
		m.resourceChanges,
		m.policies,
		m.deliveries,
		m.lastDelivery,
		m.payloadErrors,
	)

	// Initialise the delivery outcomes so that a rate() over them returns 0
	// rather than no data before the first delivery arrives.
	for _, result := range []string{
		resultAccepted, resultBadSignature, resultMalformed, resultIgnored, resultMethodNotAllowed,
	} {
		m.deliveries.WithLabelValues(result)
	}

	return m
}

// nanosecondsPerSecond converts the backend's nanosecond durations into the
// seconds Prometheus expects as a base unit.
const nanosecondsPerSecond = 1e9

// RecordRun turns one terminated run into observations across every family.
func (m *Metrics) RecordRun(payload *Payload) {
	run := payload.RunUpdated.Run
	stack := payload.RunUpdated.Stack

	space := stack.Space.ID
	workerPool := stack.WorkerPoolName()
	driftDetection := "false"
	if run.DriftDetection {
		driftDetection = "true"
	}

	m.runs.WithLabelValues(space, stack.ID, run.Type, run.State, driftDetection, workerPool).Inc()

	if duration := payload.RunUpdated.TotalDuration(); duration > 0 {
		m.runDuration.
			WithLabelValues(space, stack.ID, run.Type).
			Observe(float64(duration) / nanosecondsPerSecond)
	}

	for changeType, count := range run.PlanChangeCounts() {
		m.resourceChanges.
			WithLabelValues(space, stack.ID, run.Type, changeType).
			Add(float64(count))
	}

	for _, receipt := range payload.RunUpdated.PolicyReceipts {
		m.policies.
			WithLabelValues(space, stack.ID, receipt.Type, receipt.Outcome).
			Inc()
	}
}

func (m *Metrics) recordDelivery(result string) {
	m.deliveries.WithLabelValues(result).Inc()
}

func (m *Metrics) recordPayloadError() {
	m.payloadErrors.Inc()
}

func (m *Metrics) recordDeliveryTimestamp(seconds float64) {
	m.lastDelivery.Set(seconds)
}
