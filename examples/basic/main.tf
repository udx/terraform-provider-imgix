terraform {
  required_providers {
    imgix = {
      source = "udx/imgix"
    }
  }
}

provider "imgix" {
  # or export IMGIX_API_KEY
  api_key = var.imgix_api_key
}

variable "imgix_api_key" {
  type      = string
  sensitive = true
  default   = null
}

variable "gcs_hmac_access_key" {
  type      = string
  sensitive = true
}

variable "gcs_hmac_secret_key" {
  type      = string
  sensitive = true
}

resource "imgix_source" "media" {
  name = "example.com media (production)"

  deployment = {
    type             = "gcs"
    imgix_subdomains = ["example-media"]
    gcs_bucket       = "media.example.com"
    gcs_access_key   = var.gcs_hmac_access_key
    gcs_secret_key   = var.gcs_hmac_secret_key

    default_params = { auto = "compress,format" }
  }
}

output "media_domains" {
  value = imgix_source.media.domains
}

output "media_source_id" {
  value = imgix_source.media.id
}
