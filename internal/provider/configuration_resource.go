package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jacoblamarche/terraform-provider-viya/internal/viya"
)

var (
	_ resource.Resource                   = (*configurationResource)(nil)
	_ resource.ResourceWithConfigure      = (*configurationResource)(nil)
	_ resource.ResourceWithImportState    = (*configurationResource)(nil)
	_ resource.ResourceWithValidateConfig = (*configurationResource)(nil)
)

// configurationNameProperty is the property that tells apart the instances
// of a definition that allows several per service.
const configurationNameProperty = "name"

// reservedConfigurationProperties are the fields of a configuration
// instance that cannot be set through properties.
var reservedConfigurationProperties = []string{"id", "links", "metadata", configurationNameProperty}

type configurationResource struct {
	client *viya.Client
}

type configurationModel struct {
	ID                  types.String  `tfsdk:"id"`
	Definition          types.String  `tfsdk:"definition"`
	Services            types.Set     `tfsdk:"services"`
	Name                types.String  `tfsdk:"name"`
	Properties          types.Dynamic `tfsdk:"properties"`
	SensitiveProperties types.Map     `tfsdk:"sensitive_properties"`
	Adopted             types.Bool    `tfsdk:"adopted"`
}

func newConfigurationResource() resource.Resource {
	return &configurationResource{}
}

func (r *configurationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_configuration"
}

