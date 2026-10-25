package resources

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &apiClientAccessResource{}
	_ resource.ResourceWithConfigure   = &apiClientAccessResource{}
	_ resource.ResourceWithImportState = &apiClientAccessResource{}
	_ resource.ResourceWithModifyPlan  = &apiClientAccessResource{}
)

// NewAPIClientAccessResource is a helper function to simplify the provider implementation.
func NewAPIClientAccessResource() resource.Resource {
	return &apiClientAccessResource{}
}

// apiClientAccessResource is the resource implementation.
type apiClientAccessResource struct {
	client *client.Client
}

// apiClientAccessResourceModel maps the resource schema data.
type apiClientAccessResourceModel struct {
	ID                         types.String `tfsdk:"id"`
	APIID                      types.String `tfsdk:"api_id"`
	ClientID                   types.String `tfsdk:"client_id"`
	UserDelegatedAccess        types.Bool   `tfsdk:"user_delegated_access"`
	UserDelegatedPermissionIDs types.Set    `tfsdk:"user_delegated_permission_ids"`
	ClientAccess               types.Bool   `tfsdk:"client_access"`
	ClientPermissionIDs        types.Set    `tfsdk:"client_permission_ids"`
}

// Metadata returns the resource type name.
func (r *apiClientAccessResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_client_access"
}

// Schema defines the schema for the resource.
func (r *apiClientAccessResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	emptyIDs := setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{}))

	resp.Schema = schema.Schema{
		Description: "Manages what one OIDC client may do with one API in Pocket-ID: the permissions it may request, for users (user-delegated access) and as itself (client access, machine to machine).",
		MarkdownDescription: "Manages what one OIDC client may do with one API (`pocketid_api`) in Pocket-ID, with the permissions it may request. " +
			"There are two kinds of access:\n\n" +
			"- **User-delegated access**: the client calls the API on behalf of a signed-in user. Pocket-ID may ask the user to consent to the permissions.\n" +
			"- **Client access (M2M)**: the client calls the API as itself, with the `client_credentials` grant, and no user. " +
			"A public client cannot have it, because it cannot keep a secret.\n\n" +
			"A client with client access gets a token for the API when it asks for one with `grant_type=client_credentials`, `resource=<the API's resource>` and `scope=<permission keys>`. " +
			"The token's `aud` is the API's resource and its `scope` the permissions that were requested and are granted here; without a `scope`, all the permissions granted. " +
			"A client without access to the API is refused: with `access_denied` when it asks for the API, and with `invalid_scope` when it also asks for a permission.\n\n" +
			"The resource sets the client's grants on the API as a whole: a permission that is not listed is revoked, and destroying the resource revokes everything the client has on the API. " +
			"Its grants on other APIs are untouched. The access to metadata-document clients that the API itself gives is set on `pocketid_api` and is not shown here.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The ID of the grant: the API ID and the client ID, separated by a `/`.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"api_id": schema.StringAttribute{
				Description: "The ID of the API (`pocketid_api.id`). Changing it replaces the resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"client_id": schema.StringAttribute{
				Description: "The ID of the OIDC client (`pocketid_client.id`). Changing it replaces the resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_delegated_access": schema.BoolAttribute{
				Description: "Whether the client may use the API on behalf of a user. Defaults to whether `user_delegated_permission_ids` has any permission: Pocket-ID counts a permission as access. " +
					"Set it to true without permissions for access that gives a token no scope.",
				Optional: true,
				Computed: true,
			},
			"user_delegated_permission_ids": schema.SetAttribute{
				Description: "The IDs of the permissions of the API (`pocketid_api.permissions[key].id`) the client may request on behalf of a user. Defaults to none.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Default:     emptyIDs,
			},
			"client_access": schema.BoolAttribute{
				Description: "Whether the client may use the API as itself (machine to machine). Defaults to whether `client_permission_ids` has any permission: Pocket-ID counts a permission as access. " +
					"Set it to true without permissions for access that gives a token no scope. Pocket-ID refuses it for a public client.",
				Optional: true,
				Computed: true,
			},
			"client_permission_ids": schema.SetAttribute{
				Description: "The IDs of the permissions of the API (`pocketid_api.permissions[key].id`) the client may request as itself. Defaults to none.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Default:     emptyIDs,
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *apiClientAccessResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan resolves the two access flags, which Pocket-ID derives from the
// permissions: a permission is access for its kind. A flag that is not set is
// true exactly when its permissions are not empty, and a flag set to false next
// to permissions would be turned on by Pocket-ID, so it is an error here
// instead of a plan that cannot be applied.
func (r *apiClientAccessResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}

	var plan apiClientAccessResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var userConfig, clientConfig types.Bool
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("user_delegated_access"), &userConfig)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("client_access"), &clientConfig)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resolveAccess(&resp.Diagnostics, path.Root("user_delegated_access"), "user_delegated_permission_ids", userConfig, plan.UserDelegatedPermissionIDs, &plan.UserDelegatedAccess)
	resolveAccess(&resp.Diagnostics, path.Root("client_access"), "client_permission_ids", clientConfig, plan.ClientPermissionIDs, &plan.ClientAccess)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.UserDelegatedAccess.IsUnknown() && !plan.ClientAccess.IsUnknown() &&
		!plan.UserDelegatedAccess.ValueBool() && !plan.ClientAccess.ValueBool() {
		resp.Diagnostics.AddError(
			"No access granted",
			"Set user_delegated_access or client_access, or list permissions for one of them. A client with neither has no grant on the API, so there is nothing to manage.",
		)
		return
	}

	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// resolveAccess sets the planned value of an access flag from its config and
