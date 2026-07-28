# contrib

Everything needed to feed the exporter's webhook receiver, which turns run
events into the metrics documented under
[Run metrics](../README.md#run-metrics).

| Path | What it is |
| --- | --- |
| [`notification-policy.rego`](notification-policy.rego) | The notification policy. Forwards terminated runs to any webhook endpoint labelled `prometheus`. |
| [`terraform/`](terraform) | A module that creates the policy and the webhook together. |
| [`generate-fixtures.sh`](generate-fixtures.sh) | Regenerates `testdata/policy-output-*.json` by evaluating the policy with OPA. |
| `testdata/` | Policy inputs and their evaluated output, used by the Go contract test. |

## Why the policy projects the payload

The notification policy input carries far more than the exporter needs,
including commit messages, the run creator's login and IP address, and the run's
environment variables. The policy forwards only the fields that become metrics.
This is asserted by `TestPolicyStripsSensitiveFields` in
[`../webhook/policy_contract_test.go`](../webhook/policy_contract_test.go).

## Keeping the two halves in agreement

The Rego policy and the Go structs that decode its output are separate artifacts
that have to agree. The fixtures under `testdata/` are the real output of

```bash
opa eval -d contrib/notification-policy.rego -i <input> data.spacelift.webhook
```

and the Go contract test feeds them through the actual HTTP handler. Editing the
policy without regenerating them fails CI:

```bash
go install github.com/open-policy-agent/opa@latest
./contrib/generate-fixtures.sh
```

To add a case, drop a `testdata/policy-input-<name>.json` file in and rerun that
script.

## Using the Terraform module

```hcl
module "prometheus_metrics" {
  source = "github.com/spacelift-io/prometheus-exporter//contrib/terraform"

  exporter_endpoint = "https://promex.internal.example.com/webhook"
  webhook_secret    = var.promex_webhook_secret
  space_id          = "root"
}
```

Pass the same `webhook_secret` to the exporter as `--webhook-secret` or, better,
`--webhook-secret-file`. The module uses write-only arguments so the secret stays
out of Terraform state, which needs Terraform 1.11+ or OpenTofu 1.10+.

The exporter must be reachable from Spacelift. If it runs somewhere private, put
it behind an ingress that terminates TLS and forwards only the webhook path.
