package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// clearableString distinguishes "unmanaged" from "clear" for nullable string
// fields: a null config value mirrors whatever the API holds (prior state),
// while an explicitly configured empty string plans null, which the apply
// sends to the API as JSON null to clear the field.
func clearableString() planmodifier.String {
	return clearableStringModifier{}
}

type clearableStringModifier struct{}

func (m clearableStringModifier) Description(ctx context.Context) string {
	return "Null config keeps the remote value; an empty string clears it."
}

func (m clearableStringModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m clearableStringModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.ConfigValue.IsNull() {
		// Unmanaged: mirror prior state verbatim (including null), so
		// post-import plans are clean.
		resp.PlanValue = req.StateValue
		return
	}
	if req.ConfigValue.ValueString() == "" {
		resp.PlanValue = types.StringNull()
	}
}
