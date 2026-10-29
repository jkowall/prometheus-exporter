package collector

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.uber.org/zap"
)

// fakeClock is a settable clock shared by a cache and the collector it wraps,
// so a test can make a request take a known time.
type fakeClock struct {
	mutex sync.Mutex
	at    time.Time
}

func (c *fakeClock) now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return c.at
}

func (c *fakeClock) advance(d time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.at = c.at.Add(d)
}

// countingCollector counts its requests and, through the clock, takes
// requestTime to answer each one.
func countingCollector(clock *fakeClock, requestTime time.Duration, err error) (*testCollector, *atomic.Int32) {
	var requests atomic.Int32
	var c *testCollector
	c = newTestCollector("counted", func(context.Context) ([]prometheus.Metric, error) {
		requests.Add(1)
		clock.advance(requestTime)
		if err != nil {
			return nil, err
		}

		return []prometheus.Metric{prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, 1)}, nil
	})

	return c, &requests
}

func cached(c Collector, ttl time.Duration, clock *fakeClock) *cachingCollector {
	cache := WithCacheTTL(c, ttl).(*cachingCollector)
	cache.now = clock.now

	return cache
}

func TestWithCacheTTLZeroLeavesTheCollectorAlone(t *testing.T) {
	c := newTestCollector("plain", func(context.Context) ([]prometheus.Metric, error) { return nil, nil })

	for _, ttl := range []time.Duration{0, -time.Second} {
		if got := WithCacheTTL(c, ttl); got != Collector(c) {
			t.Errorf("WithCacheTTL(c, %s) wrapped the collector; a non-positive TTL must leave it as it is", ttl)
		}
	}
}

// TestCacheQueriesAtMostOncePerTTL is the point of the cache: however often it
// is scraped, the API sees one request per TTL.
func TestCacheQueriesAtMostOncePerTTL(t *testing.T) {
	clock := &fakeClock{at: time.Unix(0, 0)}
	inner, requests := countingCollector(clock, 0, nil)
	cache := cached(inner, time.Minute, clock)

	for range 4 {
		if _, err := cache.Collect(context.Background(), nil); err != nil {
			t.Fatalf("Collect: %v", err)
		}
		clock.advance(10 * time.Second)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("4 scrapes within the TTL issued %d requests, want 1", got)
	}

	clock.advance(20 * time.Second) // 60s since the request: expired.
	if _, err := cache.Collect(context.Background(), nil); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("a scrape after the TTL issued %d requests in all, want 2", got)
	}
}

