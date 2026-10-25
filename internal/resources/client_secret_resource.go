package resources

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timetypes/timetypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &clientSecretResource{}
	_ resource.ResourceWithConfigure   = &clientSecretResource{}
	_ resource.ResourceWithImportState = &clientSecretResource{}
)

var (
	// clientIDPattern matches the client IDs Pocket ID accepts (its
	// validateClientIDRegex).
	clientIDPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

	// printableASCIIPattern matches what Pocket ID's printascii binding accepts.
	printableASCIIPattern = regexp.MustCompile(`^[\x20-\x7E]+$`)
)

// NewClientSecretResource is a helper function to simplify the provider implementation.
func NewClientSecretResource() resource.Resource {
	return &clientSecretResource{}
}

// clientSecretResource is the resource implementation. It maps one to one to a
// client secret of Pocket ID: the secrets of a client are created and deleted,
// never updated.
type clientSecretResource struct {
	client *client.Client
}

// clientSecretResourceModel maps the resource schema data.
type clientSecretResourceModel struct {
	ID              types.String      `tfsdk:"id"`
	ClientID        types.String      `tfsdk:"client_id"`
	SecretWO        types.String      `tfsdk:"secret_wo"`
	SecretWOVersion types.Int64       `tfsdk:"secret_wo_version"`
	Value           types.String      `tfsdk:"value"`
	Prefix          types.String      `tfsdk:"prefix"`
	CreatedAt       types.String      `tfsdk:"created_at"`
	ExpiresAt       timetypes.RFC3339 `tfsdk:"expires_at"`
}

// Metadata returns the resource type name.
func (r *clientSecretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_client_secret"
}

