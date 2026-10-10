package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/udx/terraform-provider-imgix/internal/imgix"
)

const defaultOperationTimeout = 10 * time.Minute

var (
	_ resource.Resource                = &SourceResource{}
	_ resource.ResourceWithImportState = &SourceResource{}
)

type SourceResource struct {
	client *imgix.Client
}

type DeploymentModel struct {
	Type                  types.String `tfsdk:"type"`
	ImgixSubdomains       types.Set    `tfsdk:"imgix_subdomains"`
	GCSBucket             types.String `tfsdk:"gcs_bucket"`
	GCSAccessKey          types.String `tfsdk:"gcs_access_key"`
	GCSSecretKey          types.String `tfsdk:"gcs_secret_key"`
	GCSPrefix             types.String `tfsdk:"gcs_prefix"`
	CustomDomains         types.Set    `tfsdk:"custom_domains"`
	DefaultParams         types.Map    `tfsdk:"default_params"`
	SecureURLEnabled      types.Bool   `tfsdk:"secure_url_enabled"`
	CacheTTLBehavior      types.String `tfsdk:"cache_ttl_behavior"`
	CacheTTLValue         types.Int64  `tfsdk:"cache_ttl_value"`
	CacheTTLError         types.Int64  `tfsdk:"cache_ttl_error"`
	CrossdomainXMLEnabled types.Bool   `tfsdk:"crossdomain_xml_enabled"`
	ImageError            types.String `tfsdk:"image_error"`
	ImageMissing          types.String `tfsdk:"image_missing"`
	ImageErrorAppendQS    types.Bool   `tfsdk:"image_error_append_qs"`
	ImageMissingAppendQS  types.Bool   `tfsdk:"image_missing_append_qs"`
	Annotation            types.String `tfsdk:"annotation"`
	AllowsUpload          types.Bool   `tfsdk:"allows_upload"`
}

type SourceResourceModel struct {
	ID               types.String     `tfsdk:"id"`
	Name             types.String     `tfsdk:"name"`
	Enabled          types.Bool       `tfsdk:"enabled"`
	Deployment       *DeploymentModel `tfsdk:"deployment"`
	WaitForDeployed  types.Bool       `tfsdk:"wait_for_deployed"`
	DeploymentStatus types.String     `tfsdk:"deployment_status"`
	DateDeployed     types.Int64      `tfsdk:"date_deployed"`
	SecureURLToken   types.String     `tfsdk:"secure_url_token"`
	Domains          types.Set        `tfsdk:"domains"`
	Timeouts         timeouts.Value   `tfsdk:"timeouts"`
}

func NewSourceResource() resource.Resource {
	return &SourceResource{}
}

func (r *SourceResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_source"
}

