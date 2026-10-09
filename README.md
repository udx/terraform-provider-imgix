# Terraform/OpenTofu provider: Imgix

Implementation brief. Provider not implemented or published yet.

Build `udx/imgix`, published from `udx/terraform-provider-imgix`. Manage Imgix sources through the Management API. An R2A module will consume the published provider and expose `.rabbit` YAML. Keep this provider independently usable.

## Start here

- Follow [udx/terraform-provider-ghostinspector](https://github.com/udx/terraform-provider-ghostinspector) for Go structure, state semantics, tests, generated docs, licensing, and signed registry releases. Local checkout: `/opt/sources/terraform-provider-ghostinspector`.
- R2A consumer: `/opt/sources/rabbit-automation-action`. Its `gcp-storage` module already creates buckets.
- Implement from the [Imgix API](https://docs.imgix.com/en-US/apis/management/sources); do not copy another provider without verifying its license.
- Work through PRs, required checks, and Codex/Devin review feedback. Inspect the reference repo's actual workflows and approval rules before establishing this repo's release process.

## Ownership

| Component | Owns |
| --- | --- |
| This provider | Imgix source configuration, refresh, drift, import, deployment status |
| R2A module | YAML contract, provider version, resource wiring and outputs |
| Existing GCP modules | Buckets, IAM, service accounts and origin credentials; extend separately if needed |
| Site repository | Year/environment identity, CloudFront vanity routing, release media upload, application loader and promotion |

No account/billing provisioning, bucket creation, CloudFront management, media uploading, automatic purging, or client-specific defaults in this provider.

## Provider configuration

| Field | Type | Contract |
| --- | --- | --- |
| `api_key` | sensitive string | Optional attribute; fallback `IMGIX_API_KEY`; fail if neither is set. Bearer authentication. Never log. |

Production API base: `https://api.imgix.com/api/v1`. Use an injectable HTTP client/base URL internally for tests. Source operations require the `Sources` permission; Account Admin is not a prerequisite.

## Resource: `imgix_source`

First release: GCS origins. Keep API field names. Other origin types are subsequent work, not partially supported configurations.

Root fields:

| Field | Type | Contract |
| --- | --- | --- |
| `name` | string | Required display name; distinct from delivery hostname |
| `enabled` | bool | Optional/computed; preserve remote value when omitted |
| `deployment` | single nested block | Required; fields below |
| `wait_for_deployed` | bool | Optional, default `true`; poll after deployment-triggering writes |
| `timeouts.create`, `timeouts.update`, `timeouts.delete` | duration string | Optional; proposed default `10m` each; bounded, cancellable operations |
| `id` | string | Computed source ID; import key |
| `deployment_status` | string | Computed API status; handle deploying/deployed/disabled/deleted |
| `date_deployed` | int64 | Computed Unix timestamp |
| `secure_url_token` | sensitive string | Computed signing token, never a provider API key |
| `domains` | set(string) | Computed full hostnames from Imgix subdomains plus custom domains; no scheme |

`deployment` fields:

| Field | Type | Contract |
| --- | --- | --- |
| `type` | string | Required; validate `gcs` for first release |
| `imgix_subdomains` | set(string) | Required, nonempty; bare names such as `example-gala-2026`, not full hostnames |
| `gcs_bucket` | string | Required bucket name, no `gs://` prefix |
| `gcs_access_key` | sensitive string | Required for create; optional/computed for import; HMAC access identifier |
| `gcs_secret_key` | sensitive string | Required for create; optional for import; API omits it on GET; preserve configured/prior state |
| `gcs_prefix` | string | Optional/computed origin path prefix |
| `custom_domains` | set(string) | Optional/computed full DNS names; explicit `[]` clears |
| `default_params` | map(string) | Optional/computed render defaults, e.g. `{ auto = "compress,format" }`; explicit `{}` clears |
| `secure_url_enabled` | bool | Optional/computed; do not enable implicitly on existing sources |
| `cache_ttl_behavior` | string | Optional/computed; `respect_origin`, `override_origin`, or `enforce_minimum` |
| `cache_ttl_value` | int64 | Optional/computed seconds; documented range 1800–31536000 |
| `cache_ttl_error` | int64 | Optional/computed seconds; documented range 1–31536000 |
| `crossdomain_xml_enabled` | bool | Optional/computed |
| `image_error`, `image_missing` | string | Optional/computed absolute fallback URLs |
| `image_error_append_qs`, `image_missing_append_qs` | bool | Optional/computed |
| `annotation` | string | Optional/computed deployment comment; never generate changing timestamps |
| `allows_upload` | bool | Computed only if API returns it; absent means unknown, not verified false |

Optional/computed settings omitted from configuration remain unmanaged. On create, omit unset fields and read back API defaults. On import/update, do not overwrite them with guessed defaults. Define and test an explicit clearing mechanism for nullable scalar fields before freezing the schema; Terraform null must not ambiguously mean both "unmanaged" and "clear".

Live API responses also contain `origin_cache_ttl_days`, `video_cache_ttl_value`, `robots`, `capabilities`, and `render_settings`. Preserve unmanaged settings during updates. Verify their write contracts before exposing typed fields; do not add an unvalidated JSON escape hatch.

## API and state behavior

- JSON:API requests: `POST /sources`, `GET /sources/:id`, `PATCH /sources/:id`. Preserve field placement under `data.attributes` and `deployment`; source ID is not the name.
- Import by source ID. Never silently adopt an existing source by display name. Report domain conflicts clearly.
- GET omits origin secrets. Refresh/import must not erase known secrets, resend empty credentials, or force replacement. An imported resource must support a clean plan without recovering the original secret. Explain that sensitive Terraform values still reside in state.
- PATCH only managed changes. Verify nested deployment merge/replacement behavior in a disposable source before relying on sparse patches. Preserve unmodeled fields if the API requires a complete deployment object.
- Distinguish missing from unauthorized. Remove state on confirmed 404, not 401/403 or transient errors. Handle pagination, 429/Retry-After, bounded retry, and ambiguous create responses without duplicate sources.
- Wait for the intended terminal state; a disabled source must not wait forever for `deployed`. Surface deployment failures and timeouts with the source ID, without credentials.
- **Deletion is unresolved:** published source operations list GET/POST/PATCH, not DELETE. Verify supported deletion against current API and a disposable source. Until proven, fail destroy clearly. Never silently disable, orphan, or remove state as a substitute for deletion. Document the final behavior before release.

## R2A/application contract

R2A passes source settings and secret references; consumes `id`, `domains`, `deployment_status`, and the sensitive signing token only when required. Bucket name comes from GCP configuration/output. Do not claim that declaring an Imgix source creates its origin or account.

CloudFront owns public vanity paths/subdomains and proxies to the Imgix hostname; Imgix reads GCS directly. A CloudFront vanity hostname need not be an Imgix `custom_domains` entry when origin Host/TLS uses the Imgix hostname. The consumer owns path mapping, transformation query cache keys, format negotiation, and excluding application-specific edge routing.

## Done

1. Unit/HTTP-fixture tests: serialization, validation, masked secrets, null/empty semantics, drift, pagination, retries, polling and errors.
2. Explicitly opted-in acceptance tests on disposable sources/buckets only: create, read, update, import, clean second plan, credential rotation and verified destroy behavior. Never mutate existing customer sources for tests.
3. Generated resource/provider docs and runnable HCL examples using `source = "udx/imgix"`; declare supported Terraform/OpenTofu versions.
4. Reviewed CI and signed release pipeline following GhostInspector; publish and verify registry installation before calling the provider available.
5. R2A integration is a separate change consuming a pinned published version; prove it in a feature environment before site promotion.
