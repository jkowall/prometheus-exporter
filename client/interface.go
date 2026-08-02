package client

import (
	"context"
)

// Client abstracts away Spacelift's client API.
type Client interface {
	// Query executes a single GraphQL query request.
	//
	// operation names the query so Spacelift can attribute API load to a
	// specific collector rather than to the exporter as a whole. It is
	// prefixed with "PrometheusExporter", so passing "WorkerPools" sends
	// the operation name "PrometheusExporterWorkerPools".
	Query(ctx context.Context, query interface{}, variables map[string]interface{}, operation string) error
}
