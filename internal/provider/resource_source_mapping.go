package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/udx/terraform-provider-imgix/internal/imgix"
)

// expandDeployment renders the deployment object for a create request. Only
// fields that are known and non-null are sent; the API fills in its defaults,
// which are then read back into state.
func expandDeployment(ctx context.Context, d *DeploymentModel) (map[string]interface{}, diag.Diagnostics) {
	var diags diag.Diagnostics

	subdomains := make([]string, 0)
	diags.Append(d.ImgixSubdomains.ElementsAs(ctx, &subdomains, false)...)

	m := map[string]interface{}{
		"type":             d.Type.ValueString(),
		"imgix_subdomains": subdomains,
		"gcs_bucket":       d.GCSBucket.ValueString(),
	}
	putString(m, "gcs_access_key", d.GCSAccessKey)
	putString(m, "gcs_secret_key", d.GCSSecretKey)
	putString(m, "gcs_prefix", d.GCSPrefix)
	putBool(m, "secure_url_enabled", d.SecureURLEnabled)
	putString(m, "cache_ttl_behavior", d.CacheTTLBehavior)
	putInt64(m, "cache_ttl_value", d.CacheTTLValue)
	putInt64(m, "cache_ttl_error", d.CacheTTLError)
	putBool(m, "crossdomain_xml_enabled", d.CrossdomainXMLEnabled)
	putString(m, "image_error", d.ImageError)
	putString(m, "image_missing", d.ImageMissing)
	putBool(m, "image_error_append_qs", d.ImageErrorAppendQS)
	putBool(m, "image_missing_append_qs", d.ImageMissingAppendQS)
	putString(m, "annotation", d.Annotation)

	if !d.CustomDomains.IsNull() && !d.CustomDomains.IsUnknown() {
		domains := make([]string, 0)
		diags.Append(d.CustomDomains.ElementsAs(ctx, &domains, false)...)
		m["custom_domains"] = domains
	}
	if !d.DefaultParams.IsNull() && !d.DefaultParams.IsUnknown() {
		params := make(map[string]string)
		diags.Append(d.DefaultParams.ElementsAs(ctx, &params, false)...)
		m["default_params"] = params
	}
	return m, diags
}

// diffSource returns the sparse PATCH attributes for an update: only managed
// fields whose planned value differs from state. A planned null for a
// clearable string sends JSON null (explicit clear); null collections and
// null bools/ints are never sent (null there only ever means unmanaged).
func diffSource(ctx context.Context, plan, state *SourceResourceModel) (map[string]interface{}, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	patch := map[string]interface{}{}

	if !plan.Name.Equal(state.Name) {
		patch["name"] = plan.Name.ValueString()
	}
	if !plan.Enabled.Equal(state.Enabled) && !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		patch["enabled"] = plan.Enabled.ValueBool()
	}

	depPatch := map[string]interface{}{}
	pd, sd := plan.Deployment, state.Deployment
	if pd != nil && sd != nil {
		diffString(depPatch, "type", pd.Type, sd.Type)
		diffString(depPatch, "gcs_bucket", pd.GCSBucket, sd.GCSBucket)
		diffString(depPatch, "gcs_access_key", pd.GCSAccessKey, sd.GCSAccessKey)
		diffString(depPatch, "gcs_secret_key", pd.GCSSecretKey, sd.GCSSecretKey)
		diffString(depPatch, "gcs_prefix", pd.GCSPrefix, sd.GCSPrefix)
		diffBool(depPatch, "secure_url_enabled", pd.SecureURLEnabled, sd.SecureURLEnabled)
		diffString(depPatch, "cache_ttl_behavior", pd.CacheTTLBehavior, sd.CacheTTLBehavior)
		diffInt64(depPatch, "cache_ttl_value", pd.CacheTTLValue, sd.CacheTTLValue)
		diffInt64(depPatch, "cache_ttl_error", pd.CacheTTLError, sd.CacheTTLError)
		diffBool(depPatch, "crossdomain_xml_enabled", pd.CrossdomainXMLEnabled, sd.CrossdomainXMLEnabled)
		diffString(depPatch, "image_error", pd.ImageError, sd.ImageError)
		diffString(depPatch, "image_missing", pd.ImageMissing, sd.ImageMissing)
		diffBool(depPatch, "image_error_append_qs", pd.ImageErrorAppendQS, sd.ImageErrorAppendQS)
		diffBool(depPatch, "image_missing_append_qs", pd.ImageMissingAppendQS, sd.ImageMissingAppendQS)
		diffString(depPatch, "annotation", pd.Annotation, sd.Annotation)

		if !pd.ImgixSubdomains.Equal(sd.ImgixSubdomains) {
			subs := make([]string, 0)
			diags.Append(pd.ImgixSubdomains.ElementsAs(ctx, &subs, false)...)
			depPatch["imgix_subdomains"] = subs
		}
		if !pd.CustomDomains.Equal(sd.CustomDomains) && !pd.CustomDomains.IsNull() {
			domains := make([]string, 0)
			diags.Append(pd.CustomDomains.ElementsAs(ctx, &domains, false)...)
			depPatch["custom_domains"] = domains
		}
		if !pd.DefaultParams.Equal(sd.DefaultParams) && !pd.DefaultParams.IsNull() {
			params := make(map[string]string)
			diags.Append(pd.DefaultParams.ElementsAs(ctx, &params, false)...)
			depPatch["default_params"] = params
		}
	}

	if len(depPatch) > 0 {
		// Any deployment PATCH that omits annotation makes the API regenerate
		// it ("New deployment created for source by <user>"). Resend the
		// configured value so unrelated updates don't churn it.
		if _, patched := depPatch["annotation"]; !patched && !sd.Annotation.IsNull() && sd.Annotation.ValueString() != "" {
			depPatch["annotation"] = sd.Annotation.ValueString()
		}
		patch["deployment"] = depPatch
	}
	return patch, len(depPatch) > 0, diags
}

