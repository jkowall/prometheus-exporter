variable "exporter_endpoint" {
  type        = string
  description = "URL of the exporter's webhook receiver, e.g. https://promex.internal.example.com/webhook. Must be reachable from Spacelift."

  validation {
    condition     = startswith(var.exporter_endpoint, "https://")
    error_message = "The exporter endpoint must use HTTPS: the payload is signed, not encrypted at the application layer."
  }
}

variable "webhook_secret" {
  type        = string
  description = "Shared secret used to sign deliveries. Pass the same value to the exporter via --webhook-secret or --webhook-secret-file."
  sensitive   = true
  ephemeral   = true

  validation {
    condition     = length(var.webhook_secret) >= 32
    error_message = "Use at least 32 characters. This secret is the only thing preventing anyone who can reach the endpoint from injecting run metrics."
  }
}

variable "webhook_secret_version" {
  type        = string
  description = "Increment this whenever webhook_secret changes, so Terraform knows to push the new value."
  default     = "1"
}

variable "integration_name" {
  type        = string
  description = "Name given to the policy and the webhook."
  default     = "Prometheus metrics"
}

variable "space_id" {
  type        = string
  description = "Space to create the policy and webhook in. Runs in this space and its children are measured."
  default     = "root"
}
