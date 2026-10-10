package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/udx/terraform-provider-imgix/internal/imgix"
)

// Acceptance tests drive a real tofu CLI against a locally built provider
// binary (dev_overrides). resource.Test is deliberately not used: the Imgix
// API cannot delete sources (verified by TestAccSourceDeletionProbe), so the
// harness' post-test destroy would always fail. These tests assert the
// destroy error explicitly and clean state with `tofu state rm`; the
// disposable tf-acc-* sources they create are logged for dashboard cleanup.
//
// Required env: IMGIX_API_KEY, IMGIX_TEST_GCS_BUCKET,
// IMGIX_TEST_GCS_ACCESS_KEY, IMGIX_TEST_GCS_SECRET_KEY (a disposable bucket
// and HMAC pair — never an existing customer source or bucket).

var (
	accOnce       sync.Once
	accBinaryDir  string
	accCLIRCFPath string
	accSetupErr   error
)

func testAccPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance test (set TF_ACC=1)")
	}
	for _, v := range []string{"IMGIX_API_KEY", "IMGIX_TEST_GCS_BUCKET", "IMGIX_TEST_GCS_ACCESS_KEY", "IMGIX_TEST_GCS_SECRET_KEY"} {
		if os.Getenv(v) == "" {
			t.Fatalf("%s must be set for acceptance tests", v)
		}
	}
}

func testAccSetup(t *testing.T) string {
	t.Helper()
	testAccPreCheck(t)
	accOnce.Do(func() {
		tofuPath := os.Getenv("TF_ACC_TERRAFORM_PATH")
		if tofuPath == "" {
			var err error
			tofuPath, err = exec.LookPath("tofu")
			if err != nil {
				accSetupErr = fmt.Errorf("tofu binary not found: %w", err)
				return
			}
		}
		accBinaryDir, accSetupErr = os.MkdirTemp("", "tf-imgix-acc-bin")
		if accSetupErr != nil {
			return
		}
		build := exec.Command("go", "build", "-o", filepath.Join(accBinaryDir, "terraform-provider-imgix"), ".")
		build.Dir = filepath.Join("..", "..") // module root (main package)
		if out, err := build.CombinedOutput(); err != nil {
			accSetupErr = fmt.Errorf("building provider: %s: %w", out, err)
			return
		}
		rc, err := os.CreateTemp("", "tf-imgix-acc-*.tfrc")
		if err != nil {
			accSetupErr = err
			return
		}
		fmt.Fprintf(rc, "provider_installation {\n  dev_overrides {\n    \"udx/imgix\" = %q\n  }\n  direct {}\n}\n", accBinaryDir)
		rc.Close()
		accCLIRCFPath = rc.Name()
	})
	if accSetupErr != nil {
		t.Fatal(accSetupErr)
	}
	return accCLIRCFPath
}

type tfWorkdir struct {
	t   *testing.T
	dir string
}

