package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spacelift-io/prometheus-exporter/client"
	"github.com/spacelift-io/prometheus-exporter/client/session"
	"github.com/spacelift-io/prometheus-exporter/collector"
	"github.com/spacelift-io/prometheus-exporter/logging"
)

type collectorSpec struct {
	name           string
	defaultEnabled bool
	build          func() collector.Collector
}

// collectorSpecs is sorted by name so collector execution, logging and help
// output remain stable. A deployment- or tier-specific collector should be off
// by default so unsupported deployments do not report it permanently.
var collectorSpecs = []collectorSpec{
	{name: "aggregates", defaultEnabled: true, build: func() collector.Collector {
		return collector.NewAggregates()
	}},
	{name: "publicworkerpool", defaultEnabled: true, build: func() collector.Collector {
		return collector.NewPublicWorkerPool()
	}},
	{name: "usage", defaultEnabled: true, build: func() collector.Collector {
		return collector.NewUsage()
	}},
	{name: "workerpools", defaultEnabled: true, build: func() collector.Collector {
		return collector.NewWorkerPools()
	}},
}

// newCollectors builds the enabled collector set.
func newCollectors(enabled map[string]bool) ([]collector.Collector, error) {
	available := make(map[string]collectorSpec, len(collectorSpecs))
	names := make([]string, 0, len(collectorSpecs))
	for _, spec := range collectorSpecs {
		available[spec.name] = spec
		names = append(names, spec.name)
	}

	for name := range enabled {
		if _, ok := available[name]; !ok {
			return nil, fmt.Errorf("unknown collector %q, expected one of: %s", name, strings.Join(names, ", "))
		}
	}

	// Emit in a stable order so that /metrics output does not shuffle
	// between scrapes.
	out := make([]collector.Collector, 0, len(collectorSpecs))
	for _, spec := range collectorSpecs {
		on, set := enabled[spec.name]
		if !set {
			on = spec.defaultEnabled
		}

		if on {
			out = append(out, spec.build())
		}
	}

	return out, nil
}

// newExporter assembles the exporter. It performs no I/O, so constructing one
// in a test does not issue a query.
func newExporter(
	ctx context.Context,
	httpClient *http.Client,
	session session.Session,
	scrapeTimeout time.Duration,
	collectors []collector.Collector,
	partialScrapes bool,
) (*collector.Exporter, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil, errors.New("could not read build info")
	}

	options := []collector.Option(nil)
	if partialScrapes {
		options = append(options, collector.WithPartialScrapes())
	}

	return collector.New(
		ctx,
		logging.FromContext(ctx).Sugar(),
		client.NewNamed(httpClient, session),
		scrapeTimeout,
		collector.BuildInfo{Version: version, Commit: commit, GoVersion: info.GoVersion},
		collectors,
		options...,
	), nil
}
