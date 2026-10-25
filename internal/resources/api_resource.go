package resources

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &apiResource{}
	_ resource.ResourceWithConfigure   = &apiResource{}
	_ resource.ResourceWithImportState = &apiResource{}
)

// NewAPIResource is a helper function to simplify the provider implementation.
func NewAPIResource() resource.Resource {
	return &apiResource{}
}

// apiResource is the resource implementation.
type apiResource struct {
	client *client.Client
}

// apiResourceModel maps the resource schema data.
type apiResourceModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	Resource         types.String `tfsdk:"resource"`
	AllowCIMDClients types.Bool   `tfsdk:"allow_cimd_clients"`
	Permissions      types.Map    `tfsdk:"permissions"`
}

// apiPermissionModel maps one entry of the permissions attribute.
type apiPermissionModel struct {
	ID                    types.String `tfsdk:"id"`
	Name                  types.String `tfsdk:"name"`
	Description           types.String `tfsdk:"description"`
	AllowedForCIMDClients types.Bool   `tfsdk:"allowed_for_cimd_clients"`
}

var apiPermissionAttrTypes = map[string]attr.Type{
	"id":                       types.StringType,
	"name":                     types.StringType,
	"description":              types.StringType,
	"allowed_for_cimd_clients": types.BoolType,
}

var apiPermissionObjectType = types.ObjectType{AttrTypes: apiPermissionAttrTypes}

// Metadata returns the resource type name.
func (r *apiResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api"
}

