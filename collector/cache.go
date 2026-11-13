package collector

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// errorTTL caps how long a failed request is served from cache. A transient
// failure, such as an HTTP 502, then fails scrapes for at most this long
// rather than for the whole TTL.
const errorTTL = 30 * time.Second

// WithCacheTTL serves c's last outcome to every scrape, however often the
// exporter is scraped and by however many scrapers, instead of querying the
// API each time. A ttl of zero or less returns c unchanged, so the default
// behaviour is untouched.
//
// How long an outcome is served depends on what it was:
//   - a success or an unsupported result is served for ttl;
//   - a failure is served for min(ttl, 30s), and the next scrape after that
//     queries again;
//   - a failure that came with the scrape's own context ended (deadline or
//     cancellation) is not kept, so the next scrape queries again.
//
// Scrapes that arrive while a request is in flight wait for it and are
// served its outcome when it is kept. When it is not, the next one waiting
// issues its own request.
//
// It is meant for collectors whose data is slow-moving and tolerates
// staleness, such as aggregates.
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
	// request is in flight wait for it and are served its outcome when that
	// outcome is kept, rather than each issuing their own.
	mutex sync.Mutex
	entry *cacheEntry
}

// cacheEntry is the outcome of one API request, how long the request took and
// how long the outcome may be served.
type cacheEntry struct {
	metrics   []prometheus.Metric
	err       error
	duration  time.Duration
	fetchedAt time.Time
	ttl       time.Duration
}

// Collect implements Collector.
func (c *cachingCollector) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	entry, _ := c.collectCached(ctx, api)

	return entry.metrics, entry.err
}

// VersionGatedFields passes on the fields the wrapped collector declares
// through VersionGated, so a missing declared field still reads as
// unsupported once the collector is cached. It returns nil when the wrapped
// collector declares none, and an empty list matches no missing field.
func (c *cachingCollector) VersionGatedFields() []string {
	if gated, ok := c.Collector.(VersionGated); ok {
		return gated.VersionGatedFields()
	}

	return nil
}

// collectCached returns the cached outcome while it is fresh and queries the
// API otherwise, along with the age of the outcome served: 0 when this call
// issued the request, otherwise the time since the request was issued.
//
// A success or an unsupported result is kept for the full TTL. Any other
// error is kept for at most errorTTL, and not at all when the scrape's own
// context ended (deadline or cancellation) during the request: caching it
// would fail the next scrapes, whose contexts have not ended. The next
// scrape waiting behind such a request then finds no fresh entry and issues
// its own.
//
// The unsupported check classifies against the wrapped collector, so the
// fields it declares through VersionGated are honoured.
func (c *cachingCollector) collectCached(ctx context.Context, api client.Client) (cacheEntry, time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.entry != nil {
		if age := c.now().Sub(c.entry.fetchedAt); age < c.entry.ttl {
			return *c.entry, age
		}
	}

	entry := cacheEntry{fetchedAt: c.now(), ttl: c.ttl}
	entry.metrics, entry.err = c.request(ctx, api)
	entry.duration = c.now().Sub(entry.fetchedAt)
	if entry.err != nil && !errors.Is(classify(c.Collector, entry.err), ErrNotSupported) {
		if ctx.Err() != nil {
			return entry, 0
		}
		entry.ttl = min(c.ttl, errorTTL)
	}
	c.entry = &entry

	return entry, 0
}

// request runs the wrapped collector once, turning a panic into an error so
// that it is cached like any other failure instead of being retried on every
// scrape.
func (c *cachingCollector) request(ctx context.Context, api client.Client) (metrics []prometheus.Metric, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			metrics, err = nil, fmt.Errorf("collector panicked: %v", recovered)
		}
	}()

	return c.Collector.Collect(ctx, api)
}
