package resources_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
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

// fakeAPIServer is a minimal Pocket-ID APIs API: it keeps one API with its
// permissions and records every request.
type fakeAPIServer struct {
	mu          sync.Mutex
	calls       []string
	api         *client.API
	failPermsOn bool
	nextPermID  int
}

func (f *fakeAPIServer) record(r *http.Request) {
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
}

func (f *fakeAPIServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.record(r)
		w.Header().Set("Content-Type", "application/json")
		write := func(status int) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(f.api)
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apis":
			var req client.APICreateRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			f.api = &client.API{ID: "api-1", Name: req.Name, Resource: req.Resource, Permissions: []client.APIPermission{}}
			write(http.StatusCreated)
		case f.api == nil:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"API not found"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/apis/api-1":
			write(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/api/apis/api-1":
			var req client.APIUpdateRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			f.api.Name = req.Name
			write(http.StatusOK)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/apis/api-1":
			f.api = nil
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && r.URL.Path == "/api/apis/api-1/permissions":
			if f.failPermsOn {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"permissions[0].key is reserved by Pocket ID"}`))
				return
			}
			var req client.APIPermissionsUpdateRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			existing := map[string]client.APIPermission{}
			for _, p := range f.api.Permissions {
				existing[p.Key] = p
			}
			var next []client.APIPermission
			for _, in := range req.Permissions {
				p, ok := existing[in.Key]
				if !ok {
					f.nextPermID++
					p = client.APIPermission{ID: "perm-" + string(rune('0'+f.nextPermID)), Key: in.Key}
				}
				p.Name, p.Description = in.Name, in.Description
				next = append(next, p)
			}
			f.api.Permissions = next
			write(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/api/apis/api-1/cimd-access":
			var req client.APICIMDAccessRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			f.api.AllowCIMDClients = req.Enabled
			for i := range f.api.Permissions {
				f.api.Permissions[i].AllowedForCIMDClients = false
				for _, id := range req.PermissionIDs {
					if id == f.api.Permissions[i].ID {
						f.api.Permissions[i].AllowedForCIMDClients = true
					}
				}
			}
			write(http.StatusOK)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func newAPIResource(t *testing.T, f *fakeAPIServer) (resource.Resource, resource.SchemaResponse) {
	t.Helper()
	ctx := context.Background()
	r := resources.NewAPIResource()
	r.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{
		ProviderData: createMockServer(t, f.handler(t)),
	}, &resource.ConfigureResponse{})

	var s resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &s)
	require.False(t, s.Diagnostics.HasError(), "%v", s.Diagnostics)
	return r, s
}

var apiTestPermissionType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"id":                       types.StringType,
	"name":                     types.StringType,
	"description":              types.StringType,
	"allowed_for_cimd_clients": types.BoolType,
}}

func apiTestPermission(id, name string, description *string, cimd bool) attr.Value {
	desc := types.StringNull()
	if description != nil {
		desc = types.StringValue(*description)
	}
	idValue := types.StringUnknown()
	if id != "" {
		idValue = types.StringValue(id)
	}
	return types.ObjectValueMust(apiTestPermissionType.AttrTypes, map[string]attr.Value{
		"id":                       idValue,
		"name":                     types.StringValue(name),
		"description":              desc,
		"allowed_for_cimd_clients": types.BoolValue(cimd),
	})
}

func apiTestModel(ctx context.Context, t *testing.T, s resource.SchemaResponse, plan bool, id, name string, cimd bool, permissions map[string]attr.Value) (tfsdk.Plan, tfsdk.State) {
	t.Helper()
	raw := tftypes.NewValue(s.Schema.Type().TerraformType(ctx), nil)
	p, st := tfsdk.Plan{Schema: s.Schema, Raw: raw}, tfsdk.State{Schema: s.Schema, Raw: raw}

	idValue := types.StringValue(id)
	if id == "" {
		idValue = types.StringUnknown()
	}
	set := func(attribute string, value attr.Value) {
		if plan {
			require.False(t, p.SetAttribute(ctx, path.Root(attribute), value).HasError())
		} else {
			require.False(t, st.SetAttribute(ctx, path.Root(attribute), value).HasError())
		}
	}
	set("id", idValue)
	set("name", types.StringValue(name))
	set("resource", types.StringValue("https://api.example.com"))
	set("allow_cimd_clients", types.BoolValue(cimd))
	set("permissions", types.MapValueMust(apiTestPermissionType, permissions))
	return p, st
}

func permissionKeys(t *testing.T, state tfsdk.State, ctx context.Context) []string {
	t.Helper()
	var permissions map[string]types.Object
	require.False(t, state.GetAttribute(ctx, path.Root("permissions"), &permissions).HasError())
	keys := make([]string, 0, len(permissions))
	for key := range permissions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestAPIResource_Schema(t *testing.T) {
	_, s := newAPIResource(t, &fakeAPIServer{})
	attrs := s.Schema.Attributes

	for _, name := range []string{"id", "name", "resource", "allow_cimd_clients", "permissions"} {
		assert.Contains(t, attrs, name)
	}
	assert.True(t, attrs["name"].IsRequired())
	assert.True(t, attrs["resource"].IsRequired())
	assert.True(t, attrs["id"].IsComputed())
	// Unset means none, as an API is declared with its permissions.
	assert.True(t, attrs["permissions"].IsOptional())
	assert.True(t, attrs["permissions"].IsComputed())
	assert.True(t, attrs["allow_cimd_clients"].IsOptional())
}

func TestAPIResource_CreateSetsPermissionsAndCIMDAccess(t *testing.T) {
	ctx := context.Background()
	f := &fakeAPIServer{}
	r, s := newAPIResource(t, f)

	description := "Read the orders"
	plan, _ := apiTestModel(ctx, t, s, true, "", "Orders", true, map[string]attr.Value{
		"read:orders":  apiTestPermission("", "Read orders", &description, true),
		"write:orders": apiTestPermission("", "Write orders", nil, false),
	})
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)

	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{"POST /api/apis", "PUT /api/apis/api-1/permissions", "PUT /api/apis/api-1/cimd-access"}, f.calls)
	assert.True(t, f.api.AllowCIMDClients)
	assert.Equal(t, []string{"read:orders", "write:orders"}, permissionKeys(t, resp.State, ctx))

	var read types.Object
	require.False(t, resp.State.GetAttribute(ctx, path.Root("permissions").AtMapKey("read:orders"), &read).HasError())
	assert.Equal(t, types.StringValue("perm-1"), read.Attributes()["id"], "the ID comes from Pocket ID")
	assert.Equal(t, types.BoolValue(true), read.Attributes()["allowed_for_cimd_clients"])
}

func TestAPIResource_CreateWithoutPermissionsCallsOnlyCreate(t *testing.T) {
	ctx := context.Background()
	f := &fakeAPIServer{}
	r, s := newAPIResource(t, f)

	plan, _ := apiTestModel(ctx, t, s, true, "", "Orders", false, map[string]attr.Value{})
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)

	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{"POST /api/apis"}, f.calls)
	assert.Empty(t, permissionKeys(t, resp.State, ctx))
}

func TestAPIResource_CreateFailureDeletesTheAPI(t *testing.T) {
	ctx := context.Background()
	f := &fakeAPIServer{failPermsOn: true}
	r, s := newAPIResource(t, f)

	plan, _ := apiTestModel(ctx, t, s, true, "", "Orders", false, map[string]attr.Value{
		"openid": apiTestPermission("", "OpenID", nil, false),
	})
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)

	require.True(t, resp.Diagnostics.HasError())
	assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "reserved by Pocket ID")
	assert.Contains(t, f.calls, "DELETE /api/apis/api-1", "the half-created API is removed")
	assert.Nil(t, f.api)
}

func TestAPIResource_UpdateCallsOnlyWhatChanged(t *testing.T) {
	ctx := context.Background()
	description := "Read"
	f := &fakeAPIServer{api: &client.API{
		ID: "api-1", Name: "Orders", Resource: "https://api.example.com",
		Permissions: []client.APIPermission{{ID: "perm-1", Key: "read:orders", Name: "Read orders", Description: &description}},
	}}
	r, s := newAPIResource(t, f)

	existing := map[string]attr.Value{"read:orders": apiTestPermission("perm-1", "Read orders", &description, false)}
	_, state := apiTestModel(ctx, t, s, false, "api-1", "Orders", false, existing)

	// Only the name changes.
	plan, _ := apiTestModel(ctx, t, s, true, "api-1", "Renamed", false, existing)
	resp := resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: state}, &resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{"GET /api/apis/api-1", "PUT /api/apis/api-1"}, f.calls)

	// A permission is added: the whole set is sent, the kept key keeps its ID.
	f.calls = nil
	added := map[string]attr.Value{
		"read:orders":  apiTestPermission("perm-1", "Read orders", &description, false),
		"write:orders": apiTestPermission("", "Write orders", nil, false),
	}
	plan, _ = apiTestModel(ctx, t, s, true, "api-1", "Renamed", false, added)
	resp = resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: state}, &resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{"GET /api/apis/api-1", "PUT /api/apis/api-1/permissions"}, f.calls)
	assert.Equal(t, []string{"read:orders", "write:orders"}, permissionKeys(t, resp.State, ctx))
	var read types.Object
	require.False(t, resp.State.GetAttribute(ctx, path.Root("permissions").AtMapKey("read:orders"), &read).HasError())
	assert.Equal(t, types.StringValue("perm-1"), read.Attributes()["id"])

	// Removing the last permissions sends an empty list, not nothing.
	f.calls = nil
	plan, _ = apiTestModel(ctx, t, s, true, "api-1", "Renamed", false, map[string]attr.Value{})
	resp = resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: state}, &resp)
	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.Equal(t, []string{"GET /api/apis/api-1", "PUT /api/apis/api-1/permissions"}, f.calls)
	assert.Empty(t, f.api.Permissions)
}

func TestAPIResource_ReadRemovesAnAPIThatIsGone(t *testing.T) {
	ctx := context.Background()
	f := &fakeAPIServer{} // no API: every request is a 404
	r, s := newAPIResource(t, f)

	_, state := apiTestModel(ctx, t, s, false, "api-1", "Orders", false, map[string]attr.Value{})
	resp := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &resp)

	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	assert.True(t, resp.State.Raw.IsNull(), "the resource is removed from the state")
}

func TestAPIResource_ReadReflectsTheAPI(t *testing.T) {
	ctx := context.Background()
	description := "Read"
	f := &fakeAPIServer{api: &client.API{
		ID: "api-1", Name: "Changed in the UI", Resource: "https://api.example.com", AllowCIMDClients: true,
		Permissions: []client.APIPermission{
			{ID: "perm-1", Key: "read:orders", Name: "Read orders", Description: &description, AllowedForCIMDClients: true},
			{ID: "perm-2", Key: "added:in:ui", Name: "Added"},
		},
	}}
	r, s := newAPIResource(t, f)

	_, state := apiTestModel(ctx, t, s, false, "api-1", "Orders", false, map[string]attr.Value{})
	resp := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &resp)

	require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
	var name types.String
	var cimd types.Bool
	require.False(t, resp.State.GetAttribute(ctx, path.Root("name"), &name).HasError())
	require.False(t, resp.State.GetAttribute(ctx, path.Root("allow_cimd_clients"), &cimd).HasError())
	assert.Equal(t, "Changed in the UI", name.ValueString())
	assert.True(t, cimd.ValueBool())
	assert.Equal(t, []string{"added:in:ui", "read:orders"}, permissionKeys(t, resp.State, ctx))
}

func TestAPIResource_DeleteIgnoresAnAPIThatIsGone(t *testing.T) {
	ctx := context.Background()
	f := &fakeAPIServer{}
	r, s := newAPIResource(t, f)

	_, state := apiTestModel(ctx, t, s, false, "api-1", "Orders", false, map[string]attr.Value{})
	resp := resource.DeleteResponse{State: state}
	r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)
	assert.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
}