func (r *configurationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an instance of a configuration definition in the SAS Viya Configuration " +
			"service. This is what SAS Environment Manager shows under *Configuration*: for example the " +
			"LDAP connection of the Identities service, or the autoexec code of the Compute server.\n\n" +
			"The resource is generic: it works with any definition, and does not know the properties " +
			"that a definition accepts.\n\n" +
			"## Existing instances are adopted\n\n" +
			"SAS Viya creates default instances for many definitions. When an instance already exists " +
			"for the same `definition`, `services` and `name`, the resource takes it over and applies " +
			"the configured properties to it, instead of creating a second one. The `adopted` attribute " +
			"tells which of the two happened.\n\n" +
			"## Only the listed properties are managed\n\n" +
			"Properties that are in neither `properties` nor `sensitive_properties` keep the value that " +
			"they have in SAS Viya, and changes made to them outside Terraform are not reported. " +
			"Removing a property from the configuration stops managing it; it does not reset its value.\n\n" +
			"## Importing\n\n" +
			"An imported instance has no managed property. SAS Viya returns passwords in clear text, so " +
			"the import does not copy any value into the state. Write the properties to manage in the " +
			"configuration: the first plan shows them as added, and applying them changes nothing in " +
			"SAS Viya if the values are the same.\n\n" +
			"## Destroying\n\n" +
			"Destroying the resource deletes the instance only if Terraform created it. An adopted " +
			"instance is left in place with its current values, because a service may not work without " +
			"it; Terraform reports a warning and stops managing it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Identifier that SAS Viya assigned to the instance.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"definition": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Name of the configuration definition, for example " +
					"`sas.identities.providers.ldap.connection`. Changing it forces a new resource.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"services": schema.SetAttribute{
				Required:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Services that the instance applies to, for example `[\"identities\"]`. " +
					"Changing it forces a new resource.",
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Name of the instance, for the definitions that allow several instances " +
					"per service and tell them apart with a `name` property. For example, " +
					"`sas.compute.server` has the instances `autoexec_code` and `configuration_options`. " +
					"Leave it unset for the other definitions. Changing it forces a new resource.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"properties": schema.DynamicAttribute{
				Optional: true,
				MarkdownDescription: "Properties to set on the instance, as an object whose values are " +
					"strings, numbers or booleans. Use the property names of the definition, as shown " +
					"by SAS Environment Manager or by `sas-viya configuration configurations show`.",
			},
			"sensitive_properties": schema.MapAttribute{
				Optional:    true,
				Sensitive:   true,
				ElementType: types.StringType,
				MarkdownDescription: "Secret properties to set on the instance, such as a password. " +
					"They are compared with the values in SAS Viya like the other properties, so a " +
					"secret changed outside Terraform shows in the plan, but their values never do. " +
					"A property cannot be in both `properties` and `sensitive_properties`.",
			},
			"adopted": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "Whether the instance existed before Terraform managed it. An adopted " +
					"instance is not deleted when the resource is destroyed. It is `true` for an " +
					"imported instance.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *configurationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *configurationResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config configurationModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	propertiesPath := path.Root("properties")
	sensitivePath := path.Root("sensitive_properties")

	if config.Properties.IsNull() && config.SensitiveProperties.IsNull() {
		resp.Diagnostics.AddError(
			"No property to manage",
			"Set at least one of properties and sensitive_properties.",
		)
		return
	}

	var properties map[string]attr.Value
	if !config.Properties.IsNull() && !config.Properties.IsUnknown() && !config.Properties.IsUnderlyingValueUnknown() {
		var ok bool
		properties, ok = propertyValues(config.Properties)
		if !ok {
			resp.Diagnostics.AddAttributeError(propertiesPath, "Invalid properties",
				"properties must be an object, for example { host = \"ldap.example.com\", port = 636 }.")
			return
		}
	}

	for _, name := range sortedKeys(properties) {
		value := properties[name]
		if slices.Contains(reservedConfigurationProperties, name) {
			resp.Diagnostics.AddAttributeError(propertiesPath, "Reserved property name", reservedPropertyDetail(name))
			continue
		}
		if value.IsUnknown() {
			continue
		}
		if _, ok := propertyToAPI(value); !ok {
			resp.Diagnostics.AddAttributeError(propertiesPath, "Unsupported property value",
				fmt.Sprintf("The property %q must be a string, a number or a boolean, and cannot be null. "+
					"To stop managing a property, remove it from properties.", name))
		}
	}

	if config.SensitiveProperties.IsNull() || config.SensitiveProperties.IsUnknown() {
		return
	}
	for _, name := range sortedKeys(config.SensitiveProperties.Elements()) {
		if slices.Contains(reservedConfigurationProperties, name) {
			resp.Diagnostics.AddAttributeError(sensitivePath, "Reserved property name", reservedPropertyDetail(name))
		}
		if _, duplicate := properties[name]; duplicate {
			resp.Diagnostics.AddAttributeError(sensitivePath, "Property set twice",
				fmt.Sprintf("The property %q is in both properties and sensitive_properties. Keep it in one.", name))
		}
		if config.SensitiveProperties.Elements()[name].IsNull() {
			resp.Diagnostics.AddAttributeError(sensitivePath, "Unsupported property value",
				fmt.Sprintf("The sensitive property %q cannot be null. To stop managing it, remove it.", name))
		}
	}
}

func reservedPropertyDetail(name string) string {
	if name == configurationNameProperty {
		return "The name of the instance is set with the name argument of the resource, not as a property."
	}
	return fmt.Sprintf("%q is a field that SAS Viya manages, not a property of the definition.", name)
}

