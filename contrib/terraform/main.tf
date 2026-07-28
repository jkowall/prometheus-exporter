locals {
  policy_body = file("${path.module}/../notification-policy.rego")
}

# The notification policy decides which runs are forwarded and projects the
# payload down to the fields the exporter turns into metrics.
resource "spacelift_policy" "prometheus_metrics" {
  name     = var.integration_name
  type     = "NOTIFICATION"
  space_id = var.space_id
  body     = local.policy_body

  labels = ["prometheus"]
}

# The endpoint the policy delivers to. The "prometheus" label is what the policy
# matches on, so several exporters can be fed from one policy by giving each
# endpoint the same label.
resource "spacelift_named_webhook" "prometheus_metrics" {
  name     = var.integration_name
  space_id = var.space_id
  endpoint = var.exporter_endpoint
  enabled  = true

  # Spacelift signs each delivery with an HMAC of this secret, which the
  # exporter verifies via the X-Signature-256 header. Pass the same value to
  # the exporter's --webhook-secret / --webhook-secret-file.
  #
  # secret_wo keeps the value out of Terraform state. It needs
  # Terraform 1.11+ or OpenTofu 1.10+; on older versions use the deprecated
  # `secret` attribute instead.
  secret_wo         = var.webhook_secret
  secret_wo_version = var.webhook_secret_version

  # A restarting or briefly unavailable exporter should not silently lose a
  # run. Spacelift retries up to three times on a 5xx response.
  retry_on_failure = true

  labels = ["prometheus"]
}
