package collector

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// WithCacheTTL bounds how often c queries the API: at most once per ttl,
// however often the exporter is scraped and by however many scrapers. Between
// requests every scrape is served the outcome of the last one. A ttl of zero
// or less returns c unchanged, so the default behaviour is untouched.
//
// It is meant for collectors whose data moves slowly and is expensive to
// compute server-side, such as aggregates, whose `metrics` fields are
// account-wide analytics queries.
func WithCacheTTL(c Collector, ttl time.Duration) Collector {
	if ttl <= 0 {
		return c
	}

	return &cachingCollector{Collector: c, ttl: ttl, now: time.Now}
}

// cachingCollector wraps a collector with a TTL cache of its last outcome.
type cachingCollector struct {
	Collector
	ttl time.Duration
	now func() time.Time

	// mutex makes the cache single-flight: scrapes that arrive while a
	// request is in flight wait for it and are served its outcome, rather
	// than each issuing their own.
	mutex sync.Mutex
	entry *cacheEntry
}

// cacheEntry is the outcome of one API request.
type cacheEntry struct {
	metrics   []prometheus.Metric
	err       error
	duration  time.Duration
	fetchedAt time.Time
}

// Collect implements Collector for callers that do not need to know whether
// the outcome was cached.
func (c *cachingCollector) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	entry, _ := c.collectCached(ctx, api)

	return entry.metrics, entry.err
}

// collectCached returns the cached outcome while it is younger than the TTL
// and queries the API otherwise, along with the age of the outcome served.
//
// Errors are cached like successes. Otherwise a collector that keeps failing,
// for instance on a server-side timeout, would query the API on every scrape,
// which is exactly the load the TTL exists to bound.
func (c *cachingCollector) collectCached(ctx context.Context, api client.Client) (cacheEntry, time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.entry != nil {
		if age := c.now().Sub(c.entry.fetchedAt); age < c.ttl {
			return *c.entry, age
		}
	}

	start := c.now()
	metrics, err := c.Collector.Collect(ctx, api)
	c.entry = &cacheEntry{metrics: metrics, err: err, duration: c.now().Sub(start), fetchedAt: start}

	return *c.entry, 0
}
