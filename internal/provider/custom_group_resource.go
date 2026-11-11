package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jacoblamarche/terraform-provider-viya/internal/viya"
)

var (
	_ resource.Resource                = (*customGroupResource)(nil)
	_ resource.ResourceWithConfigure   = (*customGroupResource)(nil)
	_ resource.ResourceWithImportState = (*customGroupResource)(nil)
)

type customGroupResource struct {
	client *viya.Client
}

type customGroupModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	State       types.String `tfsdk:"state"`
	ProviderID  types.String `tfsdk:"provider_id"`
}

func newCustomGroupResource() resource.Resource {
	return &customGroupResource{}
}

func (r *customGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_custom_group"
}

func (r *customGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a custom group in the SAS Viya Identities service.\n\n" +
			"Custom groups are stored by SAS Viya itself, as opposed to the groups that it reads from " +
			"an identity provider such as LDAP or SCIM. They are typically used as principals of " +
			"authorization rules.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Unique identifier of the group, for example `DataScientists`. " +
					"Changing it forces a new group to be created.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Display name of the group.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Description of the group.",
				Validators: []validator.String{
					// Viya does not distinguish an empty description from
					// an absent one, so only the absent form is accepted.
					stringvalidator.LengthAtLeast(1),
				},
			},
			"state": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(viya.GroupStateActive),
				MarkdownDescription: "State of the group: `active` or `inactive`. " +
					"Defaults to `active`.",
				Validators: []validator.String{
					stringvalidator.OneOf(viya.GroupStateActive, viya.GroupStateInactive),
				},
			},
			"provider_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Identifier of the identity provider that holds the group. " +
					"It is `local` for custom groups.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *customGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// The provider is not configured yet during early validation.
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*viya.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data",
			fmt.Sprintf("Expected *viya.Client, got %T. This is a bug in the provider.", req.ProviderData),
		)
		return
	}
	r.client = client
}

func (r *customGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan customGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateGroup(ctx, plan.toAPI())
	if err != nil {
		if viya.IsConflict(err) {
			resp.Diagnostics.AddError(
				"Group already exists",
				fmt.Sprintf("A group with the ID %q already exists in SAS Viya. To manage it with "+
					"Terraform, import it: terraform import <address> %s", plan.ID.ValueString(), plan.ID.ValueString()),
			)
			return
		}
		resp.Diagnostics.AddError("Unable to create group", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, customGroupModelFromAPI(created))...)
}

func (r *customGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state customGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	group, err := r.client.GetGroup(ctx, state.ID.ValueString())
	if err != nil {
		if viya.IsNotFound(err) {
			// Deleted outside Terraform: drop it from the state so that
			// the next plan proposes to create it again.
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read group", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, customGroupModelFromAPI(group))...)
}

func (r *customGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan customGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Read the current entity tag, so that the update is rejected rather
	// than silently overwriting a concurrent change.
	current, err := r.client.GetGroup(ctx, plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to read group before update", err.Error())
		return
	}

	desired := plan.toAPI()
	desired.ETag = current.ETag

	updated, err := r.client.UpdateGroup(ctx, desired)
	if err != nil {
		if viya.IsPreconditionFailed(err) {
			resp.Diagnostics.AddError(
				"Group was modified concurrently",
				fmt.Sprintf("The group %q changed in SAS Viya while Terraform was updating it. "+
					"Run the operation again.", plan.ID.ValueString()),
			)
			return
		}
		resp.Diagnostics.AddError("Unable to update group", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, customGroupModelFromAPI(updated))...)
}

func (r *customGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state customGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A group that is already gone is the desired outcome.
	if err := r.client.DeleteGroup(ctx, state.ID.ValueString()); err != nil && !viya.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to delete group", err.Error())
	}
}

func (r *customGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (m customGroupModel) toAPI() viya.Group {
	return viya.Group{
		ID:          m.ID.ValueString(),
		Name:        m.Name.ValueString(),
		Description: m.Description.ValueString(),
		State:       m.State.ValueString(),
	}
}

func customGroupModelFromAPI(group *viya.Group) customGroupModel {
	model := customGroupModel{
		ID:          types.StringValue(group.ID),
		Name:        types.StringValue(group.Name),
		Description: types.StringNull(),
		State:       types.StringValue(group.State),
		ProviderID:  types.StringValue(group.ProviderID),
	}
	if group.Description != "" {
		model.Description = types.StringValue(group.Description)
	}
	return model
}