func (r *configurationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan configurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	definition := plan.Definition.ValueString()
	var services []string
	resp.Diagnostics.Append(plan.Services.ElementsAs(ctx, &services, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	slices.Sort(services)

	existing, err := r.client.ListConfigurations(ctx, definition)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list configurations", err.Error())
		return
	}

	var matches []viya.Configuration
	for _, candidate := range existing {
		if configurationMatches(candidate, services, plan.Name) {
			matches = append(matches, candidate)
		}
	}

	var result *viya.Configuration
	switch len(matches) {
	case 0:
		result = r.createInstance(ctx, plan, services, existing, &resp.Diagnostics)
		plan.Adopted = types.BoolValue(false)
	case 1:
		result = r.writeProperties(ctx, matches[0].ID, plan, &resp.Diagnostics)
		plan.Adopted = types.BoolValue(true)
	default:
		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.ID)
		}
		resp.Diagnostics.AddError(
			"Ambiguous configuration",
			fmt.Sprintf("SAS Viya has %d instances of %q for the services %s%s: %s. Terraform cannot tell "+
				"which one to manage. Set the name argument if the instances have names, or import the "+
				"one you want by its ID.",
				len(matches), definition, strings.Join(services, ", "), nameSuffix(plan.Name), strings.Join(ids, ", ")),
		)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = types.StringValue(result.ID)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *configurationResource) createInstance(ctx context.Context, plan configurationModel, services []string, siblings []viya.Configuration, diags *diag.Diagnostics) *viya.Configuration {
	definition := plan.Definition.ValueString()

	// The media type carries the version of the definition. Other instances
	// of the definition already have it, which spares listing every
	// definition of the deployment.
	var mediaType string
	if len(siblings) > 0 {
		mediaType = siblings[0].Metadata.MediaType
	} else {
		var err error
		mediaType, err = r.client.DefinitionMediaType(ctx, definition)
		if err != nil {
			if errors.Is(err, viya.ErrDefinitionNotFound) {
				diags.AddAttributeError(path.Root("definition"), "Unknown configuration definition",
					fmt.Sprintf("SAS Viya has no configuration definition named %q.", definition))
				return nil
			}
			diags.AddError("Unable to read configuration definition", err.Error())
			return nil
		}
	}

	configuration := viya.Configuration{
		Metadata: viya.ConfigurationMetadata{
			MediaType: mediaType,
			Services:  services,
			IsDefault: false,
		},
		Properties: map[string]any{},
	}
	if !plan.Name.IsNull() {
		configuration.Properties[configurationNameProperty] = plan.Name.ValueString()
	}
	applyPlannedProperties(ctx, &configuration, plan, diags)
	if diags.HasError() {
		return nil
	}

	created, err := r.client.CreateConfiguration(ctx, configuration)
	if err != nil {
		diags.AddError("Unable to create configuration", err.Error())
		return nil
	}
	return created
}

// writeProperties applies the planned properties to an existing instance,
// leaving its other properties as they are.
func (r *configurationResource) writeProperties(ctx context.Context, id string, plan configurationModel, diags *diag.Diagnostics) *viya.Configuration {
	// Read the whole instance: the update replaces it, so the properties
	// that Terraform does not manage must be sent back unchanged.
	current, err := r.client.GetConfiguration(ctx, id)
	if err != nil {
		diags.AddError("Unable to read configuration before update", err.Error())
		return nil
	}

	applyPlannedProperties(ctx, current, plan, diags)
	if diags.HasError() {
		return nil
	}
	// A service overwrites an instance flagged as default when it starts.
	current.Metadata.IsDefault = false

	updated, err := r.client.UpdateConfiguration(ctx, *current)
	if err != nil {
		if viya.IsPreconditionFailed(err) {
			diags.AddError(
				"Configuration was modified concurrently",
				fmt.Sprintf("The configuration %q changed in SAS Viya while Terraform was updating it. "+
					"Run the operation again.", id),
			)
			return nil
		}
		diags.AddError("Unable to update configuration", err.Error())
		return nil
	}
	return updated
}

func applyPlannedProperties(ctx context.Context, configuration *viya.Configuration, plan configurationModel, diags *diag.Diagnostics) {
	if configuration.Properties == nil {
		configuration.Properties = map[string]any{}
	}

	if properties, ok := propertyValues(plan.Properties); ok {
		for name, value := range properties {
			converted, ok := propertyToAPI(value)
			if !ok {
				diags.AddAttributeError(path.Root("properties"), "Unsupported property value",
					fmt.Sprintf("The property %q must be a string, a number or a boolean.", name))
				continue
			}
			configuration.Properties[name] = converted
		}
	}

	if !plan.SensitiveProperties.IsNull() && !plan.SensitiveProperties.IsUnknown() {
		var secrets map[string]string
		diags.Append(plan.SensitiveProperties.ElementsAs(ctx, &secrets, false)...)
		for name, value := range secrets {
			configuration.Properties[name] = value
		}
	}
}

