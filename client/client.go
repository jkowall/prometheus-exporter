package client

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/hasura/go-graphql-client"

	"github.com/spacelift-io/prometheus-exporter/client/session"
	"github.com/spacelift-io/prometheus-exporter/logging"
)

// operationPrefix is prepended to every collector's operation name, so that
// Spacelift can attribute API load both to the exporter as a whole and to the
// specific collector responsible. It must be applied to retries too, otherwise
// the reissued request arrives anonymous and unattributable.
const operationPrefix = "PrometheusExporter"

type client struct {
	wraps        *http.Client
	session      session.Session
	refreshMutex sync.Mutex
}

// New returns a new instance of a Spacelift Client.
func New(wraps *http.Client, session session.Session) Client {
	return &client{wraps: wraps, session: session}
}

// NewNamed returns a client that supports collector-specific GraphQL operation
// names while retaining the original Client API.
func NewNamed(wraps *http.Client, session session.Session) NamedClient {
	return &client{wraps: wraps, session: session}
}

func (c *client) Query(ctx context.Context, query interface{}, variables map[string]interface{}) error {
	return c.QueryNamed(ctx, query, variables, "")
}

func (c *client) QueryNamed(
	ctx context.Context,
	query interface{},
	variables map[string]interface{},
	operation string,
) error {
	name := graphql.OperationName(operationPrefix + operation)
	logger := logging.FromContext(ctx).Sugar()
	apiClient, token, err := c.apiClient(ctx)
	if err != nil {
		return err
	}

	err = apiClient.Query(ctx, query, variables, name)
	if err != nil && strings.Contains(err.Error(), "unauthorized") {
		logger.Warn("Server returned an unauthorized response - retrying request with a new token")
		if err := c.refreshToken(ctx, token); err != nil {
			return err
		}

		// Try again in case refreshing the token fixes the problem
		apiClient, _, err = c.apiClient(ctx)
		if err != nil {
			return err
		}

		err = apiClient.Query(ctx, query, variables, name)
	}

	return err
}

// refreshToken coalesces simultaneous unauthorized responses. If another
// request already replaced the token that failed, this request can retry with
// that token instead of exchanging the credentials again.
func (c *client) refreshToken(ctx context.Context, failedToken string) error {
	c.refreshMutex.Lock()
	defer c.refreshMutex.Unlock()

	currentToken, err := c.session.BearerToken(ctx)
	if err != nil {
		return fmt.Errorf("could not read current bearer token: %w", err)
	}
	if currentToken != failedToken {
		return nil
	}

	if err := c.session.RefreshToken(ctx); err != nil {
		return fmt.Errorf("could not refresh bearer token: %w", err)
	}

	return nil
}

func (c *client) apiClient(ctx context.Context) (*graphql.Client, string, error) {
	bearerToken, err := c.session.BearerToken(ctx)
	if err != nil {
		return nil, "", err
	}

	return graphql.NewClient(c.session.Endpoint(), c.wraps).WithRequestModifier(func(r *http.Request) {
		r.Header.Add("Spacelift-Client-Type", "prometheus-exporter")
		r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", bearerToken))
	}), bearerToken, nil
}
