output "policy_id" {
  value       = spacelift_policy.prometheus_metrics.id
  description = "ID of the notification policy forwarding runs to the exporter."
}

output "webhook_id" {
  value       = spacelift_named_webhook.prometheus_metrics.id
  description = "ID of the named webhook the policy delivers to. Use it to inspect delivery history in the Spacelift UI."
}