// permissions. The flag stays unknown while the permissions are.
func resolveAccess(diags *diag.Diagnostics, flagPath path.Path, permissionsAttr string, config types.Bool, permissions types.Set, planned *types.Bool) {
	if permissions.IsUnknown() {
		if config.IsNull() {
			*planned = types.BoolUnknown()
		}
		return
	}

	hasPermissions := len(permissions.Elements()) > 0
	switch {
	case config.IsNull():
		*planned = types.BoolValue(hasPermissions)
	case !config.IsUnknown() && !config.ValueBool() && hasPermissions:
		diags.AddAttributeError(
			flagPath,
			"Access is false but permissions are listed",
			fmt.Sprintf("Pocket-ID counts a permission as access, so %s cannot be false while %s is not empty.", flagPath, permissionsAttr),
		)
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *apiClientAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan apiClientAccessResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.setAccess(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Error setting API client access", err.Error())
		return
	}

	plan.ID = types.StringValue(plan.APIID.ValueString() + "/" + plan.ClientID.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *apiClientAccessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state apiClientAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	grants, err := r.client.ListClientAPIs(state.ClientID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading API client access",
			"Could not read the APIs of client "+state.ClientID.ValueString()+": "+err.Error(),
		)
		return
	}

	for _, grant := range grants {
		if grant.API.ID != state.APIID.ValueString() {
			continue
		}
		// An API in the list can be there only through the metadata-document
		// opt-in, which is not this resource's grant.
		if !grant.UserDelegatedAccess && !grant.ClientAccess &&
			len(grant.UserDelegatedPermissionIDs) == 0 && len(grant.ClientPermissionIDs) == 0 {
			break
		}

		state.UserDelegatedAccess = types.BoolValue(grant.UserDelegatedAccess)
		state.ClientAccess = types.BoolValue(grant.ClientAccess)
		state.UserDelegatedPermissionIDs = idSet(grant.UserDelegatedPermissionIDs)
		state.ClientPermissionIDs = idSet(grant.ClientPermissionIDs)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	resp.State.RemoveResource(ctx)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *apiClientAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan apiClientAccessResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.setAccess(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Error setting API client access", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *apiClientAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state apiClientAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Removing API client access", map[string]any{
		"api_id":    state.APIID.ValueString(),
		"client_id": state.ClientID.ValueString(),
	})

	err := r.client.RemoveAPIClientAccess(state.APIID.ValueString(), state.ClientID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError(
			"Error removing API client access",
			"Could not remove the client's access to the API, unexpected error: "+err.Error(),
		)
	}
}

// ImportState imports an existing resource into Terraform. The ID is the API ID
// and the client ID, separated by a `/`.
func (r *apiClientAccessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	apiID, clientID, ok := strings.Cut(req.ID, "/")
	if !ok || apiID == "" || clientID == "" || strings.Contains(clientID, "/") {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected <api_id>/<client_id>, got %q.", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("api_id"), apiID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("client_id"), clientID)...)
}

// setAccess replaces the client's grants on the API with the plan, and checks
// that Pocket-ID applied exactly that. Pocket-ID ignores permission IDs that are
// not the API's and drops client access for a public client, both with a 200,
// which would otherwise surface as a state that differs from the plan.
func (r *apiClientAccessResource) setAccess(ctx context.Context, plan apiClientAccessResourceModel) error {
	var userIDs, clientIDs []string
	if diags := plan.UserDelegatedPermissionIDs.ElementsAs(ctx, &userIDs, false); diags.HasError() {
		return fmt.Errorf("invalid user_delegated_permission_ids: %v", diags)
	}
	if diags := plan.ClientPermissionIDs.ElementsAs(ctx, &clientIDs, false); diags.HasError() {
		return fmt.Errorf("invalid client_permission_ids: %v", diags)
	}

	wanted := client.APIClientGrant{
		UserDelegatedAccess:        plan.UserDelegatedAccess.ValueBool(),
		ClientAccess:               plan.ClientAccess.ValueBool(),
		UserDelegatedPermissionIDs: userIDs,
		ClientPermissionIDs:        clientIDs,
	}

	tflog.Debug(ctx, "Setting API client access", map[string]any{
		"api_id":    plan.APIID.ValueString(),
		"client_id": plan.ClientID.ValueString(),
	})

	applied, err := r.client.SetAPIClientAccess(plan.APIID.ValueString(), plan.ClientID.ValueString(), &wanted)
	if err != nil {
		return fmt.Errorf("could not set the client's access to the API: %w", err)
	}

	if problems := grantDifferences(wanted, *applied); len(problems) > 0 {
		// Do not leave a grant that is not the one that was asked for.
		_ = r.client.RemoveAPIClientAccess(plan.APIID.ValueString(), plan.ClientID.ValueString())
		return fmt.Errorf("the access was not applied as asked and the grant was removed: %s", strings.Join(problems, "; "))
	}

	return nil
}

// grantDifferences describes how what Pocket-ID applied differs from what was
// asked for.
func grantDifferences(wanted, applied client.APIClientGrant) []string {
	var problems []string

	if wanted.UserDelegatedAccess != applied.UserDelegatedAccess {
		problems = append(problems, fmt.Sprintf("user_delegated_access is %t, not %t", applied.UserDelegatedAccess, wanted.UserDelegatedAccess))
	}
	if wanted.ClientAccess != applied.ClientAccess {
		problems = append(problems, fmt.Sprintf("client_access is %t, not %t (a public client cannot have client access)", applied.ClientAccess, wanted.ClientAccess))
	}
	if missing := missingIDs(wanted.UserDelegatedPermissionIDs, applied.UserDelegatedPermissionIDs); len(missing) > 0 {
		problems = append(problems, "user_delegated_permission_ids not applied (not permissions of the API?): "+strings.Join(missing, ", "))
	}
	if missing := missingIDs(wanted.ClientPermissionIDs, applied.ClientPermissionIDs); len(missing) > 0 {
		problems = append(problems, "client_permission_ids not applied (not permissions of the API, or a public client?): "+strings.Join(missing, ", "))
	}

	return problems
}

// missingIDs returns the IDs in wanted that are not in applied.
func missingIDs(wanted, applied []string) []string {
	have := make(map[string]struct{}, len(applied))
	for _, id := range applied {
		have[id] = struct{}{}
	}
	var missing []string
	for _, id := range wanted {
		if _, ok := have[id]; !ok {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return missing
}

// idSet converts permission IDs to a set. No IDs is an empty set, not null.
func idSet(ids []string) types.Set {
	values := make([]attr.Value, 0, len(ids))
	for _, id := range ids {
		values = append(values, types.StringValue(id))
	}
	return types.SetValueMust(types.StringType, values)
}
