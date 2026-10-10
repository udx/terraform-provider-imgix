package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/udx/terraform-provider-imgix/internal/imgix"
)

func strPtr(s string) *string      { return &s }
func boolPtr(b bool) *bool         { return &b }
func int64Ptr(i int64) *int64      { return &i }
func strVal(s string) types.String { return types.StringValue(s) }

func testDeploymentModel() *DeploymentModel {
	subs, _ := types.SetValueFrom(context.Background(), types.StringType, []string{"example-media"})
	domains, _ := types.SetValueFrom(context.Background(), types.StringType, []string{"media.example.com"})
	params, _ := types.MapValueFrom(context.Background(), types.StringType, map[string]string{"auto": "compress,format"})
	return &DeploymentModel{
		Type:             strVal("gcs"),
		ImgixSubdomains:  subs,
		GCSBucket:        strVal("media.example.com"),
		GCSAccessKey:     strVal("AKIAEXAMPLE"),
		GCSSecretKey:     strVal("secret"),
		GCSPrefix:        types.StringNull(),
		CustomDomains:    domains,
		DefaultParams:    params,
		CacheTTLBehavior: types.StringNull(),
	}
}

func TestExpandDeploymentOmitsUnsetFields(t *testing.T) {
	m, diags := expandDeployment(context.Background(), testDeploymentModel())
	if diags.HasError() {
		t.Fatal(diags)
	}
	if m["type"] != "gcs" || m["gcs_bucket"] != "media.example.com" {
		t.Fatalf("required fields missing: %v", m)
	}
	for _, absent := range []string{"gcs_prefix", "cache_ttl_behavior", "cache_ttl_value", "image_error", "annotation", "allows_upload"} {
		if _, ok := m[absent]; ok {
			t.Fatalf("unset field %s must not be sent on create: %v", absent, m)
		}
	}
	subs := m["imgix_subdomains"].([]string)
	if len(subs) != 1 || subs[0] != "example-media" {
		t.Fatalf("subdomains not expanded: %v", m)
	}
	params := m["default_params"].(map[string]string)
	if params["auto"] != "compress,format" {
		t.Fatalf("default_params not expanded: %v", m)
	}
}

func TestDiffSourceSendsOnlyChangedManagedFields(t *testing.T) {
	plan := &SourceResourceModel{Deployment: testDeploymentModel()}
	state := &SourceResourceModel{Deployment: testDeploymentModel()}

	plan.Name = strVal("media v2")
	state.Name = strVal("media")
	plan.Deployment.CacheTTLValue = types.Int64Value(86400)
	state.Deployment.CacheTTLValue = types.Int64Value(31536000)

	patch, depChanged, diags := diffSource(context.Background(), plan, state, nil)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if patch["name"] != "media v2" {
		t.Fatalf("name change missing: %v", patch)
	}
	dep := patch["deployment"].(map[string]interface{})
	if !depChanged || len(dep) != 1 || dep["cache_ttl_value"] != int64(86400) {
		t.Fatalf("only the changed deployment field must be patched: %v", dep)
	}
}

func TestDiffSourceExplicitClearSendsNull(t *testing.T) {
	plan := &SourceResourceModel{Deployment: testDeploymentModel()}
	state := &SourceResourceModel{Deployment: testDeploymentModel()}
	plan.Name = strVal("media")
	state.Name = strVal("media")

	// Config "" on a clearable string plans as "" and is sent as JSON null;
	// state holds the remote value before clearing.
	plan.Deployment.ImageError = strVal("")
	state.Deployment.ImageError = strVal("https://cdn.example.com/error.png")

	// Unmanaged field (null config mirrored from state) must not diff.
	plan.Deployment.Annotation = types.StringNull()
	state.Deployment.Annotation = types.StringNull()

	patch, depChanged, diags := diffSource(context.Background(), plan, state, nil)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if !depChanged {
		t.Fatal("expected a deployment change")
	}
	dep := patch["deployment"].(map[string]interface{})
	v, ok := dep["image_error"]
	if !ok || v != nil {
		t.Fatalf("clear must send explicit JSON null: %v", dep)
	}
	if _, ok := dep["annotation"]; ok {
		t.Fatalf("unmanaged field must not be patched: %v", dep)
	}
}

func TestDiffSourceEmptyCollectionsClear(t *testing.T) {
	plan := &SourceResourceModel{Deployment: testDeploymentModel()}
	state := &SourceResourceModel{Deployment: testDeploymentModel()}
	plan.Name = strVal("media")
	state.Name = strVal("media")

	empty, _ := types.SetValueFrom(context.Background(), types.StringType, []string{})
	plan.Deployment.CustomDomains = empty // explicit [] clears

	patch, _, diags := diffSource(context.Background(), plan, state, nil)
	if diags.HasError() {
		t.Fatal(diags)
	}
	dep := patch["deployment"].(map[string]interface{})
	domains, ok := dep["custom_domains"].([]string)
	if !ok || len(domains) != 0 {
		t.Fatalf("explicit [] must clear custom_domains: %v", dep)
	}
}

