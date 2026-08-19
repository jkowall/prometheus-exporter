package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/urfave/cli/v3"

	"github.com/spacelift-io/prometheus-exporter/collector"
)

func collectorNames(collectors []collector.Collector) []string {
	out := make([]string, 0, len(collectors))
	for _, c := range collectors {
		out = append(out, c.Name())
	}

	return out
}

func TestNewCollectorsUsesStableDefaultsAndOverrides(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled map[string]bool
		want    []string
	}{
		{
			name: "defaults",
			want: []string{"aggregates", "publicworkerpool", "usage", "workerpools"},
		},
		{
			name:    "disable one",
			enabled: map[string]bool{"usage": false},
			want:    []string{"aggregates", "publicworkerpool", "workerpools"},
		},
		{
			name: "disable all",
			enabled: map[string]bool{
				"aggregates":       false,
				"publicworkerpool": false,
				"usage":            false,
				"workerpools":      false,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := newCollectors(test.enabled)
			if err != nil {
				t.Fatalf("newCollectors() error = %v", err)
			}
			if names := collectorNames(got); !slices.Equal(names, test.want) {
				t.Fatalf("collector names = %v, want %v", names, test.want)
			}
		})
	}
}

func TestCollectorFlagsUseNodeExporterSyntax(t *testing.T) {
	flags := collectorCLIFlags()
	if len(flags) != len(collectorSpecs) {
		t.Fatalf("collector flags = %d, want %d", len(flags), len(collectorSpecs))
	}

	for index, spec := range collectorSpecs {
		flag, ok := flags[index].(*cli.BoolWithInverseFlag)
		if !ok {
			t.Fatalf("collector flag %q has type %T, want *cli.BoolWithInverseFlag", spec.name, flags[index])
		}
		want := []string{"collector." + spec.name, "no-collector." + spec.name}
		if names := flag.Names(); !slices.Equal(names, want) {
			t.Fatalf("flag names = %v, want %v", names, want)
		}
	}
}

func TestCollectorInverseFlagDisablesCollector(t *testing.T) {
	var selection map[string]bool
	command := &cli.Command{
		Name:  "test",
		Flags: collectorCLIFlags(),
		Action: func(_ context.Context, cmd *cli.Command) error {
			selection = collectorSelection(cmd)
			return nil
		},
	}

	if err := command.Run(context.Background(), []string{"test", "--no-collector.usage"}); err != nil {
		t.Fatalf("running command: %v", err)
	}
	if selection["usage"] {
		t.Fatal("--no-collector.usage did not disable the usage collector")
	}
	for _, name := range []string{"aggregates", "publicworkerpool", "workerpools"} {
		if !selection[name] {
			t.Fatalf("default collector %q was unexpectedly disabled", name)
		}
	}
}

func TestCollectorFlagRejectsConflictingForms(t *testing.T) {
	command := &cli.Command{
		Name:      "test",
		Flags:     collectorCLIFlags(),
		Writer:    io.Discard,
		ErrWriter: io.Discard,
	}
	err := command.Run(context.Background(), []string{
		"test",
		"--collector.usage",
		"--no-collector.usage",
	})
	if err == nil {
		t.Fatal("command accepted both positive and inverse collector flags")
	}
}

type unexpectedErrorCollector struct {
	desc *prometheus.Desc
}

func (c unexpectedErrorCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c unexpectedErrorCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, 1)
	ch <- prometheus.NewInvalidMetric(c.desc, errors.New("unexpected gather error"))
}

func TestMetricsHandlerReturns500ForUnexpectedGatherError(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(unexpectedErrorCollector{
		desc: prometheus.NewDesc("test_valid_metric", "Test metric", nil, nil),
	})

	response := httptest.NewRecorder()
	newMetricsHandler(registry).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
	)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("HTTP status = %d, want %d; body:\n%s",
			response.Code, http.StatusInternalServerError, response.Body.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