// Schema defines the schema for the resource.
func (r *clientSecretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a secret of an OIDC client in Pocket-ID.",
		MarkdownDescription: `Manages a secret of an OIDC client in Pocket-ID. A client can have several secrets at the same time (up to 20, expired ones included), and each of them authenticates the client. Pocket ID has no way to change a secret, so changing any argument replaces it.

Pocket ID stores only a hash of the secret. Either supply the value in the write-only ` + "`secret_wo`" + ` argument, so that it is never stored in the plan or the state, or let Pocket ID generate it and read it from ` + "`value`" + `, which is stored in the state.

~> **Note** A public client has no secrets, so Pocket ID rejects a secret for a client with ` + "`is_public = true`" + `.`,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The ID of the client secret, as assigned by Pocket ID.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"client_id": schema.StringAttribute{
				Description: "The ID of the OIDC client the secret belongs to. Changing it replaces the secret.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						clientIDPattern,
						"Client ID must contain only letters, digits, '.', '_' and '-'",
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"secret_wo": schema.StringAttribute{
				Description: "The value of the secret, as a write-only argument that is never stored in the plan or the " +
					"state. It must have at least 16 characters, all of them printable ASCII. When it is not set, " +
					"Pocket ID generates the secret and returns it in `value`. Changing it does not change the secret " +
					"on its own; increment `secret_wo_version` to replace the secret with the new value.",
				Optional:  true,
				WriteOnly: true,
				Sensitive: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(16), // Matches the API binding (min=16)
					stringvalidator.RegexMatches(
						printableASCIIPattern,
						"Client secret must contain only printable ASCII characters",
					),
				},
			},
			"secret_wo_version": schema.Int64Attribute{
				Description: "Version of `secret_wo`. Changing it replaces the secret, which is how a new `secret_wo` " +
					"takes effect, because Terraform cannot see a change of a write-only argument. Requires `secret_wo`.",
				Optional: true,
				Validators: []validator.Int64{
					int64validator.AlsoRequires(path.MatchRoot("secret_wo")),
				},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"value": schema.StringAttribute{
				Description: "The value of the secret, set only when Pocket ID generated it because `secret_wo` was " +
					"not given, and null otherwise. Pocket ID returns it once, when it creates the secret.",
				Computed:  true,
				Sensitive: true,
			},
			"prefix": schema.StringAttribute{
				Description: "The first characters of the secret, which Pocket ID keeps in clear text so that its " +
					"secrets can be told apart. Empty for secrets Pocket ID migrated from before it supported several " +
					"secrets per client.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "When the secret was created, as an RFC 3339 timestamp.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"expires_at": schema.StringAttribute{
				Description: "When the secret stops working, as an RFC 3339 timestamp in the future. The secret does " +
					"not expire when this is not set. Use whole seconds: Pocket ID's default SQLite storage keeps no " +
					"finer time. Changing it replaces the secret.",
				Optional:   true,
				CustomType: timetypes.RFC3339Type{},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// Configure adds the provider configured client to the resource.
func (r *clientSecretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = c
}

// Create creates the secret and sets the initial Terraform state.
func (r *clientSecretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan clientSecretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	// Write-only values are null in the plan, so the secret is read from the configuration.
	var secretWO types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("secret_wo"), &secretWO)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := &client.ClientSecretCreateRequest{}
	if !secretWO.IsNull() {
		// Keep the value out of the provider logs, which include request bodies.
		ctx = tflog.MaskAllFieldValuesStrings(ctx, secretWO.ValueString())
		createReq.Secret = secretWO.ValueString()
	}
	if !plan.ExpiresAt.IsNull() {
		expiresAt := plan.ExpiresAt.ValueString()
		createReq.ExpiresAt = &expiresAt
	}

	clientID := plan.ClientID.ValueString()
	tflog.Debug(ctx, "Creating OIDC client secret", map[string]any{
		"client_id": clientID,
	})

	created, err := r.client.CreateClientSecret(ctx, clientID, createReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating OIDC client secret",
			"Could not create a secret for OIDC client "+clientID+": "+err.Error(),
		)
		return
	}

	plan.ID = types.StringValue(created.ID)
	plan.Prefix = types.StringValue(created.Prefix)
	plan.CreatedAt = types.StringValue(created.CreatedAt)
	plan.SecretWO = types.StringNull()
	if secretWO.IsNull() {
		plan.Value = types.StringValue(created.Secret)
	} else {
		plan.Value = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data. The secret is
// removed from the state when it, or its client, no longer exists.
func (r *clientSecretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state clientSecretResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clientID := state.ClientID.ValueString()
	secrets, err := r.client.ListClientSecrets(ctx, clientID)
	if client.IsNotFound(err) {
		tflog.Warn(ctx, "OIDC client not found, removing its secret from the state", map[string]any{
			"client_id": clientID,
		})
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading OIDC client secret",
			"Could not read the secrets of OIDC client "+clientID+": "+err.Error(),
		)
		return
	}

	var found *client.ClientSecret
	for i := range secrets {
		if secrets[i].ID == state.ID.ValueString() {
			found = &secrets[i]
			break
		}
	}
	if found == nil {
		tflog.Warn(ctx, "OIDC client secret not found, removing it from the state", map[string]any{
			"client_id": clientID,
			"id":        state.ID.ValueString(),
		})
		resp.State.RemoveResource(ctx)
		return
	}

	state.Prefix = types.StringValue(found.Prefix)
	state.CreatedAt = types.StringValue(found.CreatedAt)
	if found.ExpiresAt != nil {
		expiresAt, diags := timetypes.NewRFC3339Value(*found.ExpiresAt)
		resp.Diagnostics.Append(diags...)
		state.ExpiresAt = expiresAt
	} else {
		state.ExpiresAt = timetypes.NewRFC3339Null()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called with a change: Pocket ID cannot change a secret, so
// every argument that can differ forces a new resource, and a write-only
// argument is not part of the plan.
func (r *clientSecretResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Error updating OIDC client secret",
		"Client secrets cannot be updated. This is a bug in the provider; please report it to the provider developers.",
	)
}

// Delete deletes the secret, which makes it unusable at once.
func (r *clientSecretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state clientSecretResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clientID := state.ClientID.ValueString()
	secretID := state.ID.ValueString()
	tflog.Debug(ctx, "Deleting OIDC client secret", map[string]any{
		"client_id": clientID,
		"id":        secretID,
	})

	err := r.client.DeleteClientSecret(ctx, clientID, secretID)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError(
			"Error deleting OIDC client secret",
			"Could not delete secret "+secretID+" of OIDC client "+clientID+": "+err.Error(),
		)
	}
}

// ImportState imports an existing secret, identified as "<client_id>/<secret_id>".
func (r *clientSecretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	clientID, secretID, err := parseClientSecretImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("client_id"), clientID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), secretID)...)
}

// parseClientSecretImportID splits an import ID of the form
// "<client_id>/<secret_id>". A client ID has no "/", because Pocket ID accepts
// only letters, digits, '.', '_' and '-' in it.
func parseClientSecretImportID(id string) (clientID, secretID string, err error) {
	parts := strings.Split(id, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || !clientIDPattern.MatchString(parts[0]) {
		return "", "", fmt.Errorf("expected an import ID of the form <client_id>/<secret_id>, got %q", id)
	}
	return parts[0], parts[1], nil
}
