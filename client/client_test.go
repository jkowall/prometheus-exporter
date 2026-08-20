package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hasura/go-graphql-client"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// legacyClient deliberately implements only the original three-argument API.
// Keeping this assignment compiling protects external mocks and wrappers from
// an accidental interface break.
type legacyClient struct{}

func (legacyClient) Query(context.Context, interface{}, map[string]interface{}) error { return nil }

var _ client.Client = legacyClient{}

type retrySession struct {
	mutex        sync.Mutex
	endpoint     string
	token        string
	refreshCount int
	refreshErr   error
}

func (s *retrySession) BearerToken(context.Context) (string, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return s.token, nil
}

func (s *retrySession) Endpoint() string { return s.endpoint }

func (s *retrySession) RefreshToken(context.Context) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.refreshCount++
	if s.refreshErr != nil {
		return s.refreshErr
	}
	s.token = "fresh"

	return nil
}

func (s *retrySession) refreshes() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return s.refreshCount
}

type recordedRequest struct {
	authorization string
	operationName string
}

// newRecordingServer is the GraphQL stand-in shared by the retry tests. It
// records every request's Authorization header and envelope operationName,
// answers "Bearer stale" with an unauthorized error (after calling onStale, if
// given, so a test can synchronize concurrent stale requests), and anything
// else with a viewer payload.
func newRecordingServer(t *testing.T, onStale func()) (*httptest.Server, func() []recordedRequest) {
	t.Helper()

	var mutex sync.Mutex
	var requests []recordedRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			OperationName string `json:"operationName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Errorf("decoding request: %v", err)
			return
		}

		authorization := r.Header.Get("Authorization")
		mutex.Lock()
		requests = append(requests, recordedRequest{
			authorization: authorization,
			operationName: envelope.OperationName,
		})
		mutex.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if authorization == "Bearer stale" {
			if onStale != nil {
				onStale()
			}
			_, _ = fmt.Fprint(w, `{"errors":[{"message":"unauthorized"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":{"viewer":{"id":"viewer"}}}`)
	}))
	t.Cleanup(server.Close)

	recorded := func() []recordedRequest {
		mutex.Lock()
		defer mutex.Unlock()

		return append([]recordedRequest(nil), requests...)
	}

	return server, recorded
}

func TestQueryRetryUsesRefreshedAuthorizationAndPreservesOperationName(t *testing.T) {
	server, recorded := newRecordingServer(t, nil)

	session := &retrySession{endpoint: server.URL, token: "stale"}
	var query struct {
		Viewer struct {
			ID graphql.ID
		}
	}

	if err := client.NewNamed(server.Client(), session).QueryNamed(
		context.Background(), &query, nil, "WorkerPools",
	); err != nil {
		t.Fatalf("QueryNamed() error = %v", err)
	}

	wantRequests := []recordedRequest{
		{authorization: "Bearer stale", operationName: "PrometheusExporterWorkerPools"},
		{authorization: "Bearer fresh", operationName: "PrometheusExporterWorkerPools"},
	}
	if gotRequests := recorded(); !reflect.DeepEqual(gotRequests, wantRequests) {
		t.Fatalf("requests = %#v, want %#v", gotRequests, wantRequests)
	}
	if session.refreshes() != 1 {
		t.Fatalf("RefreshToken() calls = %d, want 1", session.refreshes())
	}
	if query.Viewer.ID != "viewer" {
		t.Fatalf("decoded viewer ID = %q, want viewer", query.Viewer.ID)
	}
}

func TestConcurrentUnauthorizedResponsesRefreshOnlyOnce(t *testing.T) {
	const queryCount = 4

	staleArrived := make(chan struct{}, queryCount)
	releaseStale := make(chan struct{})
	server, recorded := newRecordingServer(t, func() {
		staleArrived <- struct{}{}
		<-releaseStale
	})

	session := &retrySession{endpoint: server.URL, token: "stale"}
	api := client.NewNamed(server.Client(), session)
	errorsByQuery := make(chan error, queryCount)
	var waitGroup sync.WaitGroup
	waitGroup.Add(queryCount)
	for range queryCount {
		go func() {
			defer waitGroup.Done()
			var query struct {
				Viewer struct{ ID graphql.ID }
			}
			errorsByQuery <- api.QueryNamed(context.Background(), &query, nil, "WorkerPools")
		}()
	}

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for range queryCount {
		select {
		case <-staleArrived:
		case <-timer.C:
			close(releaseStale)
			waitGroup.Wait()
			t.Fatal("not every concurrent query reached the server with the stale token")
		}
	}
	close(releaseStale)
	waitGroup.Wait()
	close(errorsByQuery)
	for err := range errorsByQuery {
		if err != nil {
			t.Errorf("QueryNamed() error = %v", err)
		}
	}

	if session.refreshes() != 1 {
		t.Fatalf("RefreshToken() calls = %d, want 1", session.refreshes())
	}
	gotRequests := recorded()
	if len(gotRequests) != 2*queryCount {
		t.Fatalf("HTTP requests = %d, want %d", len(gotRequests), 2*queryCount)
	}
	stale, fresh := 0, 0
	for _, request := range gotRequests {
		if request.operationName != "PrometheusExporterWorkerPools" {
			t.Errorf("operation name = %q, want PrometheusExporterWorkerPools", request.operationName)
		}
		switch request.authorization {
		case "Bearer stale":
			stale++
		case "Bearer fresh":
			fresh++
		default:
			t.Errorf("unexpected Authorization header %q", request.authorization)
		}
	}
	if stale != queryCount || fresh != queryCount {
		t.Fatalf("request tokens: stale=%d fresh=%d, want %d each", stale, fresh, queryCount)
	}
}

func TestQueryReturnsRefreshFailureWithoutRetrying(t *testing.T) {
	server, recorded := newRecordingServer(t, nil)

	refreshErr := errors.New("credential exchange failed")
	session := &retrySession{
		endpoint:   server.URL,
		token:      "stale",
		refreshErr: refreshErr,
	}
	var query struct {
		Viewer struct{ ID graphql.ID }
	}

	// The legacy three-argument Query is used deliberately: it must keep the
	// bare "PrometheusExporter" operation name so existing APM attribution
	// stays stable for anyone still on that API.
	err := client.New(server.Client(), session).Query(context.Background(), &query, nil)
	if err == nil || !strings.Contains(err.Error(), refreshErr.Error()) {
		t.Fatalf("Query() error = %v, want refresh failure", err)
	}
	gotRequests := recorded()
	if len(gotRequests) != 1 {
		t.Fatalf("HTTP requests = %d, want 1", len(gotRequests))
	}
	if got := gotRequests[0].operationName; got != "PrometheusExporter" {
		t.Fatalf("legacy operation name = %q, want PrometheusExporter", got)
	}
}