func (r *configurationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state configurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	configuration, err := r.client.GetConfiguration(ctx, state.ID.ValueString())
	if err != nil {
		if viya.IsNotFound(err) {
			// Deleted outside Terraform: drop it from the state so that
			// the next plan proposes to create it again.
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read configuration", err.Error())
		return
	}

	// An import only knows the ID. Everything else, including the fact that
	// the instance pre-existed, is filled in here.
	imported := state.Adopted.IsNull()

	state.Definition = types.StringValue(configuration.DefinitionName())
	services, diags := types.SetValueFrom(ctx, types.StringType, configuration.Metadata.Services)
	resp.Diagnostics.Append(diags...)
	state.Services = services
	state.Name = types.StringNull()
	if name, ok := configuration.Properties[configurationNameProperty].(string); ok {
		state.Name = types.StringValue(name)
	}

	if imported {
		// Properties are left empty rather than copied from the instance:
		// SAS Viya returns passwords in clear text, and properties is not a
		// sensitive attribute. The configuration names what to manage.
		state.Adopted = types.BoolValue(true)
		state.Properties = types.DynamicNull()
		state.SensitiveProperties = types.MapNull(types.StringType)
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}

	if !state.Properties.IsNull() {
		// Refresh only the properties that Terraform manages.
		managed, _ := propertyValues(state.Properties)
		values := make(map[string]attr.Value, len(managed))
		for name, prior := range managed {
			value, present := configuration.Properties[name]
			if !present || value == nil {
				// Removed outside Terraform: the plan will set it again.
				continue
			}
			values[name] = propertyFromAPI(value, prior)
		}
		state.Properties = propertiesValue(ctx, values, state.Properties)
	}
	if !state.SensitiveProperties.IsNull() {
		secrets := make(map[string]attr.Value, len(state.SensitiveProperties.Elements()))
		for name := range state.SensitiveProperties.Elements() {
			value, present := configuration.Properties[name]
			if !present || value == nil {
				// Removed outside Terraform: the plan will set it again.
				continue
			}
			secrets[name] = propertyFromAPI(value, types.StringValue(""))
		}
		state.SensitiveProperties = types.MapValueMust(types.StringType, secrets)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *configurationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan configurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.writeProperties(ctx, plan.ID.ValueString(), plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *configurationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state configurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.Adopted.ValueBool() {
		resp.Diagnostics.AddWarning(
			"Configuration left in place",
			fmt.Sprintf("The configuration %q (%s) existed before Terraform managed it, so it was not "+
				"deleted. It keeps its current values in SAS Viya and is no longer managed by Terraform.",
				state.ID.ValueString(), state.Definition.ValueString()),
		)
		return
	}

	// An instance that is already gone is the desired outcome.
	if err := r.client.DeleteConfiguration(ctx, state.ID.ValueString()); err != nil && !viya.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to delete configuration", err.Error())
	}
}

func (r *configurationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// configurationMatches reports whether an instance is the one that a
// resource describes: same services and same name. The services argument
// must be sorted.
func configurationMatches(candidate viya.Configuration, services []string, name types.String) bool {
	candidateServices := slices.Clone(candidate.Metadata.Services)
	slices.Sort(candidateServices)
	if !slices.Equal(candidateServices, services) {
		return false
	}

	candidateName, named := candidate.Properties[configurationNameProperty]
	if name.IsNull() {
		return !named || candidateName == nil
	}
	return candidateName == name.ValueString()
}

func nameSuffix(name types.String) string {
	if name.IsNull() {
		return " and no name"
	}
	return fmt.Sprintf(" and the name %q", name.ValueString())
}
