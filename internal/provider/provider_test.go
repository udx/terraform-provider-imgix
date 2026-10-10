package provider

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/udx/terraform-provider-imgix/internal/imgix"
)

var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"imgix": providerserver.NewProtocol6WithError(New("test")()),
}

// Acceptance tests run against the real Imgix Management API and create only
// disposable tf-acc-* sources. They require an API key plus a disposable GCS
// bucket and HMAC pair; they never touch pre-existing sources.
func testAccPreCheck(t *testing.T) {
	t.Helper()
	for _, v := range []string{"IMGIX_API_KEY", "IMGIX_TEST_GCS_BUCKET", "IMGIX_TEST_GCS_ACCESS_KEY", "IMGIX_TEST_GCS_SECRET_KEY"} {
		if os.Getenv(v) == "" {
			t.Fatalf("%s must be set for acceptance tests", v)
		}
	}
}

func testAccClient(t *testing.T) *imgix.Client {
	t.Helper()
	return imgix.NewClient(os.Getenv("IMGIX_API_KEY"))
}

func uniqueSubdomain() string {
	return fmt.Sprintf("tf-acc-%d", time.Now().Unix())
}

func testAccSourceConfig(subdomain, name, defaultParams string) string {
	return fmt.Sprintf(`
terraform {
  required_providers {
    imgix = {
      source = "udx/imgix"
    }
  }
}

provider "imgix" {}

resource "imgix_source" "test" {
  name = %[2]q

  deployment = {
    type             = "gcs"
    imgix_subdomains = [%[1]q]
    gcs_bucket       = %[3]q
    gcs_access_key   = %[4]q
    gcs_secret_key   = %[5]q
    default_params   = %[6]s
    annotation       = "terraform-provider-imgix acceptance test"
  }
}
`, subdomain, name, os.Getenv("IMGIX_TEST_GCS_BUCKET"),
		os.Getenv("IMGIX_TEST_GCS_ACCESS_KEY"), os.Getenv("IMGIX_TEST_GCS_SECRET_KEY"), defaultParams)
}

func TestAccSource_basic(t *testing.T) {
	subdomain := uniqueSubdomain()
	paramsV1 := `{ auto = "compress,format" }`
	paramsV2 := `{ auto = "compress,format", fit = "max" }`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccSourceConfig(subdomain, "TF acceptance "+subdomain, paramsV1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("imgix_source.test", "id"),
					resource.TestCheckResourceAttr("imgix_source.test", "deployment_status", "deployed"),
					resource.TestCheckResourceAttr("imgix_source.test", "enabled", "true"),
					resource.TestCheckResourceAttr("imgix_source.test", "domains.0", subdomain+".imgix.net"),
					resource.TestCheckResourceAttr("imgix_source.test", "deployment.default_params.auto", "compress,format"),
					resource.TestCheckNoResourceAttr("imgix_source.test", "secure_url_token"),
				),
			},
			// Re-apply of the same config must be empty.
			{
				Config:             testAccSourceConfig(subdomain, "TF acceptance "+subdomain, paramsV1),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				Config: testAccSourceConfig(subdomain, "TF acceptance renamed", paramsV2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("imgix_source.test", "name", "TF acceptance renamed"),
					resource.TestCheckResourceAttr("imgix_source.test", "deployment.default_params.fit", "max"),
				),
			},
			{
				Config:   testAccSourceConfig(subdomain, "TF acceptance renamed", paramsV2),
				PlanOnly: true,
			},
		},
	})
}