// Schema defines the schema for the resource.
func (r *apiResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an API in Pocket-ID: a resource server that OIDC clients can request access tokens for, with the permissions a token can carry.",
		MarkdownDescription: "Manages an [API](https://pocket-id.org/docs/guides/apis) in Pocket-ID: a resource server that OIDC clients can request access tokens for, " +
			"with the permissions a token can carry. A client asks for a token for an API with the `resource` parameter of its token request, " +
			"and the token's `aud` is the API's `resource` and its `scope` the permissions the client is granted. " +
			"Which clients may request which permissions is set with `pocketid_api_client_access`.\n\n" +
			"Pocket-ID replaces the permissions of an API as a whole and matches them by key, so they are an attribute of the API here, not a resource of their own.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The ID of the API.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The friendly name of the API.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 50),
				},
			},
			"resource": schema.StringAttribute{
				Description: "The resource identifier of the API, usually a URL such as `https://api.example.com`. It is the `resource` of a token request and the `aud` of the token. " +
					"Pocket-ID cannot change it, so changing it replaces the API, and with it every permission and client access.",
				MarkdownDescription: "The resource identifier of the API, usually a URL such as `https://api.example.com`. It need not be a page that exists. " +
					"It is the `resource` of a token request and the `aud` of the token. Pocket-ID cannot change it, so changing it replaces the API, and with it every permission and client access. " +
					"It must not end with a slash: Pocket-ID stores the resource without trailing slashes.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 350),
					stringvalidator.RegexMatches(regexp.MustCompile(`[^/]$`), "must not end with a slash, Pocket-ID stores the resource without trailing slashes"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"allow_cimd_clients": schema.BoolAttribute{
				Description: "Whether every client registered through a Client ID Metadata Document may use the API on behalf of a user, " +
					"limited to the permissions with `allowed_for_cimd_clients`. It covers current and future metadata-document clients, and never client access. Defaults to false.",
				MarkdownDescription: "Whether every client registered through a [Client ID Metadata Document](https://pocket-id.org/docs/guides/client-id-metadata-documents) may use the API on behalf of a user, " +
					"limited to the permissions with `allowed_for_cimd_clients`. It covers current and future metadata-document clients, and never client access (M2M), because these clients are public. Defaults to false.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"permissions": schema.MapNestedAttribute{
				Description: "The permissions of the API, by key. The key is what a client puts in the `scope` of a token request, such as `read:orders`. " +
					"Setting this attribute replaces all permissions of the API: a permission that is left out is deleted, with every client's access to it. Defaults to none.",
				MarkdownDescription: "The permissions of the API, by key. The key is what a client puts in the `scope` of a token request, such as `read:orders`. " +
					"Setting this attribute replaces all permissions of the API: a permission that is left out is deleted, with every client's access to it. " +
					"Pocket-ID rejects keys with characters that are not valid in an OAuth scope, and the reserved keys `openid`, `profile`, `email`, `email_verified`, `groups` and `offline_access`. Defaults to none.",
				Optional: true,
				Computed: true,
				Default:  mapdefault.StaticValue(types.MapValueMust(apiPermissionObjectType, map[string]attr.Value{})),
				Validators: []validator.Map{
					mapvalidator.KeysAre(stringvalidator.LengthBetween(1, 128)),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The ID of the permission. Client access refers to permissions by this ID.",
							Computed:    true,
							PlanModifiers: []planmodifier.String{
								// A new key has no prior id: the plain UseStateForUnknown would plan null.
								stringplanmodifier.UseNonNullStateForUnknown(),
							},
						},
						"name": schema.StringAttribute{
							Description: "The friendly name of the permission, shown to users when they are asked to consent.",
							Required:    true,
							Validators: []validator.String{
								stringvalidator.LengthBetween(1, 50),
							},
						},
						"description": schema.StringAttribute{
							Description: "What the permission allows.",
							Optional:    true,
							Validators: []validator.String{
								stringvalidator.LengthAtMost(200),
							},
						},
						"allowed_for_cimd_clients": schema.BoolAttribute{
							Description: "Whether metadata-document clients may request the permission when `allow_cimd_clients` is on. Pocket-ID keeps the selection while `allow_cimd_clients` is off. Defaults to false.",
							Optional:    true,
							Computed:    true,
							Default:     booldefault.StaticBool(false),
						},
					},
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *apiResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = client
}

// Create creates the resource and sets the initial Terraform state.
func (r *apiResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan apiResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating API", map[string]any{
		"name":     plan.Name.ValueString(),
		"resource": plan.Resource.ValueString(),
	})

	created, err := r.client.CreateAPI(&client.APICreateRequest{
		Name:     plan.Name.ValueString(),
		Resource: plan.Resource.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating API",
			"Could not create API, unexpected error: "+err.Error(),
		)
		return
	}

	api, err := r.applyPermissions(ctx, created, plan)
	if err != nil {
		// Try to clean up the created API
		_ = r.client.DeleteAPI(created.ID)
		resp.Diagnostics.AddError(
			"Error setting API permissions",
			"Could not set the API permissions, the API was deleted. Error: "+err.Error(),
		)
		return
	}

	state, diags := apiToModel(api)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *apiResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state apiResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	api, err := r.client.GetAPI(state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading API",
			"Could not read API ID "+state.ID.ValueString()+": "+err.Error(),
		)
		return
	}

	state, diags := apiToModel(api)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *apiResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state apiResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	api, err := r.client.GetAPI(plan.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading API",
			"Could not read API ID "+plan.ID.ValueString()+": "+err.Error(),
		)
		return
	}

	if api.Name != plan.Name.ValueString() {
		api, err = r.client.UpdateAPI(plan.ID.ValueString(), &client.APIUpdateRequest{Name: plan.Name.ValueString()})
		if err != nil {
			resp.Diagnostics.AddError(
				"Error updating API",
				"Could not update API, unexpected error: "+err.Error(),
			)
			return
		}
	}

	api, err = r.applyPermissions(ctx, api, plan)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error setting API permissions",
			"Could not set the API permissions: "+err.Error(),
		)
		return
	}

	newState, diags := apiToModel(api)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *apiResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state apiResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting API", map[string]any{"id": state.ID.ValueString()})

	err := r.client.DeleteAPI(state.ID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError(
			"Error deleting API",
			"Could not delete API, unexpected error: "+err.Error(),
		)
	}
}