// flattenSource folds an API source into the Terraform state model. Fields
// the API omits (the origin secret, allows_upload) keep their prior state
// value; everything else mirrors the API exactly.
func flattenSource(ctx context.Context, src *imgix.Source, model *SourceResourceModel, diags *diag.Diagnostics) {
	model.ID = types.StringValue(src.ID)
	model.Name = types.StringValue(src.Name)
	if model.WaitForDeployed.IsNull() || model.WaitForDeployed.IsUnknown() {
		// Provider-side default, normalized into state (matters after import).
		model.WaitForDeployed = types.BoolValue(true)
	}
	if src.Enabled != nil {
		model.Enabled = types.BoolValue(*src.Enabled)
	} else if model.Enabled.IsUnknown() {
		model.Enabled = types.BoolNull()
	}
	model.DeploymentStatus = types.StringValue(src.DeploymentStatus)
	if src.DateDeployed != nil {
		model.DateDeployed = types.Int64Value(*src.DateDeployed)
	} else {
		model.DateDeployed = types.Int64Null()
	}
	if src.SecureURLToken != nil {
		model.SecureURLToken = types.StringValue(*src.SecureURLToken)
	} else {
		model.SecureURLToken = types.StringNull()
	}

	var subdomains, customDomains []string
	if src.Deployment != nil {
		d := model.Deployment
		if d == nil {
			d = &DeploymentModel{}
			model.Deployment = d
		}
		dep := src.Deployment

		d.Type = types.StringValue(dep.Type)
		d.GCSBucket = types.StringValue(dep.GCSBucket)
		if dep.GCSAccessKey != "" {
			d.GCSAccessKey = types.StringValue(dep.GCSAccessKey)
		}
		// GCSSecretKey is never returned by the API: keep the prior value.
		d.GCSPrefix = stringPointerValue(dep.GCSPrefix)
		d.ImageError = stringPointerValue(dep.ImageError)
		d.ImageMissing = stringPointerValue(dep.ImageMissing)
		// Annotation is write-only after creation: any deployment PATCH that
		// omits it makes the API regenerate it, so the configured/state value
		// is kept. Only seed from the API when the value is unknown (import).
		if d.Annotation.IsUnknown() {
			d.Annotation = stringPointerValue(dep.Annotation)
		}

		subdomains = dep.ImgixSubdomains
		subValue, subDiags := types.SetValueFrom(ctx, types.StringType, dep.ImgixSubdomains)
		diags.Append(subDiags...)
		d.ImgixSubdomains = subValue

		customDomains = dep.CustomDomains
		cdValue, cdDiags := types.SetValueFrom(ctx, types.StringType, dep.CustomDomains)
		diags.Append(cdDiags...)
		d.CustomDomains = cdValue

		dpValue, dpDiags := types.MapValueFrom(ctx, types.StringType, dep.DefaultParams)
		diags.Append(dpDiags...)
		d.DefaultParams = dpValue

		if dep.SecureURLEnabled != nil {
			d.SecureURLEnabled = types.BoolValue(*dep.SecureURLEnabled)
		} else if d.SecureURLEnabled.IsUnknown() {
			d.SecureURLEnabled = types.BoolNull()
		}
		if dep.CacheTTLBehavior != "" {
			d.CacheTTLBehavior = types.StringValue(dep.CacheTTLBehavior)
		} else if d.CacheTTLBehavior.IsUnknown() {
			d.CacheTTLBehavior = types.StringNull()
		}
		if dep.CacheTTLValue != nil {
			d.CacheTTLValue = types.Int64Value(*dep.CacheTTLValue)
		} else if d.CacheTTLValue.IsUnknown() {
			d.CacheTTLValue = types.Int64Null()
		}
		if dep.CacheTTLError != nil {
			d.CacheTTLError = types.Int64Value(*dep.CacheTTLError)
		} else if d.CacheTTLError.IsUnknown() {
			d.CacheTTLError = types.Int64Null()
		}
		if dep.CrossdomainXMLEnabled != nil {
			d.CrossdomainXMLEnabled = types.BoolValue(*dep.CrossdomainXMLEnabled)
		} else if d.CrossdomainXMLEnabled.IsUnknown() {
			d.CrossdomainXMLEnabled = types.BoolNull()
		}
		if dep.ImageErrorAppendQS != nil {
			d.ImageErrorAppendQS = types.BoolValue(*dep.ImageErrorAppendQS)
		} else if d.ImageErrorAppendQS.IsUnknown() {
			d.ImageErrorAppendQS = types.BoolNull()
		}
		if dep.ImageMissingAppendQS != nil {
			d.ImageMissingAppendQS = types.BoolValue(*dep.ImageMissingAppendQS)
		} else if d.ImageMissingAppendQS.IsUnknown() {
			d.ImageMissingAppendQS = types.BoolNull()
		}
		if dep.AllowsUpload != nil {
			d.AllowsUpload = types.BoolValue(*dep.AllowsUpload)
		} else if d.AllowsUpload.IsUnknown() {
			// Absent from the API means unknown — null in state, never false.
			d.AllowsUpload = types.BoolNull()
		}
	}

	domains := make([]string, 0, len(subdomains)+len(customDomains))
	for _, sub := range subdomains {
		domains = append(domains, sub+".imgix.net")
	}
	domains = append(domains, customDomains...)
	domainsValue, domDiags := types.SetValueFrom(ctx, types.StringType, domains)
	diags.Append(domDiags...)
	model.Domains = domainsValue
}

