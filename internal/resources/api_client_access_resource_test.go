package resources_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
	"github.com/Trozz/terraform-provider-pocketid/internal/resources"
)

// fakeAccessServer is a minimal Pocket-ID client-access API for one API and one
// client. The permissions of the API are perm-1 and perm-2. A public client
// has its client access dropped, as Pocket-ID does.
type fakeAccessServer struct {
	mu         sync.Mutex
	calls      []string
	grant      *client.APIClientGrant // nil when the client has no grant
	cimdOnly   bool                   // the API is in the list only through the CIMD opt-in
	public     bool
	clientGone bool
}

func (f *fakeAccessServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/apis/api-1/clients/client-1":
			var req client.APIClientGrant
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			valid := func(ids []string) []string {
				out := []string{}
				for _, id := range ids {
					if id == "perm-1" || id == "perm-2" {
						out = append(out, id)
					}
				}
				return out
			}
			applied := client.APIClientGrant{
				UserDelegatedPermissionIDs: valid(req.UserDelegatedPermissionIDs),
				ClientPermissionIDs:        valid(req.ClientPermissionIDs),
			}
			if f.public {
				req.ClientAccess = false
				applied.ClientPermissionIDs = []string{}
			}
			applied.UserDelegatedAccess = req.UserDelegatedAccess || len(applied.UserDelegatedPermissionIDs) > 0
			applied.ClientAccess = req.ClientAccess || len(applied.ClientPermissionIDs) > 0
			f.grant = &applied
			_ = json.NewEncoder(w).Encode(applied)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/apis/api-1/clients/client-1":
			f.grant = nil
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/api-access/client-1/apis":
			if f.clientGone {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"OIDC client not found"}`))
				return
			}
			grants := []map[string]any{}
			if f.grant != nil || f.cimdOnly {
				grant := client.APIClientGrant{UserDelegatedPermissionIDs: []string{}, ClientPermissionIDs: []string{}}
				if f.grant != nil {
					grant = *f.grant
				}
				grants = append(grants, map[string]any{
					"api":                        map[string]any{"id": "api-1"},
					"userDelegatedAccess":        grant.UserDelegatedAccess,
					"clientAccess":               grant.ClientAccess,
					"userDelegatedPermissionIds": grant.UserDelegatedPermissionIDs,
					"clientPermissionIds":        grant.ClientPermissionIDs,
					"cimdGrantedAccess":          f.cimdOnly,
					"cimdGrantedPermissionIds":   []string{},
				})
			}
			_ = json.NewEncoder(w).Encode(grants)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func newAccessResource(t *testing.T, f *fakeAccessServer) (resource.Resource, resource.SchemaResponse) {
	t.Helper()
	ctx := context.Background()
	r := resources.NewAPIClientAccessResource()
	r.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{
		ProviderData: createMockServer(t, f.handler(t)),
	}, &resource.ConfigureResponse{})

	var s resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &s)
	require.False(t, s.Diagnostics.HasError(), "%v", s.Diagnostics)
	return r, s
}

func accessIDs(ids ...string) types.Set {
	values := make([]attr.Value, 0, len(ids))
	for _, id := range ids {
		values = append(values, types.StringValue(id))
	}
	return types.SetValueMust(types.StringType, values)
}

// accessObject sets the attributes of a client access on a plan, a config or a
// state, whichever holds the setter.
type accessValues struct {
	id         types.String
	userAccess types.Bool
	userIDs    types.Set
	clientAcc  types.Bool
	clientIDs  types.Set
}

func (v accessValues) apply(t *testing.T, ctx context.Context, set func(path.Path, attr.Value) bool) {
	t.Helper()
	for attribute, value := range map[string]attr.Value{
		"id":                            v.id,
		"api_id":                        types.StringValue("api-1"),
		"client_id":                     types.StringValue("client-1"),
		"user_delegated_access":         v.userAccess,
		"user_delegated_permission_ids": v.userIDs,
		"client_access":                 v.clientAcc,
		"client_permission_ids":         v.clientIDs,
	} {
		require.True(t, set(path.Root(attribute), value), attribute)
	}
}

func accessPlan(ctx context.Context, t *testing.T, s resource.SchemaResponse, v accessValues) tfsdk.Plan {
	t.Helper()
	p := tfsdk.Plan{Schema: s.Schema, Raw: tftypes.NewValue(s.Schema.Type().TerraformType(ctx), nil)}
	v.apply(t, ctx, func(at path.Path, value attr.Value) bool { return !p.SetAttribute(ctx, at, value).HasError() })
	return p
}

func accessState(ctx context.Context, t *testing.T, s resource.SchemaResponse, v accessValues) tfsdk.State {
	t.Helper()
	st := tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Schema.Type().TerraformType(ctx), nil)}
	v.apply(t, ctx, func(at path.Path, value attr.Value) bool { return !st.SetAttribute(ctx, at, value).HasError() })
	return st
}

func accessConfig(ctx context.Context, t *testing.T, s resource.SchemaResponse, v accessValues) tfsdk.Config {
	t.Helper()
	// tfsdk.Config cannot be set, so the values are put in a state of the same schema.
	st := accessState(ctx, t, s, v)
	return tfsdk.Config{Schema: s.Schema, Raw: st.Raw}
}

func emptyAccessState(ctx context.Context, s resource.SchemaResponse) tfsdk.State {
	return tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Schema.Type().TerraformType(ctx), nil)}
}

func TestAPIClientAccessResource_Schema(t *testing.T) {
	_, s := newAccessResource(t, &fakeAccessServer{})
	attrs := s.Schema.Attributes

	assert.True(t, attrs["api_id"].IsRequired())
	assert.True(t, attrs["client_id"].IsRequired())
	for _, name := range []string{"user_delegated_access", "user_delegated_permission_ids", "client_access", "client_permission_ids"} {
		assert.True(t, attrs[name].IsOptional(), name)
		assert.True(t, attrs[name].IsComputed(), name)
	}
}

func TestAPIClientAccessResource_ModifyPlan(t *testing.T) {
	ctx := context.Background()
	_, s := newAccessResource(t, &fakeAccessServer{})
	r := resources.NewAPIClientAccessResource().(resource.ResourceWithModifyPlan)

	unknownIDs := types.SetUnknown(types.StringType)
	cases := []struct {
		name        string
		config      accessValues // the configured flags; null is not set
		plan        accessValues // the plan before ModifyPlan: flags unknown when not set
		wantUser    types.Bool
		wantClient  types.Bool
		wantErrText string
	}{
		{
			name:       "flags follow the permissions",
			config:     accessValues{id: types.StringUnknown(), userAccess: types.BoolNull(), userIDs: accessIDs(), clientAcc: types.BoolNull(), clientIDs: accessIDs("perm-1")},
			plan:       accessValues{id: types.StringUnknown(), userAccess: types.BoolUnknown(), userIDs: accessIDs(), clientAcc: types.BoolUnknown(), clientIDs: accessIDs("perm-1")},
			wantUser:   types.BoolValue(false),
			wantClient: types.BoolValue(true),
		},
		{
			name:       "permissions that are not known yet leave the flag unknown",
			config:     accessValues{id: types.StringUnknown(), userAccess: types.BoolNull(), userIDs: accessIDs(), clientAcc: types.BoolNull(), clientIDs: unknownIDs},
			plan:       accessValues{id: types.StringUnknown(), userAccess: types.BoolUnknown(), userIDs: accessIDs("perm-1"), clientAcc: types.BoolUnknown(), clientIDs: unknownIDs},
			wantUser:   types.BoolValue(true),
			wantClient: types.BoolUnknown(),
		},
		{
			name:       "a flag set to true without permissions stays true",
			config:     accessValues{id: types.StringUnknown(), userAccess: types.BoolNull(), userIDs: accessIDs(), clientAcc: types.BoolValue(true), clientIDs: accessIDs()},
			plan:       accessValues{id: types.StringUnknown(), userAccess: types.BoolUnknown(), userIDs: accessIDs(), clientAcc: types.BoolValue(true), clientIDs: accessIDs()},
			wantUser:   types.BoolValue(false),
			wantClient: types.BoolValue(true),
		},
		{
			name:        "false next to permissions is an error",
			config:      accessValues{id: types.StringUnknown(), userAccess: types.BoolNull(), userIDs: accessIDs(), clientAcc: types.BoolValue(false), clientIDs: accessIDs("perm-1")},
			plan:        accessValues{id: types.StringUnknown(), userAccess: types.BoolUnknown(), userIDs: accessIDs(), clientAcc: types.BoolValue(false), clientIDs: accessIDs("perm-1")},
			wantErrText: "Access is false but permissions are listed",
		},
		{
			name:        "neither kind is an error",
			config:      accessValues{id: types.StringUnknown(), userAccess: types.BoolNull(), userIDs: accessIDs(), clientAcc: types.BoolNull(), clientIDs: accessIDs()},
			plan:        accessValues{id: types.StringUnknown(), userAccess: types.BoolUnknown(), userIDs: accessIDs(), clientAcc: types.BoolUnknown(), clientIDs: accessIDs()},
			wantErrText: "No access granted",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := accessPlan(ctx, t, s, tc.plan)
			resp := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(ctx, resource.ModifyPlanRequest{
				Config: accessConfig(ctx, t, s, tc.config),
				Plan:   plan,
				State:  emptyAccessState(ctx, s),
			}, &resp)

			if tc.wantErrText != "" {
				require.True(t, resp.Diagnostics.HasError())
				assert.Contains(t, resp.Diagnostics.Errors()[0].Summary(), tc.wantErrText)
				return
			}
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			var user, clientAccess types.Bool
			require.False(t, resp.Plan.GetAttribute(ctx, path.Root("user_delegated_access"), &user).HasError())
			require.False(t, resp.Plan.GetAttribute(ctx, path.Root("client_access"), &clientAccess).HasError())
			assert.Equal(t, tc.wantUser, user)
			assert.Equal(t, tc.wantClient, clientAccess)
		})
	}
}

func TestAPIClientAccessResource_Create(t *testing.T) {
	ctx := context.Background()
	f := &fakeAccessServer{}
	r, s := newAccessResource(t, f)

	plan := accessPlan(ctx, t, s, accessValues{
		id: types.StringUnknown(), userAccess: types.BoolValue(true), userIDs: accessIDs("perm-1"),
		clientAcc: types.BoolValue(true), clientIDs: accessIDs("perm-1", "perm-2"),
	})
	resp := resource.CreateResponse{State: emptyAccessState(ctx, s)}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)

	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{"PUT /api/apis/api-1/clients/client-1"}, f.calls)
	var id types.String
	require.False(t, resp.State.GetAttribute(ctx, path.Root("id"), &id).HasError())
	assert.Equal(t, "api-1/client-1", id.ValueString())
}

func TestAPIClientAccessResource_CreateRefusesWhatPocketIDDoesNotApply(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name   string
		server *fakeAccessServer
		values accessValues
		want   string
	}{
		{
			name:   "a public client has no client access",
			server: &fakeAccessServer{public: true},
			values: accessValues{id: types.StringUnknown(), userAccess: types.BoolValue(false), userIDs: accessIDs(), clientAcc: types.BoolValue(true), clientIDs: accessIDs()},
			want:   "client_access is false, not true",
		},
		{
			name:   "a permission of another API is dropped",
			server: &fakeAccessServer{},
			values: accessValues{id: types.StringUnknown(), userAccess: types.BoolValue(false), userIDs: accessIDs(), clientAcc: types.BoolValue(true), clientIDs: accessIDs("perm-1", "other-api-perm")},
			want:   "client_permission_ids not applied (not permissions of the API, or a public client?): other-api-perm",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, s := newAccessResource(t, tc.server)
			resp := resource.CreateResponse{State: emptyAccessState(ctx, s)}
			r.Create(ctx, resource.CreateRequest{Plan: accessPlan(ctx, t, s, tc.values)}, &resp)

			require.True(t, resp.Diagnostics.HasError())
			assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), tc.want)
			assert.Contains(t, tc.server.calls, "DELETE /api/apis/api-1/clients/client-1", "the grant that was not asked for is removed")
			assert.Nil(t, tc.server.grant)
		})
	}
}

func TestAPIClientAccessResource_Read(t *testing.T) {
	ctx := context.Background()
	state := accessValues{id: types.StringValue("api-1/client-1"), userAccess: types.BoolValue(false), userIDs: accessIDs(), clientAcc: types.BoolValue(true), clientIDs: accessIDs("perm-1")}

	t.Run("reflects the grant", func(t *testing.T) {
		f := &fakeAccessServer{grant: &client.APIClientGrant{UserDelegatedAccess: true, ClientAccess: true, UserDelegatedPermissionIDs: []string{"perm-2"}, ClientPermissionIDs: []string{"perm-1", "perm-2"}}}
		r, s := newAccessResource(t, f)
		resp := resource.ReadResponse{State: accessState(ctx, t, s, state)}
		r.Read(ctx, resource.ReadRequest{State: accessState(ctx, t, s, state)}, &resp)

		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		var user types.Bool
		var userIDs, clientIDs types.Set
		require.False(t, resp.State.GetAttribute(ctx, path.Root("user_delegated_access"), &user).HasError())
		require.False(t, resp.State.GetAttribute(ctx, path.Root("user_delegated_permission_ids"), &userIDs).HasError())
		require.False(t, resp.State.GetAttribute(ctx, path.Root("client_permission_ids"), &clientIDs).HasError())
		assert.True(t, user.ValueBool())
		assert.True(t, userIDs.Equal(accessIDs("perm-2")))
		assert.True(t, clientIDs.Equal(accessIDs("perm-1", "perm-2")))
	})

	removed := map[string]*fakeAccessServer{
		"the grant is gone": {},
		"the client reaches the API only through the CIMD opt-in": {cimdOnly: true},
		"the client is gone": {clientGone: true},
	}
	for name, f := range removed {
		t.Run(name, func(t *testing.T) {
			r, s := newAccessResource(t, f)
			resp := resource.ReadResponse{State: accessState(ctx, t, s, state)}
			r.Read(ctx, resource.ReadRequest{State: accessState(ctx, t, s, state)}, &resp)

			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			assert.True(t, resp.State.Raw.IsNull(), "the resource is removed from the state")
		})
	}
}

func TestAPIClientAccessResource_Update(t *testing.T) {
	ctx := context.Background()
	f := &fakeAccessServer{}
	r, s := newAccessResource(t, f)

	before := accessValues{id: types.StringValue("api-1/client-1"), userAccess: types.BoolValue(false), userIDs: accessIDs(), clientAcc: types.BoolValue(true), clientIDs: accessIDs("perm-1")}
	after := accessValues{id: types.StringValue("api-1/client-1"), userAccess: types.BoolValue(false), userIDs: accessIDs(), clientAcc: types.BoolValue(true), clientIDs: accessIDs("perm-2")}
	resp := resource.UpdateResponse{State: accessState(ctx, t, s, before)}
	r.Update(ctx, resource.UpdateRequest{Plan: accessPlan(ctx, t, s, after), State: accessState(ctx, t, s, before)}, &resp)

	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{"perm-2"}, f.grant.ClientPermissionIDs)
}

func TestAPIClientAccessResource_Delete(t *testing.T) {
	ctx := context.Background()
	state := accessValues{id: types.StringValue("api-1/client-1"), userAccess: types.BoolValue(false), userIDs: accessIDs(), clientAcc: types.BoolValue(true), clientIDs: accessIDs("perm-1")}

	f := &fakeAccessServer{grant: &client.APIClientGrant{ClientAccess: true, ClientPermissionIDs: []string{"perm-1"}}}
	r, s := newAccessResource(t, f)
	resp := resource.DeleteResponse{State: accessState(ctx, t, s, state)}
	r.Delete(ctx, resource.DeleteRequest{State: accessState(ctx, t, s, state)}, &resp)

	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Nil(t, f.grant)
}

func TestAPIClientAccessResource_ImportState(t *testing.T) {
	ctx := context.Background()
	r, s := newAccessResource(t, &fakeAccessServer{})

	t.Run("api and client", func(t *testing.T) {
		resp := resource.ImportStateResponse{State: emptyAccessState(ctx, s)}
		r.(resource.ResourceWithImportState).ImportState(ctx, resource.ImportStateRequest{ID: "api-1/client-1"}, &resp)

		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		var id, apiID, clientID types.String
		require.False(t, resp.State.GetAttribute(ctx, path.Root("id"), &id).HasError())
		require.False(t, resp.State.GetAttribute(ctx, path.Root("api_id"), &apiID).HasError())
		require.False(t, resp.State.GetAttribute(ctx, path.Root("client_id"), &clientID).HasError())
		assert.Equal(t, "api-1/client-1", id.ValueString())
		assert.Equal(t, "api-1", apiID.ValueString())
		assert.Equal(t, "client-1", clientID.ValueString())
	})

	for _, bad := range []string{"", "api-1", "api-1/", "/client-1", "a/b/c"} {
		t.Run("invalid "+bad, func(t *testing.T) {
			resp := resource.ImportStateResponse{State: emptyAccessState(ctx, s)}
			r.(resource.ResourceWithImportState).ImportState(ctx, resource.ImportStateRequest{ID: bad}, &resp)
			require.True(t, resp.Diagnostics.HasError())
			assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "<api_id>/<client_id>")
		})
	}
}
