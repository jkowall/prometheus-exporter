package main

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

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

func TestNewCollectorsHonoursDefaultsAndOverrides(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled map[string]bool
		want    []string
	}{
		{
			name: "defaults",
			want: []string{"publicworkerpool", "workerpools", "usage", "aggregates"},
		},
		{
			name:    "disable one",
			enabled: map[string]bool{"usage": false},
			want:    []string{"publicworkerpool", "workerpools", "aggregates"},
		},
		{
			name: "disable all",
			enabled: map[string]bool{
				"publicworkerpool": false, "workerpools": false, "usage": false, "aggregates": false,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := collectorNames(newCollectors(test.enabled)); !slices.Equal(got, test.want) {
				t.Fatalf("collector names = %v, want %v", got, test.want)
			}
		})
	}
}

// TestCollectorFlagsDisableCollectors runs the real flag set through urfave/cli
// so the node_exporter idiom (--collector.<name> / --no-collector.<name>) and
// its environment variable both reach newCollectors as intended.
func TestCollectorFlagsDisableCollectors(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		env  map[string]string
		want []string
	}{
		{
			name: "defaults",
			want: []string{"publicworkerpool", "workerpools", "usage", "aggregates"},
		},
		{
			name: "inverse flag",
			args: []string{"--no-collector.usage"},
			want: []string{"publicworkerpool", "workerpools", "aggregates"},
		},
		{
			name: "environment variable",
			env:  map[string]string{"SPACELIFT_PROMEX_COLLECTOR_AGGREGATES": "false"},
			want: []string{"publicworkerpool", "workerpools", "usage"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for key, value := range test.env {
				t.Setenv(key, value)
			}

			var got []string
			command := &cli.Command{
				Name:  "test",
				Flags: collectorCLIFlags(),
				Action: func(_ context.Context, cmd *cli.Command) error {
					got = collectorNames(newCollectors(collectorSelection(cmd)))
					return nil
				},
			}
			if err := command.Run(context.Background(), append([]string{"test"}, test.args...)); err != nil {
				t.Fatalf("running command: %v", err)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("enabled collectors = %v, want %v", got, test.want)
			}
		})
	}
}

// TestCacheTTLFlags runs the cache TTL flags through urfave/cli, by flag and by
// environment variable, alongside the collector toggles they sit next to.
func TestCacheTTLFlags(t *testing.T) {
	zero := map[string]time.Duration{"publicworkerpool": 0, "workerpools": 0, "usage": 0, "aggregates": 0}
	with := func(name string, ttl time.Duration) map[string]time.Duration {
		out := maps.Clone(zero)
		out[name] = ttl

		return out
	}

	for _, test := range []struct {
		name    string
		args    []string
		env     map[string]string
		want    map[string]time.Duration
		wantErr string
	}{
		{name: "defaults to off", want: zero},
		{
			name: "flag",
			args: []string{"--collector.aggregates.cache-ttl=5m"},
			want: with("aggregates", 5*time.Minute),
		},
		{
			name: "environment variable",
			env:  map[string]string{"SPACELIFT_PROMEX_COLLECTOR_USAGE_CACHE_TTL": "1h"},
			want: with("usage", time.Hour),
		},
		{
			name: "next to the inverse toggle",
			args: []string{"--no-collector.usage", "--collector.aggregates.cache-ttl=30s"},
			want: with("aggregates", 30*time.Second),
		},
		{
			name:    "negative is rejected",
			args:    []string{"--collector.aggregates.cache-ttl=-1s"},
			wantErr: "--collector.aggregates.cache-ttl must not be negative",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for key, value := range test.env {
				t.Setenv(key, value)
			}

			var got map[string]time.Duration
			var gotErr error
			command := &cli.Command{
				Name:  "test",
				Flags: collectorCLIFlags(),
				Action: func(_ context.Context, cmd *cli.Command) error {
					got, gotErr = collectorCacheTTLs(cmd)
					return nil
				},
			}
			if err := command.Run(context.Background(), append([]string{"test"}, test.args...)); err != nil {
				t.Fatalf("running command: %v", err)
			}

			if test.wantErr != "" {
				if gotErr == nil || !strings.Contains(gotErr.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", gotErr, test.wantErr)
				}
				return
			}
			if gotErr != nil {
				t.Fatalf("collectorCacheTTLs: %v", gotErr)
			}
			if !maps.Equal(got, test.want) {
				t.Fatalf("cache TTLs = %v, want %v", got, test.want)
			}
		})
	}
}