// ImportState imports an existing resource into Terraform.
func (r *apiResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// applyPermissions makes the permissions of the API, and its metadata-document
// access, what the plan says. Each call is made only when the API differs, as
// Pocket-ID replaces the whole set every time. It returns the API as it is
// afterwards.
func (r *apiResource) applyPermissions(ctx context.Context, api *client.API, plan apiResourceModel) (*client.API, error) {
	var planned map[string]apiPermissionModel
	if diags := plan.Permissions.ElementsAs(ctx, &planned, false); diags.HasError() {
		return nil, fmt.Errorf("invalid permissions: %v", diags)
	}

	inputs := make([]client.APIPermissionInput, 0, len(planned))
	for _, key := range sortedKeys(planned) {
		permission := planned[key]
		input := client.APIPermissionInput{Key: key, Name: permission.Name.ValueString()}
		if !permission.Description.IsNull() {
			description := permission.Description.ValueString()
			input.Description = &description
		}
		inputs = append(inputs, input)
	}

	var err error
	if !samePermissions(api.Permissions, inputs) {
		tflog.Debug(ctx, "Updating API permissions", map[string]any{"id": api.ID})
		api, err = r.client.UpdateAPIPermissions(api.ID, inputs)
		if err != nil {
			return nil, err
		}
	}

	// The permission IDs are only known now, so the selection is made by key.
	selected := make([]string, 0, len(planned))
	for _, permission := range api.Permissions {
		if planned[permission.Key].AllowedForCIMDClients.ValueBool() {
			selected = append(selected, permission.ID)
		}
	}
	if !sameCIMDAccess(api, plan.AllowCIMDClients.ValueBool(), selected) {
		tflog.Debug(ctx, "Updating API metadata-document client access", map[string]any{"id": api.ID})
		api, err = r.client.UpdateAPICIMDAccess(api.ID, plan.AllowCIMDClients.ValueBool(), selected)
		if err != nil {
			return nil, err
		}
	}

	return api, nil
}

// samePermissions reports whether the API's permissions are the given ones,
// by key, name and description.
func samePermissions(current []client.APIPermission, wanted []client.APIPermissionInput) bool {
	if len(current) != len(wanted) {
		return false
	}
	byKey := make(map[string]client.APIPermission, len(current))
	for _, permission := range current {
		byKey[permission.Key] = permission
	}
	for _, in := range wanted {
		permission, ok := byKey[in.Key]
		if !ok || permission.Name != in.Name {
			return false
		}
		if (permission.Description == nil) != (in.Description == nil) {
			return false
		}
		if in.Description != nil && *permission.Description != *in.Description {
			return false
		}
	}
	return true
}

// sameCIMDAccess reports whether the API has metadata-document access set to
// enabled, with exactly the selected permission IDs.
func sameCIMDAccess(api *client.API, enabled bool, selected []string) bool {
	if api.AllowCIMDClients != enabled {
		return false
	}
	current := map[string]bool{}
	for _, permission := range api.Permissions {
		if permission.AllowedForCIMDClients {
			current[permission.ID] = true
		}
	}
	if len(current) != len(selected) {
		return false
	}
	for _, id := range selected {
		if !current[id] {
			return false
		}
	}
	return true
}

// apiToModel maps an API to the resource model.
func apiToModel(api *client.API) (apiResourceModel, diag.Diagnostics) {
	permissions := make(map[string]attr.Value, len(api.Permissions))
	for _, permission := range api.Permissions {
		description := types.StringNull()
		if permission.Description != nil {
			description = types.StringValue(*permission.Description)
		}
		permissions[permission.Key] = types.ObjectValueMust(apiPermissionAttrTypes, map[string]attr.Value{
			"id":                       types.StringValue(permission.ID),
			"name":                     types.StringValue(permission.Name),
			"description":              description,
			"allowed_for_cimd_clients": types.BoolValue(permission.AllowedForCIMDClients),
		})
	}

	permissionsMap, diags := types.MapValue(apiPermissionObjectType, permissions)
	return apiResourceModel{
		ID:               types.StringValue(api.ID),
		Name:             types.StringValue(api.Name),
		Resource:         types.StringValue(api.Resource),
		AllowCIMDClients: types.BoolValue(api.AllowCIMDClients),
		Permissions:      permissionsMap,
	}, diags
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
