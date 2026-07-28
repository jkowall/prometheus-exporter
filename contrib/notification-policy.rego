package spacelift

import rego.v1

# Notification policy that forwards terminated runs to the Spacelift Prometheus
# exporter's webhook receiver, which turns them into run metrics.
#
# Attach this policy in the space whose runs you want to measure, and label the
# named webhook pointing at the exporter with "prometheus".
#
# See contrib/terraform for a module that creates both.

terminal := {"FAILED", "FINISHED", "DISCARDED", "STOPPED"}

run_state := input.run_updated.run.state

webhook contains {"endpoint_id": endpoint_id, "payload": payload} if {
	# Send to any endpoint labelled "prometheus", so several exporters can be
	# fed from one policy.
	some endpoint in input.webhook_endpoints
	"prometheus" in endpoint.labels
	endpoint_id := endpoint.id

	# Only terminated runs carry complete timing. This is the same terminal
	# set the Datadog integration uses, and it deliberately excludes
	# CANCELED: a run that was created and cancelled never did any work.
	run_state in terminal

	payload := {
		"account": {"name": input.account.name},
		"run_updated": {
			"run": {
				"id": input.run_updated.run.id,
				"state": run_state,
				"type": input.run_updated.run.type,
				"drift_detection": input.run_updated.run.drift_detection,
				"changes": changes,
			},
			"stack": stack,
			"timing": input.run_updated.timing,
			"policy_receipts": policy_receipts,
		},
	}
}

# Only the fields the exporter turns into metrics are forwarded. The full
# notification policy input also carries commit messages, the creator's login
# and IP, and the run's environment variables; none of that belongs in a metrics
# pipeline.
changes := [{"action": change.action, "phase": change.phase} |
	some change in input.run_updated.run.changes
]

policy_receipts := [{"name": receipt.name, "type": receipt.type, "outcome": receipt.outcome} |
	some receipt in input.run_updated.policy_receipts
]

stack := object.union(
	{
		"id": input.run_updated.stack.id,
		"name": input.run_updated.stack.name,
		"space": {
			"id": input.run_updated.stack.space.id,
			"name": input.run_updated.stack.space.name,
		},
	},
	worker_pool,
)

# worker_pool is absent from the input for stacks that run on the public worker
# pool, so it is only added when present. The exporter labels those runs
# "public".
worker_pool := {"worker_pool": {
	"id": input.run_updated.stack.worker_pool.id,
	"name": input.run_updated.stack.worker_pool.name,
}} if {
	input.run_updated.stack.worker_pool
} else := {}

# Only sample terminated runs, so the policy workbench shows the inputs that
# actually produce a delivery.
sample if {
	run_state in terminal
}