// TestCacheCachesErrors keeps a failing collector from querying on every
// scrape, which is when an expensive query is most likely to be timing out.
func TestCacheCachesErrors(t *testing.T) {
	clock := &fakeClock{at: time.Unix(0, 0)}
	inner, requests := countingCollector(clock, 0, errors.New("server timeout"))
	cache := cached(inner, time.Minute, clock)

	for range 3 {
		if _, err := cache.Collect(context.Background(), nil); err == nil || err.Error() != "server timeout" {
			t.Fatalf("Collect error = %v, want the cached server timeout", err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("3 failing scrapes within the TTL issued %d requests, want 1", got)
	}
}

// TestCacheIsSingleFlight covers several scrapers arriving together, such as an
// HA pair of Prometheus servers: they share one request.
func TestCacheIsSingleFlight(t *testing.T) {
	clock := &fakeClock{at: time.Unix(0, 0)}
	var requests atomic.Int32
	release := make(chan struct{})
	inner := newTestCollector("slow", func(context.Context) ([]prometheus.Metric, error) {
		requests.Add(1)
		<-release

		return nil, nil
	})
	cache := cached(inner, time.Minute, clock)

	const scrapers = 8
	var waitGroup sync.WaitGroup
	for range scrapers {
		waitGroup.Go(func() { _, _ = cache.Collect(context.Background(), nil) })
	}
	time.Sleep(50 * time.Millisecond) // let every scraper reach the cache
	close(release)
	waitGroup.Wait()

	if got := requests.Load(); got != 1 {
		t.Fatalf("%d concurrent scrapes issued %d requests, want 1", scrapers, got)
	}
}

// gatherFamilies runs one scrape of the exporter and indexes the result by
// metric name.
func gatherFamilies(t *testing.T, exporter *Exporter) map[string]*dto.MetricFamily {
	t.Helper()

	registry := prometheus.NewPedanticRegistry()
	if err := registry.Register(exporter); err != nil {
		t.Fatalf("registering exporter: %v", err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}

	out := make(map[string]*dto.MetricFamily, len(families))
	for _, family := range families {
		out[family.GetName()] = family
	}

	return out
}

// gaugeFor returns the value of the named gauge for one collector label, and
// whether it was present.
func gaugeFor(families map[string]*dto.MetricFamily, name, collector string) (float64, bool) {
	for _, metric := range families[name].GetMetric() {
		for _, label := range metric.GetLabel() {
			if label.GetName() == "collector" && label.GetValue() == collector {
				return metric.GetGauge().GetValue(), true
			}
		}
	}

	return 0, false
}

// TestCachedScrapeReportsTruthfulHealth checks that a scrape served from cache
// reports the request that produced its data, not a fresh near-zero one, and
// how old that data is. Uncached collectors gain no cache series.
func TestCachedScrapeReportsTruthfulHealth(t *testing.T) {
	clock := &fakeClock{at: time.Unix(0, 0)}
	inner, requests := countingCollector(clock, 2*time.Second, nil)
	plain := newTestCollector("plain", func(context.Context) ([]prometheus.Metric, error) { return nil, nil })
	exporter := New(context.Background(), zap.NewNop().Sugar(), nil, time.Second, BuildInfo{},
		[]Collector{cached(inner, time.Minute, clock), plain}, false)

	assertHealth := func(scrape string, wantAge float64) {
		t.Helper()

		families := gatherFamilies(t, exporter)
		if got, _ := gaugeFor(families, "spacelift_scrape_collector_duration_seconds", "counted"); got != 2 {
			t.Errorf("%s: duration = %v, want the 2s of the request that produced the data", scrape, got)
		}
		if got, ok := gaugeFor(families, "spacelift_scrape_collector_cache_age_seconds", "counted"); !ok || got != wantAge {
			t.Errorf("%s: cache age = %v (present %v), want %v", scrape, got, ok, wantAge)
		}
		if got, _ := gaugeFor(families, "spacelift_scrape_collector_success", "counted"); got != 1 {
			t.Errorf("%s: success = %v, want 1", scrape, got)
		}
		if _, ok := gaugeFor(families, "spacelift_scrape_collector_cache_age_seconds", "plain"); ok {
			t.Errorf("%s: an uncached collector reported a cache age", scrape)
		}
		if _, ok := families["test_counted"]; !ok {
			t.Errorf("%s: the cached collector's own metric is missing", scrape)
		}
	}

	assertHealth("fresh scrape", 0)
	clock.advance(30 * time.Second)
	assertHealth("cached scrape", 32) // 2s request + 30s since

	if got := requests.Load(); got != 1 {
		t.Fatalf("two scrapes within the TTL issued %d requests, want 1", got)
	}
}

// TestUncachedExporterDoesNotDescribeCacheAge keeps the default metric surface
// unchanged: the cache series exists only once a collector has a TTL.
func TestUncachedExporterDoesNotDescribeCacheAge(t *testing.T) {
	plain := newTestCollector("plain", func(context.Context) ([]prometheus.Metric, error) { return nil, nil })
	exporter := New(context.Background(), zap.NewNop().Sugar(), nil, time.Second, BuildInfo{}, []Collector{plain}, false)

	described := make(chan *prometheus.Desc, 16)
	exporter.Describe(described)
	close(described)
	for desc := range described {
		if desc == exporter.collectorCacheAge {
			t.Fatal("an exporter without cached collectors described the cache age series")
		}
	}
}
