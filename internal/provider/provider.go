package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/udx/terraform-provider-imgix/internal/imgix"
)

var _ provider.Provider = &ImgixProvider{}

type ImgixProvider struct {
	version string
}

type ImgixProviderModel struct {
	APIKey types.String `tfsdk:"api_key"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &ImgixProvider{version: version}
	}
}

func (p *ImgixProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "imgix"
	resp.Version = p.version
}

func (p *ImgixProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage Imgix sources through the Imgix Management API.",
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Imgix API key with the Sources permission. Falls back to the IMGIX_API_KEY environment variable.",
			},
		},
	}
}

func (p *ImgixProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config ImgixProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiKey := os.Getenv("IMGIX_API_KEY")
	if !config.APIKey.IsNull() && !config.APIKey.IsUnknown() && config.APIKey.ValueString() != "" {
		apiKey = config.APIKey.ValueString()
	}
	if apiKey == "" {
		resp.Diagnostics.AddError(
			"Missing Imgix API key",
			"Set the api_key provider attribute or the IMGIX_API_KEY environment variable. "+
				"The key needs the Sources permission in the Imgix dashboard.",
		)
		return
	}

	client := imgix.NewClient(apiKey)
	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *ImgixProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewSourceResource,
	}
}

func (p *ImgixProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}