func putString(m map[string]interface{}, key string, v types.String) {
	if !v.IsNull() && !v.IsUnknown() {
		m[key] = v.ValueString()
	}
}

func putBool(m map[string]interface{}, key string, v types.Bool) {
	if !v.IsNull() && !v.IsUnknown() {
		m[key] = v.ValueBool()
	}
}

func putInt64(m map[string]interface{}, key string, v types.Int64) {
	if !v.IsNull() && !v.IsUnknown() {
		m[key] = v.ValueInt64()
	}
}

// diffString sends JSON null when the planned value is null (explicit clear
// of a clearable string); every other changed value is sent verbatim.
func diffString(m map[string]interface{}, key string, plan, state types.String) {
	if plan.Equal(state) {
		return
	}
	if plan.IsNull() {
		m[key] = nil
		return
	}
	if !plan.IsUnknown() {
		m[key] = plan.ValueString()
	}
}

func diffBool(m map[string]interface{}, key string, plan, state types.Bool) {
	if plan.Equal(state) || plan.IsNull() || plan.IsUnknown() {
		return
	}
	m[key] = plan.ValueBool()
}

func diffInt64(m map[string]interface{}, key string, plan, state types.Int64) {
	if plan.Equal(state) || plan.IsNull() || plan.IsUnknown() {
		return
	}
	m[key] = plan.ValueInt64()
}

func stringPointerValue(p *string) types.String {
	if p == nil {
		return types.StringNull()
	}
	return types.StringValue(*p)
}