func (r *SourceResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an Imgix source (GCS origin) through the Imgix Management API.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Imgix source ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Display name of the source. Distinct from the delivery hostname.",
				Validators:  []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"enabled": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "Whether the source serves traffic. Omitted preserves the remote value.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"deployment": schema.SingleNestedAttribute{
				Required:    true,
				Description: "Origin and serving configuration. Only GCS origins (type = \"gcs\") are supported.",
				Attributes:  deploymentAttributes(),
			},
			"wait_for_deployed": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(true),
				Description:   "Poll after deployment-triggering writes until the source reaches its intended terminal state.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"deployment_status": schema.StringAttribute{
				Computed:    true,
				Description: "API deployment status: deploying, deployed, disabled, or deleted.",
			},
			"date_deployed": schema.Int64Attribute{
				Computed:    true,
				Description: "Unix timestamp of the last completed deployment.",
			},
			"secure_url_token": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "Signing token for the source when the API returns one; it may be present even when secure_url_enabled is false.",
			},
			"domains": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Full delivery hostnames: imgix subdomains plus custom domains. No scheme.",
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func deploymentAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"type": schema.StringAttribute{
			Required:    true,
			Description: "Origin type. Only \"gcs\" is supported by this release.",
			Validators:  []validator.String{stringvalidator.OneOf("gcs")},
		},
		"imgix_subdomains": schema.SetAttribute{
			Required:    true,
			ElementType: types.StringType,
			Description: "Bare *.imgix.net subdomains (e.g. \"example-media\"). At least one; unique across all sources.",
			Validators: []validator.Set{
				setvalidator.SizeAtLeast(1),
				setvalidator.ValueStringsAre(subdomainValidator()),
			},
		},
		"gcs_bucket": schema.StringAttribute{
			Required:    true,
			Description: "GCS bucket name, without a gs:// prefix.",
			Validators:  []validator.String{gcsBucketValidator()},
		},
		"gcs_access_key": schema.StringAttribute{
			Optional:      true,
			Computed:      true,
			Sensitive:     true,
			Description:   "GCS HMAC access key. Required on create; preserved from state afterwards.",
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"gcs_secret_key": schema.StringAttribute{
			Optional:      true,
			Computed:      true,
			Sensitive:     true,
			Description:   "GCS HMAC secret. Required on create; the API never returns it, so the known value is preserved in state.",
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"gcs_prefix": schema.StringAttribute{
			Optional:      true,
			Computed:      true,
			Description:   "Folder prefix prepended to image paths before resolving in GCS. Empty string clears.",
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"custom_domains": schema.SetAttribute{
			Optional:      true,
			Computed:      true,
			ElementType:   types.StringType,
			Description:   "Full DNS hostnames Imgix terminates. Explicit [] clears.",
			Validators:    []validator.Set{setvalidator.ValueStringsAre(dnsNameValidator())},
			PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
		},
		"default_params": schema.MapAttribute{
			Optional:      true,
			Computed:      true,
			ElementType:   types.StringType,
			Description:   "Imgix render parameters applied to all requests, e.g. { auto = \"compress,format\" }. Explicit {} clears.",
			PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
		},
		"secure_url_enabled": schema.BoolAttribute{
			Optional:      true,
			Computed:      true,
			Description:   "Require requests to be signed with secure_url_token. Never enabled implicitly on an existing source.",
			PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
		},
		"cache_ttl_behavior": schema.StringAttribute{
			Optional:      true,
			Computed:      true,
			Description:   "How the TTL on served images is determined.",
			Validators:    []validator.String{stringvalidator.OneOf("respect_origin", "override_origin", "enforce_minimum")},
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"cache_ttl_value": schema.Int64Attribute{
			Optional:      true,
			Computed:      true,
			Description:   "TTL in seconds used by cache_ttl_behavior (1800-31536000).",
			Validators:    []validator.Int64{int64validator.Between(1800, 31536000)},
			PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
		},
		"cache_ttl_error": schema.Int64Attribute{
			Optional:      true,
			Computed:      true,
			Description:   "TTL in seconds for error images when the origin cannot be fetched (1-31536000).",
			Validators:    []validator.Int64{int64validator.Between(1, 31536000)},
			PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
		},
		"crossdomain_xml_enabled": schema.BoolAttribute{
			Optional:      true,
			Computed:      true,
			Description:   "Serve a cross-domain policy file when requested.",
			PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
		},
		"image_error": schema.StringAttribute{
			Optional:      true,
			Computed:      true,
			Description:   "Absolute URL served when a request errors. Empty string clears.",
			Validators:    []validator.String{absoluteURLValidator()},
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"image_missing": schema.StringAttribute{
			Optional:      true,
			Computed:      true,
			Description:   "Absolute URL served when the origin image is missing. Empty string clears.",
			Validators:    []validator.String{absoluteURLValidator()},
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"image_error_append_qs": schema.BoolAttribute{
			Optional:      true,
			Computed:      true,
			Description:   "Forward the failed request's query parameters to image_error.",
			PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
		},
		"image_missing_append_qs": schema.BoolAttribute{
			Optional:      true,
			Computed:      true,
			Description:   "Forward the failed request's query parameters to image_missing.",
			PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
		},
		"annotation": schema.StringAttribute{
			Optional:      true,
			Computed:      true,
			Description:   "Comment recorded on the deployment. Empty string clears.",
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"allows_upload": schema.BoolAttribute{
			Computed:    true,
			Description: "Whether Imgix can upload to the origin. Absent from the API means unknown, not false.",
			// No UseStateForUnknown: the API may omit this after an update, and
			// flattening then records null — the plan must tolerate that.
		},
	}
}

func (r *SourceResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*imgix.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *imgix.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	r.client = client
}

func (r *SourceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SourceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The HMAC pair is required on create but optional afterwards (the API
	// never returns the secret), so schema cannot express it: validate here.
	if plan.Deployment.GCSAccessKey.IsNull() || plan.Deployment.GCSAccessKey.IsUnknown() || plan.Deployment.GCSAccessKey.ValueString() == "" {
		resp.Diagnostics.AddError("Missing gcs_access_key", "deployment.gcs_access_key is required when creating a source.")
	}
	if plan.Deployment.GCSSecretKey.IsNull() || plan.Deployment.GCSSecretKey.IsUnknown() || plan.Deployment.GCSSecretKey.ValueString() == "" {
		resp.Diagnostics.AddError("Missing gcs_secret_key", "deployment.gcs_secret_key is required when creating a source.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, defaultOperationTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	attrs := map[string]interface{}{"name": plan.Name.ValueString()}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		attrs["enabled"] = plan.Enabled.ValueBool()
	}
	depAttrs, depDiags := expandDeployment(ctx, plan.Deployment)
	resp.Diagnostics.Append(depDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	attrs["deployment"] = depAttrs

	source, err := r.client.CreateSource(ctx, attrs)
	if err != nil {
		resp.Diagnostics.AddError("Error creating Imgix source", err.Error())
		return
	}

	// Persist the created source immediately: Terraform records state returned
	// alongside error diagnostics, so a failed wait or refresh below leaves
	// the source managed and the next apply converges via update instead of
	// conflicting on the subdomains.
	flattenSource(ctx, source, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.WaitForDeployed.ValueBool() {
		if _, err := r.client.WaitForStatus(ctx, source.ID, wantDisabled(plan.Enabled), 5*time.Second); err != nil {
			resp.Diagnostics.AddError("Imgix source did not reach its expected status", err.Error())
			return
		}
	}

	r.readIntoState(ctx, source.ID, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SourceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SourceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	source, err := r.client.GetSource(ctx, state.ID.ValueString())
	if err != nil {
		if imgix.NotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading Imgix source", err.Error())
		return
	}

	flattenSource(ctx, source, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SourceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, config SourceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Update(ctx, defaultOperationTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	patch, _, patchDiags := diffSource(ctx, &plan, &state, config.Deployment)
	resp.Diagnostics.Append(patchDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	if len(patch) > 0 {
		if _, err := r.client.UpdateSource(ctx, id, patch); err != nil {
			resp.Diagnostics.AddError("Error updating Imgix source "+id, err.Error())
			return
		}
	}
	// Wait when a patch triggered a deploy — but also when the source is not
	// at its intended terminal state anyway (e.g. a previous create/update
	// timed out after persisting state), so a retry does not silently succeed
	// while the source is still deploying.
	wantDisabledNow := wantDisabled(plan.Enabled)
	targetStatus := "deployed"
	if wantDisabledNow {
		targetStatus = "disabled"
	}
	statusPending := state.DeploymentStatus.ValueString() != targetStatus
	if plan.WaitForDeployed.ValueBool() && (len(patch) > 0 || statusPending) {
		if _, err := r.client.WaitForStatus(ctx, id, wantDisabledNow, 5*time.Second); err != nil {
			resp.Diagnostics.AddError("Imgix source did not reach its expected status", err.Error())
			return
		}
	}

	r.readIntoState(ctx, id, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SourceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SourceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := state.Timeouts.Delete(ctx, defaultOperationTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	id := state.ID.ValueString()
	if err := r.client.DeleteSource(ctx, id); err != nil {
		if imgix.DeletionUnsupported(err) {
			resp.Diagnostics.AddError(
				"Imgix source deletion is not supported by the API",
				fmt.Sprintf("Source %s was left untouched: the Imgix Management API documents only GET/POST/PATCH for sources. "+
					"Delete the source in the Imgix dashboard, then run `terraform state rm` to drop it from state.", id),
			)
			return
		}
		resp.Diagnostics.AddError("Error deleting Imgix source "+id, err.Error())
	}
}

func (r *SourceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// readIntoState refreshes the source and folds it into the model that is
// about to be persisted. Secrets never returned by the API are preserved from
// the model's prior value.
func (r *SourceResource) readIntoState(ctx context.Context, id string, model *SourceResourceModel, diags *diag.Diagnostics) {
	source, err := r.client.GetSource(ctx, id)
	if err != nil {
		diags.AddError("Error reading Imgix source after write", err.Error())
		return
	}
	flattenSource(ctx, source, model, diags)
}

func wantDisabled(enabled types.Bool) bool {
	return !enabled.IsNull() && !enabled.IsUnknown() && !enabled.ValueBool()
}
