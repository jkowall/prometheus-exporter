package session

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newExchangeStub is a stand-in for the apiKeyUser token exchange. The
// response for each request is produced from its 1-based sequence number, so a
// test controls exactly what the first exchange (construction) and every later
// exchange return.
func newExchangeStub(t *testing.T, respond func(seq int64) (jwt string, validUntil int64, status int)) (*httptest.Server, *int64) {
	t.Helper()

	var calls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seq := atomic.AddInt64(&calls, 1)

		jwt, validUntil, status := respond(seq)
		if status != http.StatusOK {
			w.WriteHeader(status)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":{"apiKeyUser":{"jwt":%q,"validUntil":%d}}}`, jwt, validUntil)
	}))
	t.Cleanup(server.Close)

	return server, &calls
}

// TestConcurrentBearerTokenRefreshesOnce exercises the production apiKey type
// under the exact interleaving the exporter creates: several collectors
// sharing one session, all discovering a stale token at the same instant.
//
// This is the double-checked lock in BearerToken. Every other concurrency test
// in this repository runs against a hand-rolled fake session, so without this
// test the code that actually ships is the one piece with no coverage.
func TestConcurrentBearerTokenRefreshesOnce(t *testing.T) {
	server, calls := newExchangeStub(t, func(seq int64) (string, int64, int) {
		if seq == 1 {
			// The construction-time exchange hands out a token that is
			// already stale, so every subsequent BearerToken call sees
			// isFresh() == false and enters the refresh path.
			return "stale-token", time.Now().Add(-time.Minute).Unix(), http.StatusOK
		}

		return "refreshed-token", time.Now().Add(time.Hour).Unix(), http.StatusOK
	})

	session, err := FromAPIKey(context.Background(), server.Client(), server.URL, "key-id", "key-secret")
	if err != nil {
		t.Fatalf("FromAPIKey: %v", err)
	}

	const goroutines = 32

	tokens := make([]string, goroutines)
	errs := make([]error, goroutines)

	var waitGroup sync.WaitGroup
	waitGroup.Add(goroutines)
	for i := range goroutines {
		go func() {
			defer waitGroup.Done()
			tokens[i], errs[i] = session.BearerToken(context.Background())
		}()
	}
	waitGroup.Wait()

	for i := range goroutines {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: BearerToken: %v", i, errs[i])
		}
		if tokens[i] != "refreshed-token" {
			t.Errorf("goroutine %d got token %q, want the refreshed one", i, tokens[i])
		}
	}

	// Construction plus exactly one coalesced refresh. Without the
	// double-checked lock this is up to 1+32 exchanges, each burning an API
	// call and a secret resolution.
	if got := atomic.LoadInt64(calls); got != 2 {
		t.Errorf("token exchange requests = %d, want 2 (construction + one coalesced refresh)", got)
	}
}

// TestBearerTokenRecoversAfterFailedExchange pins two properties of the
// refresh path: a failed exchange surfaces as an error to the caller rather
// than handing back the stale token, and the failure is not sticky — the next
// caller retries and succeeds.
func TestBearerTokenRecoversAfterFailedExchange(t *testing.T) {
	server, calls := newExchangeStub(t, func(seq int64) (string, int64, int) {
		switch seq {
		case 1:
			return "stale-token", time.Now().Add(-time.Minute).Unix(), http.StatusOK
		case 2:
			return "", 0, http.StatusInternalServerError
		default:
			return "refreshed-token", time.Now().Add(time.Hour).Unix(), http.StatusOK
		}
	})

	session, err := FromAPIKey(context.Background(), server.Client(), server.URL, "key-id", "key-secret")
	if err != nil {
		t.Fatalf("FromAPIKey: %v", err)
	}

	if _, err := session.BearerToken(context.Background()); err == nil {
		t.Error("BearerToken succeeded although the exchange returned 500; a stale token must not be handed out silently")
	}

	token, err := session.BearerToken(context.Background())
	if err != nil {
		t.Fatalf("BearerToken after a failed exchange: %v", err)
	}
	if token != "refreshed-token" {
		t.Errorf("token after recovery = %q, want the refreshed one", token)
	}

	if got := atomic.LoadInt64(calls); got != 3 {
		t.Errorf("token exchange requests = %d, want 3 (construction, failed refresh, successful retry)", got)
	}
}

// TestRefreshTokenSerializesExplicitRefreshes documents intended behaviour:
// unlike BearerToken, an explicit RefreshToken always exchanges. Concurrent
// callers are serialized by the mutex but not coalesced — the exporter calls
// RefreshToken only from the client's unauthorized-retry path, where each
// caller has just proven its token invalid.
func TestRefreshTokenSerializesExplicitRefreshes(t *testing.T) {
	server, calls := newExchangeStub(t, func(int64) (string, int64, int) {
		return "token", time.Now().Add(time.Hour).Unix(), http.StatusOK
	})

	session, err := FromAPIKey(context.Background(), server.Client(), server.URL, "key-id", "key-secret")
	if err != nil {
		t.Fatalf("FromAPIKey: %v", err)
	}

	const goroutines = 8

	var waitGroup sync.WaitGroup
	waitGroup.Add(goroutines)
	for range goroutines {
		go func() {
			defer waitGroup.Done()
			if err := session.RefreshToken(context.Background()); err != nil {
				t.Errorf("RefreshToken: %v", err)
			}
		}()
	}
	waitGroup.Wait()

	if got := atomic.LoadInt64(calls); got != 1+goroutines {
		t.Errorf("token exchange requests = %d, want %d (construction + one per explicit refresh)", got, 1+goroutines)
	}
}
