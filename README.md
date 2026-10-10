# Terraform/OpenTofu Provider: Imgix

Manage [Imgix](https://imgix.com) sources as code through the [Imgix Management API](https://docs.imgix.com/en-US/apis/management/sources). Works with both Terraform and OpenTofu.

Built for teams whose Imgix sources outgrew point-and-click management: every source is reviewable in Git, imported without churn, and drift is visible in `plan` output before anything changes.

```hcl
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
```

## Why this provider exists

Imgix sources are usually born as dashboard clicks and mutated by one-off API scripts. Both lose one or more of: state, drift detection, history of what changed, or safe handling of the origin credentials embedded in every source.

This provider models the API honestly:

- **A source is managed state.** Read normalizes what the API returns, so `plan` only shows real changes — not API defaults you never set.
- **Null means unmanaged.** Every deployment setting (`cache_ttl_behavior`, `default_params`, `image_missing`, ...) is optional/computed. Omitted fields mirror whatever the API holds, so you manage exactly the fields you care about and leave the rest to the dashboard. To clear a nullable field back to the Imgix default, set it explicitly to `""`, `[]`, or `{}`.
- **Origin secrets stay secret.** `gcs_secret_key` is never returned by the API after creation. Refresh and import preserve the known value from state instead of erasing it, and an imported source plans cleanly without recovering the original secret. (Sensitive values still reside in Terraform state — that is unavoidable.)
- **Deploys are waited on, not assumed.** Creates and deployment-affecting updates trigger an Imgix deploy; with `wait_for_deployed = true` (the default) the apply polls until the source reaches its intended terminal state and surfaces the source ID on failure or timeout. A disabled source waits for `disabled`, not `deployed`.
- **Nothing is silently orphaned.** The Imgix API cannot delete sources: destroy fails with a clear error naming the source ID, and the source is left untouched. The provider never disables a source, drops state, or reports success as a substitute for deletion. To stop managing a source, delete it in the dashboard first, then `terraform state rm`.

## What this provider manages — and what it does not

| Component | Owns |
| --- | --- |
| This provider | Imgix source configuration, refresh, drift, import, deployment status |
| Your other Terraform | Buckets, IAM, service accounts and origin credentials (e.g. GCS HMAC keys), CDN/vanity routing in front of the Imgix hostname |
| Your site/application | Uploading media, rendering URLs, cache-key and transformation design |

There is no account/billing provisioning, no bucket creation, no CloudFront/CDN management, no media uploading, and no automatic purging here. A declared Imgix source does **not** create its origin bucket — the bucket must exist with working credentials before the source will deploy.

A note on vanity domains: if your CDN (e.g. CloudFront) owns the public hostname and proxies to `*.imgix.net`, that hostname does **not** need to be an Imgix `custom_domains` entry — `custom_domains` is only for hostnames Imgix itself terminates.

## Provider configuration

| Field | Type | Contract |
| --- | --- | --- |
| `api_key` | sensitive string | Optional; falls back to `IMGIX_API_KEY`; configuration error if neither is set. Bearer authentication against `https://api.imgix.com/api/v1`. The key needs the **Sources** permission; Account Admin is not required. |

## Resource: `imgix_source`

GCS origins. Other origin types (`s3`, `azure`, `webfolder`, `webproxy`, `s3_compatible`) are rejected by schema validation for now rather than partially supported.

Root fields:

| Field | Type | Contract |
| --- | --- | --- |
| `name` | string | Required display name; distinct from the delivery hostname. |
| `enabled` | bool | Optional/computed; preserves the remote value when omitted. |
| `deployment` | single nested block | Required; fields below. |
| `wait_for_deployed` | bool | Optional, default `true`; polls after deployment-triggering writes. |
| `timeouts.create`, `timeouts.update`, `timeouts.delete` | duration string | Optional; default `10m` each. Bounded, cancellable operations. |
| `id` | string | Computed source ID; also the import key. |
| `deployment_status` | string | Computed API status: `deploying`, `deployed`, `disabled`, `deleted`. |
| `date_deployed` | int64 | Computed Unix timestamp. |
| `secure_url_token` | sensitive string | Computed signing token, when the API returns one for the source. Never a provider API key. |
| `domains` | set(string) | Computed full hostnames from the Imgix subdomains plus custom domains; no scheme. |

`deployment` fields:

| Field | Type | Contract |
| --- | --- | --- |
| `type` | string | Required; must be `gcs`. |
| `imgix_subdomains` | set(string) | Required, nonempty; bare names such as `example-gala-2026`, not full hostnames. Unique across all Imgix sources; conflicts are reported plainly. |
| `gcs_bucket` | string | Required bucket name, no `gs://` prefix. |
| `gcs_access_key` | sensitive string | Required on create; optional/computed on import. GCS HMAC access identifier. |
| `gcs_secret_key` | sensitive string | Required on create; optional on import. The API omits it on GET; the configured/prior state value is preserved. |
| `gcs_prefix` | string | Optional/computed origin path prefix. |
| `custom_domains` | set(string) | Optional/computed full DNS names Imgix terminates; explicit `[]` clears. |
| `default_params` | map(string) | Optional/computed render defaults, e.g. `{ auto = "compress,format" }`; explicit `{}` clears. |
| `secure_url_enabled` | bool | Optional/computed; never enabled implicitly on an existing source. |
| `cache_ttl_behavior` | string | Optional/computed; `respect_origin`, `override_origin`, or `enforce_minimum`. |
| `cache_ttl_value` | int64 | Optional/computed seconds; range 1800–31536000. |
| `cache_ttl_error` | int64 | Optional/computed seconds; range 1–31536000. |
| `crossdomain_xml_enabled` | bool | Optional/computed. |
| `image_error`, `image_missing` | string | Optional/computed absolute fallback URLs. |
| `image_error_append_qs`, `image_missing_append_qs` | bool | Optional/computed. |
| `annotation` | string | Optional/computed deployment comment. Write-only after creation: any deployment PATCH that omits it makes Imgix regenerate it, so the provider keeps the configured value in state and resends it with every deployment update. Never auto-generated by the provider. |
| `allows_upload` | bool | Computed; only meaningful when the API returns it — absent means unknown, not false. |

The API also returns fields this resource deliberately does not model yet (`origin_cache_ttl_days`, `video_cache_ttl_value`, `robots`, `capabilities`, `render_settings`). They are preserved on update; typed fields are only added once their write contracts are verified.

## Importing existing sources

Import by source ID (the 24-character string from the dashboard URL or `GET /sources`):

```bash
tofu import imgix_source.media 6583999a6e489be528f1729a
```

There is **no adopt-by-name**: creating a source whose subdomains already belong to another source fails with a conflict error rather than silently adopting it. After import, `gcs_secret_key` is unknown to the provider (the API never returns it) — the imported resource still plans cleanly; set the attribute if you ever need to rotate the credential.

## Semantics worth knowing

- **PATCH sends only managed changes.** Unmodeled and unmanaged fields are preserved. Managed-but-unset fields are never overwritten with guessed defaults.
- **404 and 401/403 are different.** State is removed on a confirmed 404 only; auth failures and transient errors surface as errors, never as disappearance.
- **Rate limits are respected.** The client backs off with jitter on `429`/`5xx` (honoring `Retry-After`) within bounded attempts, and deploy polling is gentle. Source create/update is limited to 60/hour by Imgix — plan accordingly.
- **Deletes are honest.** The published source operations are GET/POST/PATCH, and `DELETE /sources/:id` is verified to be unsupported: destroy fails with an explicit error naming the source, and the source remains in state and in your account. Delete it in the Imgix dashboard, then remove it from state with `terraform state rm`.

## Development

```bash
go build ./...
go test ./internal/imgix/        # unit tests (HTTP fixtures, no API key)

# acceptance tests (real API, disposable sources only — never existing ones)
TF_ACC=1 \
TF_ACC_TERRAFORM_PATH=$(which tofu) \
TF_ACC_PROVIDER_NAMESPACE=udx \
TF_ACC_PROVIDER_HOST=registry.opentofu.org \
IMGIX_API_KEY=... \
IMGIX_TEST_GCS_BUCKET=... \
IMGIX_TEST_GCS_ACCESS_KEY=... \
IMGIX_TEST_GCS_SECRET_KEY=... \
go test ./internal/provider/ -timeout 20m
```

Acceptance tests require a disposable GCS bucket and HMAC pair and create only `tf-acc-*` sources; they refuse to run against sources they did not create. Releases are cut from tags (`v*`) via GoReleaser with GPG-signed checksums, published to the Terraform Registry as `udx/imgix`.

## License

[Mozilla Public License 2.0](LICENSE)
