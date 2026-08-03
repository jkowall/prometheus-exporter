package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/spacelift-io/prometheus-exporter/client"
	"github.com/spacelift-io/prometheus-exporter/client/session"
	"github.com/spacelift-io/prometheus-exporter/collector"
	"github.com/spacelift-io/prometheus-exporter/logging"
)

type machineProbe func(context.Context, client.NamedClient) error

type collectorSpec struct {
	name           string
	defaultEnabled bool
	build          func() collector.Collector
	machineProbe   machineProbe
}

// collectorSpecs is sorted by name so collector execution, logging and help
// output remain stable. A deployment- or tier-specific collector should be off
// by default so unsupported deployments do not report it permanently.
var collectorSpecs = []collectorSpec{
	{name: "aggregates", defaultEnabled: true, build: func() collector.Collector {
		return collector.NewAggregates()
	}, machineProbe: probeAggregates},
	{name: "publicworkerpool", defaultEnabled: true, build: func() collector.Collector {
		return collector.NewPublicWorkerPool()
	}, machineProbe: probePublicWorkerPool},
	{name: "usage", defaultEnabled: true, build: func() collector.Collector {
		return collector.NewUsage()
	}, machineProbe: probeUsage},
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

// newExporter assembles the exporter. It performs no I/O: the machine-key
// probe is a separate, explicit call, so that constructing an exporter in a
// test does not issue a query.
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

func machineProbeFor(name string) machineProbe {
	for _, spec := range collectorSpecs {
		if spec.name == name {
			return spec.machineProbe
		}
	}

	return nil
}

func probeAggregates(ctx context.Context, api client.NamedClient) error {
	var probe struct {
		Metrics struct {
			StacksCountByState []struct {
				Value float64 `graphql:"value"`
			} `graphql:"stacksCountByState"`
		} `graphql:"metrics"`
	}

	return api.QueryNamed(ctx, &probe, nil, "Probe")
}

func probePublicWorkerPool(ctx context.Context, api client.NamedClient) error {
	var probe struct {
		PublicWorkerPool struct {
			Parallelism int `graphql:"parallelism"`
		} `graphql:"publicWorkerPool"`
	}

	return api.QueryNamed(ctx, &probe, nil, "Probe")
}

func probeUsage(ctx context.Context, api client.NamedClient) error {
	var probe struct {
		Usage struct {
			BillingPeriodStart int `graphql:"billingPeriodStart"`
		} `graphql:"usage"`
	}

	return api.QueryNamed(ctx, &probe, nil, "Probe")
}

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
	var affected []string
	var probe machineProbe
	for _, c := range collectors {
		if candidate := machineProbeFor(c.Name()); candidate != nil {
			affected = append(affected, c.Name())
			if probe == nil {
				probe = candidate
			}
		}
	}

	if probe == nil {
		return
	}

	api := client.NewNamed(httpClient, session)

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err := probe(probeCtx, api)
	if err == nil || !strings.Contains(err.Error(), "not available for machine sessions") {
		return
	}

	logger.Warnw(
		"This API key is a machine user, so some collectors cannot read their data. "+
			"Enable partial scrapes to export the remaining metrics and mark these collectors unsupported, "+
			"or disable them explicitly.",
		"collectors", strings.Join(affected, ", "),
	)
}
