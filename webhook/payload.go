package webhook

import "strings"

// Payload is the subset of Spacelift's notification policy input that this
// receiver needs.
//
// The full document is described at
// https://docs.spacelift.io/concepts/policy/notification-policy. Fields we do
// not turn into metrics are omitted rather than parsed, so an addition upstream
// cannot break decoding.
type Payload struct {
	Account    Account     `json:"account"`
	RunUpdated *RunUpdated `json:"run_updated"`
}

// Account identifies the Spacelift account the event came from.
type Account struct {
	Name string `json:"name"`
}

// RunUpdated describes a run state change.
//
// Note that PolicyReceipts and Timing are siblings of Run rather than fields on
// it, matching the notification policy document.
type RunUpdated struct {
	Run            Run             `json:"run"`
	Stack          Stack           `json:"stack"`
	Timing         []StateTiming   `json:"timing"`
	PolicyReceipts []PolicyReceipt `json:"policy_receipts"`
}

// Run is the run whose state changed.
type Run struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Type  string `json:"type"`

	DriftDetection bool `json:"drift_detection"`

	// Changes lists the entity changes the run made, per phase. Only
	// plan-phase changes are counted, matching the Datadog integration;
	// drift-phase changes are stripped from the policy input upstream.
	Changes []EntityChange `json:"changes"`
}

// EntityChange is a single resource change detected or applied by a run.
type EntityChange struct {
	// Action is a resource change type such as "added", "changed",
	// "deleted", "create-Before-destroy-replaced" or "import".
	Action string `json:"action"`

	// Phase is "plan" or "apply".
	Phase string `json:"phase"`
}

// StateTiming is the time a run spent in one state, in nanoseconds. It mirrors
// shared.StateTiming in the Spacelift backend, which is also what the Datadog
// integration reads, so durations derived from it reconcile with Datadog and
// with billing.
type StateTiming struct {
	State string `json:"state"`

	// Duration is nanoseconds, because the backend marshals a
	// time.Duration.
	Duration int64 `json:"duration"`
}

// PolicyReceipt records the outcome of one policy evaluation against the run.
// Notification policy receipts are excluded upstream to avoid a feedback loop.
type PolicyReceipt struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Outcome string `json:"outcome"`
}

// Stack is the stack the run belongs to.
type Stack struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Space Space  `json:"space"`

	// WorkerPool is absent when the stack runs on the public worker pool,
	// so this must stay a pointer. See WorkerPoolName.
	WorkerPool *WorkerPool `json:"worker_pool"`
}

// Space is the space a stack belongs to.
type Space struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// WorkerPool is a private worker pool.
type WorkerPool struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// publicWorkerPoolName is reported for stacks with no private worker pool
// attached. The Datadog integration uses the same default, so the two agree.
const publicWorkerPoolName = "public"

// WorkerPoolName returns the worker pool label value for the stack, falling
// back to "public" when no private pool is attached.
func (s Stack) WorkerPoolName() string {
	if s.WorkerPool == nil || s.WorkerPool.Name == "" {
		return publicWorkerPoolName
	}

	return s.WorkerPool.Name
}

// terminalStates are the run states that end a run and therefore carry
// complete timing. It matches the Datadog integration's terminal set, which
// deliberately excludes CANCELED: a run that was created and cancelled never
// did any work.
var terminalStates = map[string]bool{
	"FAILED":    true,
	"FINISHED":  true,
	"DISCARDED": true,
	"STOPPED":   true,
}

// IsTerminal reports whether the run reached a state that ends it.
func (r Run) IsTerminal() bool {
	return terminalStates[strings.ToUpper(r.State)]
}

// changeTypes are the resource change classes we count, in the order they are
// emitted. Every run reports all four, including zeroes, so that a rate() over
// a quiet period returns 0 rather than no data.
var changeTypes = []string{"added", "changed", "deleted", "replaced"}

// PlanChangeCounts returns the number of plan-phase changes of each counted
// type.
//
// Matching on substrings is deliberate: the backend reports replacements as
// "create-Before-destroy-replaced" and "destroy-Before-create-replaced", and
// both are counted as "replaced". Change types outside this set (import,
// forget, no-op, read, and the Ansible outcomes) are not counted, matching the
// Datadog integration.
func (r Run) PlanChangeCounts() map[string]int {
	counts := make(map[string]int, len(changeTypes))
	for _, changeType := range changeTypes {
		counts[changeType] = 0
	}

	for _, change := range r.Changes {
		if change.Phase != "plan" {
			continue
		}

		action := strings.ToLower(change.Action)
		for _, changeType := range changeTypes {
			if strings.Contains(action, changeType) {
				counts[changeType]++
			}
		}
	}

	return counts
}

// TotalDuration returns the run's end-to-end duration in nanoseconds, as the
// sum of the time spent in every state.
//
// This is the same quantity as shared.RunTiming.Total() in the backend. The
// terminal state itself contributes nothing, because a terminal state never
// ends.
func (r RunUpdated) TotalDuration() int64 {
	var total int64
	for _, timing := range r.Timing {
		total += timing.Duration
	}

	return total
}
