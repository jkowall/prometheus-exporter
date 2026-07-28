terraform {
  # secret_wo (write-only arguments) and ephemeral variables need
  # Terraform 1.11+ or OpenTofu 1.10+.
  required_version = ">= 1.11.0"

  required_providers {
    spacelift = {
      source  = "spacelift-io/spacelift"
      version = ">= 1.14.0"
    }
  }
}
