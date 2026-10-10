resource "imgix_source" "media" {
  name = "example.com media (production)"

  deployment = {
    type             = "gcs"
    imgix_subdomains = ["example-media"]
    gcs_bucket       = "media.example.com"
    gcs_access_key   = var.gcs_hmac_access_key
    gcs_secret_key   = var.gcs_hmac_secret_key

    default_params = { auto = "compress,format" }

    cache_ttl_behavior = "respect_origin"
    cache_ttl_value    = 31536000
    image_missing      = "https://example.com/assets/missing.png"
  }
}
