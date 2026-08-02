package collector

import (
	"fmt"
	"strings"
)

// machineSessionError is the message the API returns for fields that a machine
// (API key) session may not read: `metrics`, `metricsRange`, `usage`,
// `publicWorkerPool`, `stacks`, `searchStacksSuggestions`, `Stack.state` and
// others.
//
// Matching on the message is unpleasant, but the GraphQL response carries no
// machine-readable error code, and the alternative — failing the collector — is
// worse, because a machine key is a supported way to run the exporter for the
// collectors that are not gated.
const machineSessionError = "not available for machine sessions"

// classify converts an API error into ErrNotSupported when the field is simply
// unavailable to this session, and leaves genuine failures alone.
func classify(err error, name string) error {
	if err == nil {
		return nil
	}

	if strings.Contains(err.Error(), machineSessionError) {
		return fmt.Errorf("%s: %w", name, ErrNotSupported)
	}

	return fmt.Errorf("%s: %w", name, err)
}