func newTFWorkdir(t *testing.T, config string) *tfWorkdir {
	t.Helper()
	testAccSetup(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	// No `tofu init`: provider dev_overrides make init unnecessary, and init
	// would query a registry that does not list udx/imgix yet.
	return &tfWorkdir{t: t, dir: dir}
}

func (w *tfWorkdir) writeConfig(config string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.dir, "main.tf"), []byte(config), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *tfWorkdir) run(args ...string) (string, error) {
	w.t.Helper()
	cmd := exec.Command("tofu", args...)
	cmd.Dir = w.dir
	cmd.Env = append(os.Environ(),
		"TF_CLI_CONFIG_FILE="+accCLIRCFPath,
		"TF_IN_AUTOMATION=1",
		"INPUT=false",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (w *tfWorkdir) mustRun(args ...string) string {
	w.t.Helper()
	out, err := w.run(args...)
	if err != nil {
		w.t.Fatalf("tofu %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// stateValues returns the state values map for imgix_source.test.
func (w *tfWorkdir) stateValues() map[string]interface{} {
	w.t.Helper()
	out := w.mustRun("show", "-json")
	var doc struct {
		Values struct {
			RootModule struct {
				Resources []struct {
					Address string                 `json:"address"`
					Values  map[string]interface{} `json:"values"`
				} `json:"resources"`
			} `json:"root_module"`
		} `json:"values"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		w.t.Fatalf("decoding state: %v", err)
	}
	for _, r := range doc.Values.RootModule.Resources {
		if r.Address == "imgix_source.test" {
			return r.Values
		}
	}
	return nil
}

// expectEmptyPlan fails the test unless tofu plan reports no changes.
func (w *tfWorkdir) expectEmptyPlan() {
	w.t.Helper()
	out, err := w.run("plan", "-no-color", "-input=false", "-detailed-exitcode")
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
		w.t.Fatalf("expected empty plan, got changes:\n%s", out)
	} else if err != nil {
		w.t.Fatalf("plan failed: %v\n%s", err, out)
	}
}

// expectDestroyFails asserts the documented destroy behavior: the API cannot
// delete sources, so destroy must fail clearly. State is then dropped with
// `tofu state rm` (the only sanctioned way to stop managing a source), and
// the source ID is logged for dashboard cleanup.
func (w *tfWorkdir) expectDestroyFails(sourceID string) {
	w.t.Helper()
	out, err := w.run("destroy", "-no-color", "-auto-approve")
	if err == nil {
		w.t.Fatalf("destroy unexpectedly succeeded — the API deletion contract changed, update the provider Delete:\n%s", out)
	}
	if !strings.Contains(out, "deletion is not supported") {
		w.t.Fatalf("destroy failed for an unexpected reason:\n%s", out)
	}
	w.mustRun("state", "rm", "imgix_source.test")
	w.t.Logf("tf-acc source %s left in the Imgix account (API cannot delete) — remove in the dashboard", sourceID)
}

func testAccConfig(subdomain, name, params string, withSecrets bool, withAnnotation bool) string {
	if !strings.HasPrefix(subdomain, "tf-acc-") {
		// Never let a test touch anything but disposable sources.
		panic(fmt.Sprintf("test subdomain %q must start with tf-acc-", subdomain))
	}
	annotation := ""
	if withAnnotation {
		annotation = `
    annotation       = "terraform-provider-imgix acceptance test"`
	}
	secrets := ""
	if withSecrets {
		secrets = fmt.Sprintf(`
    gcs_access_key   = %q
    gcs_secret_key   = %q`, os.Getenv("IMGIX_TEST_GCS_ACCESS_KEY"), os.Getenv("IMGIX_TEST_GCS_SECRET_KEY"))
	}
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
    gcs_bucket       = %[3]q%[4]s
    default_params   = %[5]s%[6]s
  }
}
`, subdomain, name, os.Getenv("IMGIX_TEST_GCS_BUCKET"), secrets, params, annotation)
}

func TestAccSourceLifecycle(t *testing.T) {
	subdomain := fmt.Sprintf("tf-acc-%d", os.Getpid())
	w := newTFWorkdir(t, testAccConfig(subdomain, "TF acceptance "+subdomain, `{ auto = "compress,format" }`, true, true))

	w.mustRun("apply", "-no-color", "-auto-approve", "-input=false")

	values := w.stateValues()
	if values == nil {
		t.Fatal("imgix_source.test not in state after apply")
	}
	if values["deployment_status"] != "deployed" {
		t.Fatalf("expected deployed, got %v", values["deployment_status"])
	}
	dep := values["deployment"].(map[string]interface{})
	if dep["default_params"].(map[string]interface{})["auto"] != "compress,format" {
		t.Fatalf("default_params not applied: %v", dep)
	}
	domains, _ := values["domains"].([]interface{})
	found := false
	for _, d := range domains {
		if d == subdomain+".imgix.net" {
			found = true
		}
	}
	if !found {
		t.Fatalf("domains must include %s.imgix.net: %v", subdomain, domains)
	}
	sourceID := values["id"].(string)

	w.expectEmptyPlan()

	// In-place update.
	w.writeConfig(testAccConfig(subdomain, "TF acceptance renamed", `{ auto = "compress,format", fit = "max" }`, true, true))
	w.mustRun("apply", "-no-color", "-auto-approve", "-input=false")
	values = w.stateValues()
	if values["name"] != "TF acceptance renamed" {
		t.Fatalf("name not updated: %v", values["name"])
	}
	if values["id"].(string) != sourceID {
		t.Fatalf("update must keep the same source ID: %s -> %s", sourceID, values["id"])
	}
	w.expectEmptyPlan()

	w.expectDestroyFails(sourceID)
}

func TestAccSourceImportCleanPlan(t *testing.T) {
	subdomain := fmt.Sprintf("tf-acc-imp-%d", os.Getpid())
	name := "TF import " + subdomain

	w := newTFWorkdir(t, testAccConfig(subdomain, name, `{}`, true, true))
	w.mustRun("apply", "-no-color", "-auto-approve", "-input=false")
	values := w.stateValues()
	sourceID := values["id"].(string)

	// Drop state, switch to a config without the origin secret, and import:
	// the imported resource must plan cleanly without recovering the secret.
	w.mustRun("state", "rm", "imgix_source.test")
	w.writeConfig(testAccConfig(subdomain, name, `{}`, false, false))
	w.mustRun("import", "-no-color", "imgix_source.test", sourceID)

	values = w.stateValues()
	if values == nil || values["id"].(string) != sourceID {
		t.Fatalf("import did not restore state: %v", values)
	}
	w.expectEmptyPlan()

	w.expectDestroyFails(sourceID)
}

// A sparse PATCH must not clobber deployment fields the provider does not
// model (e.g. robots, render_settings).
func TestAccSourceSparsePatchPreservesUnmodeled(t *testing.T) {
	subdomain := fmt.Sprintf("tf-acc-sparse-%d", os.Getpid())
	client := imgix.NewClient(os.Getenv("IMGIX_API_KEY"))

	w := newTFWorkdir(t, testAccConfig(subdomain, "TF sparse "+subdomain, `{ auto = "compress" }`, true, true))
	w.mustRun("apply", "-no-color", "-auto-approve", "-input=false")
	sourceID := w.stateValues()["id"].(string)

	modeled := map[string]bool{
		"type": true, "imgix_subdomains": true, "gcs_bucket": true, "gcs_access_key": true,
		"gcs_prefix": true, "custom_domains": true, "default_params": true,
		"secure_url_enabled": true, "cache_ttl_behavior": true, "cache_ttl_value": true,
		"cache_ttl_error": true, "crossdomain_xml_enabled": true, "image_error": true,
		"image_missing": true, "image_error_append_qs": true, "image_missing_append_qs": true,
		"annotation": true, "allows_upload": true,
	}
	unmodeled := func() map[string]interface{} {
		attrs, err := client.GetSourceAttributes(context.Background(), sourceID)
		if err != nil {
			t.Fatalf("reading raw source attributes: %v", err)
		}
		dep, _ := attrs["deployment"].(map[string]interface{})
		out := map[string]interface{}{}
		for k, v := range dep {
			if !modeled[k] {
				out[k] = v
			}
		}
		return out
	}
	before := unmodeled()

	w.writeConfig(testAccConfig(subdomain, "TF sparse "+subdomain, `{ auto = "compress,format" }`, true, true))
	w.mustRun("apply", "-no-color", "-auto-approve", "-input=false")

	after := unmodeled()
	for k, want := range before {
		if fmt.Sprintf("%v", after[k]) != fmt.Sprintf("%v", want) {
			t.Fatalf("unmodeled deployment field %q changed by sparse PATCH: was %v, now %v", k, want, after[k])
		}
	}

	w.expectDestroyFails(sourceID)
}

// Probes the real deletion contract against a disposable source: the
// published Management API documents only GET/POST/PATCH. Whatever the API
// actually does with DELETE is recorded here; the provider maps "unsupported"
// to an explicit destroy error rather than silently disabling or orphaning.
func TestAccSourceDeletionProbe(t *testing.T) {
	testAccPreCheck(t)

	ctx := context.Background()
	client := imgix.NewClient(os.Getenv("IMGIX_API_KEY"))
	subdomain := fmt.Sprintf("tf-acc-probe-%d", os.Getpid())

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
		t.Logf("VERIFIED: DELETE /sources/:id is not supported; destroy fails clearly by design. "+
			"Probe source %s (%s.imgix.net) must be removed in the dashboard.", source.ID, subdomain)
	default:
		t.Fatalf("unexpected deletion error for source %s: %v", source.ID, err)
	}
}

// Explicit empty strings clear nullable fields: image_error = "" must reach
// the API as JSON null and leave a clean plan afterwards (null = unmanaged,
// "" = clear). The API validates fallback images by rendering them, so the
// test uses the source's own deployed URL as a renderable fallback.
func TestAccSourceClearableFields(t *testing.T) {
	subdomain := fmt.Sprintf("tf-acc-clear-%d", os.Getpid())
	client := imgix.NewClient(os.Getenv("IMGIX_API_KEY"))

	configFor := func(imageErrorLine string) string {
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
  name = "TF clear %[1]s"

  deployment = {
    type             = "gcs"
    imgix_subdomains = [%[1]q]
    gcs_bucket       = %[2]q
    gcs_access_key   = %[3]q
    gcs_secret_key   = %[4]q%[5]s
  }
}
`, subdomain, os.Getenv("IMGIX_TEST_GCS_BUCKET"),
			os.Getenv("IMGIX_TEST_GCS_ACCESS_KEY"), os.Getenv("IMGIX_TEST_GCS_SECRET_KEY"), imageErrorLine)
	}

	getImageError := func(sourceID string) interface{} {
		attrs, err := client.GetSourceAttributes(context.Background(), sourceID)
		if err != nil {
			t.Fatalf("reading raw source attributes: %v", err)
		}
		dep, _ := attrs["deployment"].(map[string]interface{})
		return dep["image_error"]
	}

	// Phase 1: create without a fallback image.
	w := newTFWorkdir(t, configFor(""))
	w.mustRun("apply", "-no-color", "-auto-approve", "-input=false")
	values := w.stateValues()
	sourceID := values["id"].(string)
	if got := getImageError(sourceID); got != nil {
		t.Fatalf("image_error must be null without configuration, got %v", got)
	}

	// Phase 2: set a renderable fallback (this source serves test.png).
	selfURL := fmt.Sprintf("https://%s.imgix.net/test.png", subdomain)
	w.writeConfig(configFor(fmt.Sprintf(`
    image_error      = %q`, selfURL)))
	w.mustRun("apply", "-no-color", "-auto-approve", "-input=false")
	if got := getImageError(sourceID); got != selfURL {
		t.Fatalf("image_error not applied: %v", got)
	}
	if got := w.stateValues()["deployment"].(map[string]interface{})["image_error"]; got != selfURL {
		t.Fatalf("state must hold the configured fallback, got %v", got)
	}

	// Phase 3: "" clears to JSON null on the API and null in state.
	w.writeConfig(configFor(`
    image_error      = ""`))
	w.mustRun("apply", "-no-color", "-auto-approve", "-input=false")
	if got := getImageError(sourceID); got != nil {
		t.Fatalf("image_error must be null after clearing, got %v", got)
	}
	values = w.stateValues()
	// The "" sentinel is kept in state (not null): it is what makes the
	// post-clear plan agree with the config, while the API holds null.
	v, present := values["deployment"].(map[string]interface{})["image_error"]
	s, isString := v.(string)
	if !present || v == nil || !isString || s != "" {
		t.Fatalf("state must hold the empty-string sentinel for the cleared field, got %#v", v)
	}
	w.expectEmptyPlan()

	w.expectDestroyFails(sourceID)
}