func TestAccSource_import(t *testing.T) {
	subdomain := uniqueSubdomain()
	config := testAccSourceConfig(subdomain, "TF import "+subdomain, `{}`)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				ResourceName:      "imgix_source.test",
				ImportState:       true,
				ImportStateVerify: true,
				// The API never returns the origin secret, and provider-only
				// attributes have no remote counterpart.
				ImportStateVerifyIgnore: []string{
					"deployment.gcs_secret_key",
					"wait_for_deployed",
					"timeouts",
				},
			},
			// An imported resource plans cleanly without the original secret.
			{
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// A sparse PATCH must not clobber fields the provider does not model (for
// example render_settings or robots). Verifies the deployment merge contract
// against a disposable source.
func TestAccSource_sparsePatchPreservesUnmodeledFields(t *testing.T) {
	subdomain := uniqueSubdomain()
	paramsV1 := `{ auto = "compress" }`
	paramsV2 := `{ auto = "compress,format" }`

	var sourceID string
	unmodeledBefore := map[string]interface{}{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccSourceConfig(subdomain, "TF sparse "+subdomain, paramsV1),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(s *terraform.State) error {
						rs := s.RootModule().Resources["imgix_source.test"]
						sourceID = rs.Primary.ID
						attrs, err := testAccClient(t).GetSourceAttributes(context.Background(), sourceID)
						if err != nil {
							return err
						}
						dep, _ := attrs["deployment"].(map[string]interface{})
						for k, v := range dep {
							switch k {
							case "type", "imgix_subdomains", "gcs_bucket", "gcs_access_key",
								"gcs_prefix", "custom_domains", "default_params",
								"secure_url_enabled", "cache_ttl_behavior", "cache_ttl_value",
								"cache_ttl_error", "crossdomain_xml_enabled", "image_error",
								"image_missing", "image_error_append_qs", "image_missing_append_qs",
								"annotation", "allows_upload":
								// modeled
							default:
								unmodeledBefore[k] = v
							}
						}
						return nil
					},
				),
			},
			{
				Config: testAccSourceConfig(subdomain, "TF sparse "+subdomain, paramsV2),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(s *terraform.State) error {
						attrs, err := testAccClient(t).GetSourceAttributes(context.Background(), sourceID)
						if err != nil {
							return err
						}
						dep, _ := attrs["deployment"].(map[string]interface{})
						for k, want := range unmodeledBefore {
							if got := dep[k]; !deepEqualish(got, want) {
								return fmt.Errorf("unmodeled deployment field %q changed by sparse PATCH: was %v, now %v", k, want, got)
							}
						}
						return nil
					},
				),
			},
		},
	})
}

// Rotating the origin HMAC pair updates the source in place; the new secret
// is preserved in state even though the API never returns it.
func TestAccSource_credentialRotation(t *testing.T) {
	access2, secret2 := os.Getenv("IMGIX_TEST_GCS_ACCESS_KEY_2"), os.Getenv("IMGIX_TEST_GCS_SECRET_KEY_2")
	if access2 == "" || secret2 == "" {
		t.Skip("IMGIX_TEST_GCS_ACCESS_KEY_2/IMGIX_TEST_GCS_SECRET_KEY_2 not set; skipping rotation test")
	}
	subdomain := uniqueSubdomain()

	rotated := fmt.Sprintf(`
terraform {
  required_providers {
    imgix = {
      source = "udx/imgix"
    }
  }
}

provider "imgix" {}

resource "imgix_source" "test" {
  name = "TF rotate %[1]s"

  deployment = {
    type             = "gcs"
    imgix_subdomains = [%[1]q]
    gcs_bucket       = %[2]q
    gcs_access_key   = %[3]q
    gcs_secret_key   = %[4]q
  }
}
`, subdomain, os.Getenv("IMGIX_TEST_GCS_BUCKET"), access2, secret2)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: testAccSourceConfig(subdomain, "TF rotate "+subdomain, `{}`)},
			{
				Config: rotated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("imgix_source.test", "deployment.gcs_access_key", access2),
					resource.TestCheckResourceAttr("imgix_source.test", "deployment.gcs_secret_key", secret2),
				),
			},
			{
				Config:             rotated,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// Probes the real deletion contract: the published API documents only
// GET/POST/PATCH for sources. Whatever the API actually does with DELETE is
// recorded here; the provider maps "unsupported" to an explicit destroy
// error rather than silently disabling or orphaning the source.
func TestAccSource_deletionProbe(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance test")
	}
	testAccPreCheck(t)

	ctx := context.Background()
	client := testAccClient(t)
	subdomain := uniqueSubdomain()

	source, err := client.CreateSource(ctx, map[string]interface{}{
		"name": "TF delete probe " + subdomain,
		"deployment": map[string]interface{}{
			"type":             "gcs",
			"imgix_subdomains": []string{subdomain},
			"gcs_bucket":       os.Getenv("IMGIX_TEST_GCS_BUCKET"),
			"gcs_access_key":   os.Getenv("IMGIX_TEST_GCS_ACCESS_KEY"),
			"gcs_secret_key":   os.Getenv("IMGIX_TEST_GCS_SECRET_KEY"),
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	err = client.DeleteSource(ctx, source.ID)
	switch {
	case err == nil:
		t.Logf("DELETE /sources/:id is supported: source %s deleted", source.ID)
	case imgix.DeletionUnsupported(err):
		t.Logf("DELETE /sources/:id is NOT supported for this account; destroy fails clearly by design. "+
			"Source %s (%s.imgix.net) must be removed in the dashboard.", source.ID, subdomain)
	default:
		t.Fatalf("unexpected deletion error for source %s: %v", source.ID, err)
	}
}

func deepEqualish(a, b interface{}) bool {
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}
