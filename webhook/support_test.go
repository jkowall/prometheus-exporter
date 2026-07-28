package webhook

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/expfmt"
)

// fixedTime is a stand-in clock so the last-delivery gauge is deterministic.
func fixedTime() time.Time {
	return time.Unix(1753000200, 0).UTC()
}

// gatherText renders a registry as exposition-format text.
func gatherText(t *testing.T, registry *prometheus.Registry) string {
	t.Helper()

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}

	var out strings.Builder
	encoder := expfmt.NewEncoder(&out, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, family := range families {
		if err := encoder.Encode(family); err != nil {
			t.Fatalf("encoding metric family %q: %v", family.GetName(), err)
		}
	}

	return out.String()
}

// deliveryResults are the outcomes reported by
// spacelift_webhook_deliveries_total, in the order the exposition format sorts
// them.
var deliveryResults = []string{
	resultAccepted,
	resultBadSignature,
	resultIgnored,
	resultMalformed,
	resultMethodNotAllowed,
}

// assertDeliveryResult checks spacelift_webhook_deliveries_total: the named
// outcome must equal want, and every other outcome must be 0. Operators depend
// on these to tell "no runs happened" apart from "deliveries are being
// rejected", so asserting the whole family catches misattribution.
func assertDeliveryResult(t *testing.T, registry *prometheus.Registry, result string, want int) {
	t.Helper()

	var expected strings.Builder
	expected.WriteString("\n# HELP spacelift_webhook_deliveries_total Total number of webhook deliveries received, by outcome.\n")
	expected.WriteString("# TYPE spacelift_webhook_deliveries_total counter\n")

	for _, candidate := range deliveryResults {
		value := 0
		if candidate == result {
			value = want
		}

		expected.WriteString(`spacelift_webhook_deliveries_total{result="` + candidate + `"} ` + strconv.Itoa(value) + "\n")
	}

	if err := testutil.GatherAndCompare(registry, strings.NewReader(expected.String()), "spacelift_webhook_deliveries_total"); err != nil {
		t.Error(err)
	}
}