func TestFlattenSourcePreservesSecretAndComputesDomains(t *testing.T) {
	model := &SourceResourceModel{Deployment: testDeploymentModel()}
	model.Deployment.GCSSecretKey = strVal("known-secret")

	src := &imgix.Source{
		ID:               "id1",
		Name:             "media",
		Enabled:          boolPtr(true),
		DeploymentStatus: "deployed",
		DateDeployed:     int64Ptr(1700000000),
		SecureURLToken:   strPtr("tok123"),
		Deployment: &imgix.Deployment{
			Type:            "gcs",
			ImgixSubdomains: []string{"example-media"},
			GCSBucket:       "media.example.com",
			GCSAccessKey:    "AKIAROTATED",
			CustomDomains:   []string{"media.example.com"},
			DefaultParams:   map[string]string{"auto": "compress,format"},
			CacheTTLValue:   int64Ptr(31536000),
			// AllowsUpload absent → must stay untouched.
		},
	}

	var diags diag.Diagnostics
	flattenSource(context.Background(), src, model, &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}

	if model.Deployment.GCSSecretKey.ValueString() != "known-secret" {
		t.Fatal("secret was erased by refresh")
	}
	if model.Deployment.GCSAccessKey.ValueString() != "AKIAROTATED" {
		t.Fatal("access key should follow the API value when returned")
	}
	// allows_upload was never set and the API omitted it → stays not-known
	// (unknown or null), never a verified false.
	if model.Deployment.AllowsUpload.Equal(types.BoolValue(false)) {
		t.Fatal("absent allows_upload must stay unknown, not verified false")
	}

	// A previously confirmed allows_upload is dropped when the API stops
	// returning it: absent means unknown, not stale.
	model.Deployment.AllowsUpload = types.BoolValue(true)
	flattenSource(context.Background(), src, model, &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if !model.Deployment.AllowsUpload.IsNull() {
		t.Fatal("absent allows_upload must become null, not retain a stale value")
	}
	if model.SecureURLToken.ValueString() != "tok123" {
		t.Fatal("secure_url_token not flattened")
	}

	var domains []string
	model.Domains.ElementsAs(context.Background(), &domains, false)
	want := map[string]bool{"example-media.imgix.net": true, "media.example.com": true}
	if len(domains) != 2 {
		t.Fatalf("unexpected domains: %v", domains)
	}
	for _, d := range domains {
		if !want[d] {
			t.Fatalf("unexpected domain %q", d)
		}
	}
}

func TestFlattenSourceAbsentTokenClears(t *testing.T) {
	model := &SourceResourceModel{Deployment: testDeploymentModel()}
	model.SecureURLToken = strVal("old-tok")
	src := &imgix.Source{ID: "id1", Name: "media", DeploymentStatus: "deployed", Deployment: &imgix.Deployment{Type: "gcs"}}

	var diags diag.Diagnostics
	flattenSource(context.Background(), src, model, &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if !model.SecureURLToken.IsNull() {
		t.Fatal("absent secure_url_token must reset to null")
	}
}

func TestDiffSourceAnnotationResendPolicy(t *testing.T) {
	newModels := func(annotationPlan, annotationState, annotationCfg types.String) (*SourceResourceModel, *SourceResourceModel, *DeploymentModel) {
		plan := &SourceResourceModel{Deployment: testDeploymentModel()}
		state := &SourceResourceModel{Deployment: testDeploymentModel()}
		plan.Name = strVal("media")
		state.Name = strVal("media")
		plan.Deployment.Annotation = annotationPlan
		state.Deployment.Annotation = annotationState
		// an unrelated managed change so a deployment PATCH exists
		plan.Deployment.CacheTTLValue = types.Int64Value(86400)
		state.Deployment.CacheTTLValue = types.Int64Value(31536000)
		return plan, state, &DeploymentModel{Annotation: annotationCfg}
	}

	t.Run("configured annotation is resent on unrelated deployment patches", func(t *testing.T) {
		plan, state, cfg := newModels(strVal("pinned"), strVal("pinned"), strVal("pinned"))
		patch, _, diags := diffSource(context.Background(), plan, state, cfg)
		if diags.HasError() {
			t.Fatal(diags)
		}
		dep := patch["deployment"].(map[string]interface{})
		if dep["annotation"] != "pinned" {
			t.Fatalf("configured annotation must be resent: %v", dep)
		}
	})

	t.Run("unmanaged annotation is never replayed over dashboard edits", func(t *testing.T) {
		// Seeded at import ("old" from the API), not present in configuration.
		plan, state, cfg := newModels(strVal("old"), strVal("old"), types.StringNull())
		patch, _, diags := diffSource(context.Background(), plan, state, cfg)
		if diags.HasError() {
			t.Fatal(diags)
		}
		dep := patch["deployment"].(map[string]interface{})
		if _, sent := dep["annotation"]; sent {
			t.Fatalf("unmanaged annotation must not be replayed: %v", dep)
		}
	})

	t.Run("configured empty annotation re-asserts the clear on unrelated patches", func(t *testing.T) {
		// plan == state == "" (clear already applied), unrelated TTL change.
		plan, state, cfg := newModels(strVal(""), strVal(""), strVal(""))
		patch, _, diags := diffSource(context.Background(), plan, state, cfg)
		if diags.HasError() {
			t.Fatal(diags)
		}
		dep := patch["deployment"].(map[string]interface{})
		v, ok := dep["annotation"]
		if !ok || v != nil {
			t.Fatalf("configured empty annotation must resend JSON null: %v", dep)
		}
	})

	t.Run("explicit clear wins over resend", func(t *testing.T) {
		plan, state, cfg := newModels(types.StringNull(), strVal("old"), strVal(""))
		patch, _, diags := diffSource(context.Background(), plan, state, cfg)
		if diags.HasError() {
			t.Fatal(diags)
		}
		dep := patch["deployment"].(map[string]interface{})
		v, ok := dep["annotation"]
		if !ok || v != nil {
			t.Fatalf("explicit clear must send JSON null: %v", dep)
		}
	})
}
