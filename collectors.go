package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/spacelift-io/prometheus-exporter/client"
	"github.com/spacelift-io/prometheus-exporter/client/session"
	"github.com/spacelift-io/prometheus-exporter/collector"
	"github.com/spacelift-io/prometheus-exporter/logging"
)

// defaultCollectors are enabled unless --collector=no-<name> says otherwise.
//
// A collector whose data is deployment- or tier-gated should be added here as
// off by default, so that a SaaS account does not permanently report it as
// unsupported.
var defaultCollectors = []string{"publicworkerpool", "workerpools", "usage", "aggregates"}

// newCollectors builds the enabled collector set.
func newCollectors(enabled map[string]bool) ([]collector.Collector, error) {
	available := map[string]func() collector.Collector{
		"publicworkerpool": func() collector.Collector { return collector.NewPublicWorkerPool() },
		"workerpools":      func() collector.Collector { return collector.NewWorkerPools() },
		"usage":            func() collector.Collector { return collector.NewUsage() },
		"aggregates":       func() collector.Collector { return collector.NewAggregates() },
	}

	names := make([]string, 0, len(available))
	for name := range available {
		names = append(names, name)
	}
	slices.Sort(names)

	for name := range enabled {
		if _, ok := available[name]; !ok {
			return nil, fmt.Errorf("unknown collector %q, expected one of: %s", name, strings.Join(names, ", "))
		}
	}

	// Emit in a stable order so that /metrics output does not shuffle
	// between scrapes.
	out := make([]collector.Collector, 0, len(names))
	for _, name := range names {
		on, set := enabled[name]
		if !set {
			on = slices.Contains(defaultCollectors, name)
		}

		if on {
			out = append(out, available[name]())
		}
	}

	return out, nil
}

// newExporter assembles the exporter. It performs no I/O: the machine-key
// probe is a separate, explicit call, so that constructing an exporter in a
// test does not issue a query.
func newExporter(
	ctx context.Context,
	httpClient *http.Client,
	session session.Session,
	scrapeTimeout time.Duration,
	collectors []collector.Collector,
) (*collector.Exporter, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil, errors.New("could not read build info")
	}

	return collector.New(
		ctx,
		logging.FromContext(ctx).Sugar(),
		client.New(httpClient, session),
		scrapeTimeout,
		collector.BuildInfo{Version: version, Commit: commit, GoVersion: info.GoVersion},
		collectors,
	), nil
}

// machineGatedCollectors read fields the API refuses to serve to a machine
// (API key) session.
var machineGatedCollectors = []string{"publicworkerpool", "usage", "aggregates"}

// warnIfMachineKey tells the operator at startup, rather than leaving them to
// infer it from three permanently-failing collectors, that their key cannot
// read the gated fields.
//
// This has always been a requirement — the exporter has never worked with a
// machine key for these fields — but it is documented nowhere, and the README
// asks for an "Admin key", which is both stricter and less accurate.
func warnIfMachineKey(
	ctx context.Context,
	httpClient *http.Client,
	session session.Session,
	logger *zap.SugaredLogger,
	collectors []collector.Collector,
) {
	api := client.New(httpClient, session)

	var probe struct {
		Usage struct {
			BillingPeriodStart int `graphql:"billingPeriodStart"`
		} `graphql:"usage"`
	}

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err := api.Query(probeCtx, &probe, nil, "Probe")
	if err == nil || !strings.Contains(err.Error(), "not available for machine sessions") {
		return
	}

	var affected []string
	for _, c := range collectors {
		if slices.Contains(machineGatedCollectors, c.Name()) {
			affected = append(affected, c.Name())
		}
	}

	if len(affected) == 0 {
		return
	}

	logger.Warnw(
		"This API key is a machine user, so some collectors cannot read their data. "+
			"They will report spacelift_scrape_collector_supported=0. "+
			"Create a non-machine API key to enable them, or disable them explicitly.",
		"collectors", strings.Join(affected, ", "),
	)
}
