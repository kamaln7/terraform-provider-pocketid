package resources_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-timetypes/timetypes"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
	"github.com/Trozz/terraform-provider-pocketid/internal/resources"
)

const testSecretValue = "write-only-secret-0123456789"

func nullSecretState(t *testing.T, s schema.Schema) tfsdk.State {
	t.Helper()
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)}
}

func clientSecretSchema(t *testing.T) schema.Schema {
	t.Helper()

	resp := &resource.SchemaResponse{}
	resources.NewClientSecretResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	require.False(t, resp.Diagnostics.HasError(), "Schema returned diagnostics: %+v", resp.Diagnostics)
	return resp.Schema
}

// clientSecretValues builds the raw object for the schema from the given
// attribute values. Attributes that are not given are null.
func clientSecretValues(t *testing.T, s schema.Schema, values map[string]tftypes.Value) tftypes.Value {
	t.Helper()

	objType, ok := s.Type().TerraformType(context.Background()).(tftypes.Object)
	require.True(t, ok)

	all := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, typ := range objType.AttributeTypes {
		if v, ok := values[name]; ok {
			all[name] = v
		} else {
			all[name] = tftypes.NewValue(typ, nil)
		}
	}
	return tftypes.NewValue(objType, all)
}

func str(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

// configuredSecretResource returns the resource wired to a client that talks to handler.
func configuredSecretResource(t *testing.T, handler http.HandlerFunc) resource.Resource {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)

	r := resources.NewClientSecretResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, resp)
	require.False(t, resp.Diagnostics.HasError())
	return r
}

func TestClientSecretResource_Metadata(t *testing.T) {
	resp := &resource.MetadataResponse{}
	resources.NewClientSecretResource().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "pocketid"}, resp)
	assert.Equal(t, "pocketid_client_secret", resp.TypeName)
}

func TestClientSecretResource_Schema(t *testing.T) {
	s := clientSecretSchema(t)

	diags := s.ValidateImplementation(context.Background())
	require.False(t, diags.HasError(), "%+v", diags)

	attrs := s.Attributes
	assert.ElementsMatch(t,
		[]string{"id", "client_id", "secret_wo", "secret_wo_version", "value", "prefix", "created_at", "expires_at"},
		keys(attrs),
	)

	for _, name := range []string{"id", "prefix", "created_at"} {
		a := attrs[name]
		assert.True(t, a.IsComputed(), "%s should be computed", name)
		assert.False(t, a.IsOptional(), "%s should not be optional", name)
		assert.False(t, a.IsRequired(), "%s should not be required", name)
	}

	clientID := attrs["client_id"].(schema.StringAttribute)
	assert.True(t, clientID.Required)
	assert.Len(t, clientID.PlanModifiers, 1, "changing client_id should replace the secret")

	secretWO := attrs["secret_wo"].(schema.StringAttribute)
	assert.True(t, secretWO.Optional)
	assert.True(t, secretWO.WriteOnly)
	assert.True(t, secretWO.Sensitive)
	assert.False(t, secretWO.Computed)
	assert.Empty(t, secretWO.PlanModifiers, "a write-only attribute has no plan modifiers")

	version := attrs["secret_wo_version"].(schema.Int64Attribute)
	assert.True(t, version.Optional)
	assert.False(t, version.Computed)
	assert.Len(t, version.PlanModifiers, 1, "changing secret_wo_version should replace the secret")

	value := attrs["value"].(schema.StringAttribute)
	assert.True(t, value.Computed)
	assert.True(t, value.Sensitive)
	assert.False(t, value.Optional)

	expiresAt := attrs["expires_at"].(schema.StringAttribute)
	assert.True(t, expiresAt.Optional)
	assert.False(t, expiresAt.Computed)
	assert.IsType(t, timetypes.RFC3339Type{}, expiresAt.CustomType)
	assert.Len(t, expiresAt.PlanModifiers, 1, "changing expires_at should replace the secret")
}

func keys(m map[string]schema.Attribute) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestClientSecretResource_SecretWOValidation(t *testing.T) {
	s := clientSecretSchema(t)
	secretWO := s.Attributes["secret_wo"].(schema.StringAttribute)

	tests := []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{"valid", types.StringValue(testSecretValue), false},
		{"exactly 16 characters", types.StringValue("0123456789abcdef"), false},
		{"with punctuation and space", types.StringValue("pass word!#$%&'()*+,-./:;<=>?@[]^_`{|}~"), false},
		{"too short", types.StringValue("0123456789abcde"), true},
		{"not printable ASCII", types.StringValue("0123456789abcdef\n"), true},
		{"not ASCII", types.StringValue("0123456789abcdéf"), true},
		{"null", types.StringNull(), false},
		{"unknown", types.StringUnknown(), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &validator.StringResponse{}
			for _, v := range secretWO.Validators {
				v.ValidateString(context.Background(), validator.StringRequest{
					Path:        path.Root("secret_wo"),
					ConfigValue: tt.value,
				}, resp)
			}
			assert.Equal(t, tt.wantErr, resp.Diagnostics.HasError(), "%+v", resp.Diagnostics)
		})
	}
}

func TestClientSecretResource_ClientIDValidation(t *testing.T) {
	s := clientSecretSchema(t)
	clientID := s.Attributes["client_id"].(schema.StringAttribute)

	tests := []struct {
		value   types.String
		wantErr bool
	}{
		{types.StringValue("0b2f7a1c-4d4e-4f4e-9d37-5a3c1b2e9f10"), false},
		{types.StringValue("my.client_id-1"), false},
		{types.StringValue("https://client.example.com/metadata.json"), true},
		{types.StringValue("a/b"), true},
		{types.StringValue(""), true},
		{types.StringUnknown(), false},
	}

	for _, tt := range tests {
		t.Run(tt.value.String(), func(t *testing.T) {
			resp := &validator.StringResponse{}
			for _, v := range clientID.Validators {
				v.ValidateString(context.Background(), validator.StringRequest{
					Path:        path.Root("client_id"),
					ConfigValue: tt.value,
				}, resp)
			}
			assert.Equal(t, tt.wantErr, resp.Diagnostics.HasError(), "%+v", resp.Diagnostics)
		})
	}
}

func TestClientSecretResource_SecretWOVersionRequiresSecretWO(t *testing.T) {
	s := clientSecretSchema(t)
	version := s.Attributes["secret_wo_version"].(schema.Int64Attribute)

	tests := []struct {
		name    string
		values  map[string]tftypes.Value
		wantErr bool
	}{
		{
			"version without secret_wo",
			map[string]tftypes.Value{
				"client_id":         str("c1"),
				"secret_wo_version": tftypes.NewValue(tftypes.Number, 1),
			},
			true,
		},
		{
			"version with secret_wo",
			map[string]tftypes.Value{
				"client_id":         str("c1"),
				"secret_wo":         str(testSecretValue),
				"secret_wo_version": tftypes.NewValue(tftypes.Number, 1),
			},
			false,
		},
		{
			"secret_wo without version",
			map[string]tftypes.Value{
				"client_id": str("c1"),
				"secret_wo": str(testSecretValue),
			},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := tfsdk.Config{Schema: s, Raw: clientSecretValues(t, s, tt.values)}

			var configValue types.Int64
			require.False(t, config.GetAttribute(context.Background(), path.Root("secret_wo_version"), &configValue).HasError())

			resp := &validator.Int64Response{}
			for _, v := range version.Validators {
				v.ValidateInt64(context.Background(), validator.Int64Request{
					Path:           path.Root("secret_wo_version"),
					PathExpression: path.MatchRoot("secret_wo_version"),
					Config:         config,
					ConfigValue:    configValue,
				}, resp)
			}
			assert.Equal(t, tt.wantErr, resp.Diagnostics.HasError(), "%+v", resp.Diagnostics)
		})
	}
}

func TestParseClientSecretImportID(t *testing.T) {
	r := resources.NewClientSecretResource().(resource.ResourceWithImportState)
	s := clientSecretSchema(t)

	tests := []struct {
		name         string
		id           string
		wantClientID string
		wantSecretID string
		wantErr      bool
	}{
		{"valid", "my-client/7f1f5c0e-2a6b-4f38-8d0c-9a1d6f4a2b11", "my-client", "7f1f5c0e-2a6b-4f38-8d0c-9a1d6f4a2b11", false},
		{"client id with dots and underscores", "my.client_1/s1", "my.client_1", "s1", false},
		{"no separator", "my-client", "", "", true},
		{"empty", "", "", "", true},
		{"missing secret id", "my-client/", "", "", true},
		{"missing client id", "/s1", "", "", true},
		{"too many parts", "my-client/s1/extra", "", "", true},
		{"url as client id", "https://client.example.com/s1", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &resource.ImportStateResponse{
				State: tfsdk.State{Schema: s, Raw: clientSecretValues(t, s, nil)},
			}
			r.ImportState(context.Background(), resource.ImportStateRequest{ID: tt.id}, resp)

			if tt.wantErr {
				assert.True(t, resp.Diagnostics.HasError())
				return
			}
			require.False(t, resp.Diagnostics.HasError(), "%+v", resp.Diagnostics)

			var clientID, secretID types.String
			require.False(t, resp.State.GetAttribute(context.Background(), path.Root("client_id"), &clientID).HasError())
			require.False(t, resp.State.GetAttribute(context.Background(), path.Root("id"), &secretID).HasError())
			assert.Equal(t, tt.wantClientID, clientID.ValueString())
			assert.Equal(t, tt.wantSecretID, secretID.ValueString())
		})
	}
}

type createdSecretResponse struct {
	ID        string  `json:"id"`
	Prefix    string  `json:"prefix"`
	CreatedAt string  `json:"createdAt"`
	ExpiresAt *string `json:"expiresAt"`
	IsActive  bool    `json:"isActive"`
	Secret    string  `json:"secret"`
}

func TestClientSecretResource_Create(t *testing.T) {
	s := clientSecretSchema(t)

	tests := []struct {
		name       string
		plan       map[string]tftypes.Value
		config     map[string]tftypes.Value
		wantBody   map[string]any
		wantValue  types.String
		wantExpiry string
	}{
		{
			name:      "generated",
			plan:      map[string]tftypes.Value{"client_id": str("c1")},
			config:    map[string]tftypes.Value{"client_id": str("c1")},
			wantBody:  map[string]any{},
			wantValue: types.StringValue("generated-by-pocket-id"),
		},
		{
			name: "write-only",
			plan: map[string]tftypes.Value{"client_id": str("c1")},
			config: map[string]tftypes.Value{
				"client_id": str("c1"),
				"secret_wo": str(testSecretValue),
			},
			wantBody:  map[string]any{"secret": testSecretValue},
			wantValue: types.StringNull(),
		},
		{
			name: "write-only with version and expiry",
			plan: map[string]tftypes.Value{
				"client_id":         str("c1"),
				"secret_wo_version": tftypes.NewValue(tftypes.Number, 3),
				"expires_at":        str("2031-01-01T00:00:00Z"),
			},
			config: map[string]tftypes.Value{
				"client_id":         str("c1"),
				"secret_wo":         str(testSecretValue),
				"secret_wo_version": tftypes.NewValue(tftypes.Number, 3),
				"expires_at":        str("2031-01-01T00:00:00Z"),
			},
			wantBody:   map[string]any{"secret": testSecretValue, "expiresAt": "2031-01-01T00:00:00Z"},
			wantValue:  types.StringNull(),
			wantExpiry: "2031-01-01T00:00:00Z",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := configuredSecretResource(t, func(w http.ResponseWriter, req *http.Request) {
				assert.Equal(t, http.MethodPost, req.Method)
				assert.Equal(t, "/api/oidc/clients/c1/secrets", req.URL.Path)

				var body map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				assert.Equal(t, tt.wantBody, body)

				secret := "generated-by-pocket-id"
				if v, ok := body["secret"].(string); ok {
					secret = v
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				require.NoError(t, json.NewEncoder(w).Encode(createdSecretResponse{
					ID:        "secret-1",
					Prefix:    secret[:4],
					CreatedAt: "2030-01-01T00:00:00Z",
					IsActive:  true,
					Secret:    secret,
				}))
			})

			planValues := tt.plan
			planValues["id"] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
			planValues["value"] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
			planValues["prefix"] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
			planValues["created_at"] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)

			req := resource.CreateRequest{
				Plan:   tfsdk.Plan{Schema: s, Raw: clientSecretValues(t, s, planValues)},
				Config: tfsdk.Config{Schema: s, Raw: clientSecretValues(t, s, tt.config)},
			}
			resp := &resource.CreateResponse{State: nullSecretState(t, s)}
			r.Create(context.Background(), req, resp)
			require.False(t, resp.Diagnostics.HasError(), "%+v", resp.Diagnostics)

			var got struct {
				ID        types.String      `tfsdk:"id"`
				ClientID  types.String      `tfsdk:"client_id"`
				SecretWO  types.String      `tfsdk:"secret_wo"`
				Version   types.Int64       `tfsdk:"secret_wo_version"`
				Value     types.String      `tfsdk:"value"`
				Prefix    types.String      `tfsdk:"prefix"`
				CreatedAt types.String      `tfsdk:"created_at"`
				ExpiresAt timetypes.RFC3339 `tfsdk:"expires_at"`
			}
			require.False(t, resp.State.Get(context.Background(), &got).HasError())

			assert.Equal(t, "secret-1", got.ID.ValueString())
			assert.Equal(t, "c1", got.ClientID.ValueString())
			assert.True(t, got.SecretWO.IsNull(), "the write-only value must not reach the state")
			assert.Equal(t, tt.wantValue, got.Value)
			assert.False(t, got.Prefix.IsNull())
			assert.Equal(t, "2030-01-01T00:00:00Z", got.CreatedAt.ValueString())
			assert.Equal(t, tt.wantExpiry, got.ExpiresAt.ValueString())

			// The write-only value must be nowhere in the state.
			if tt.wantValue.IsNull() {
				assert.NotContains(t, resp.State.Raw.String(), testSecretValue)
			}
		})
	}
}

func TestClientSecretResource_Create_Error(t *testing.T) {
	s := clientSecretSchema(t)
	r := configuredSecretResource(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"A client cannot have more than 20 secrets"}`)
	})

	req := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: clientSecretValues(t, s, map[string]tftypes.Value{
			"client_id":  str("c1"),
			"id":         tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			"value":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			"prefix":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			"created_at": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		})},
		Config: tfsdk.Config{Schema: s, Raw: clientSecretValues(t, s, map[string]tftypes.Value{"client_id": str("c1")})},
	}
	resp := &resource.CreateResponse{State: nullSecretState(t, s)}
	r.Create(context.Background(), req, resp)

	require.True(t, resp.Diagnostics.HasError())
	assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), "A client cannot have more than 20 secrets")
	assert.True(t, resp.State.Raw.IsNull(), "a failed create must leave no state")
}

func existingSecretState(t *testing.T, s schema.Schema) tfsdk.State {
	t.Helper()
	return tfsdk.State{Schema: s, Raw: clientSecretValues(t, s, map[string]tftypes.Value{
		"id":         str("secret-1"),
		"client_id":  str("c1"),
		"value":      str("generated-by-pocket-id"),
		"prefix":     str("gene"),
		"created_at": str("2030-01-01T00:00:00Z"),
	})}
}

func TestClientSecretResource_Read(t *testing.T) {
	s := clientSecretSchema(t)
	expiresAt := "2031-01-01T00:00:00Z"

	tests := []struct {
		name        string
		status      int
		body        string
		wantRemoved bool
		wantErr     bool
		wantPrefix  string
		wantExpiry  types.String
	}{
		{
			name:       "still there",
			status:     http.StatusOK,
			body:       `[{"id":"other","prefix":"zzzz","createdAt":"2030-01-01T00:00:00Z","expiresAt":null,"isActive":true},{"id":"secret-1","prefix":"gene","createdAt":"2030-01-01T00:00:00Z","expiresAt":null,"isActive":true}]`,
			wantPrefix: "gene",
			wantExpiry: types.StringNull(),
		},
		{
			name:       "expiry set elsewhere",
			status:     http.StatusOK,
			body:       `[{"id":"secret-1","prefix":"gene","createdAt":"2030-01-01T00:00:00Z","expiresAt":"` + expiresAt + `","isActive":true}]`,
			wantPrefix: "gene",
			wantExpiry: types.StringValue(expiresAt),
		},
		{
			name:        "secret deleted",
			status:      http.StatusOK,
			body:        `[{"id":"other","prefix":"zzzz","createdAt":"2030-01-01T00:00:00Z","expiresAt":null,"isActive":true}]`,
			wantRemoved: true,
		},
		{
			name:        "no secrets left",
			status:      http.StatusOK,
			body:        `[]`,
			wantRemoved: true,
		},
		{
			name:        "client deleted",
			status:      http.StatusNotFound,
			body:        `{"error":"OIDC client not found"}`,
			wantRemoved: true,
		},
		{
			name:    "server error",
			status:  http.StatusForbidden,
			body:    `{"error":"nope"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := configuredSecretResource(t, func(w http.ResponseWriter, req *http.Request) {
				assert.Equal(t, http.MethodGet, req.Method)
				assert.Equal(t, "/api/oidc/clients/c1/secrets", req.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprint(w, tt.body)
			})

			resp := &resource.ReadResponse{State: existingSecretState(t, s)}
			r.Read(context.Background(), resource.ReadRequest{State: existingSecretState(t, s)}, resp)

			if tt.wantErr {
				assert.True(t, resp.Diagnostics.HasError())
				return
			}
			require.False(t, resp.Diagnostics.HasError(), "%+v", resp.Diagnostics)
			if tt.wantRemoved {
				assert.True(t, resp.State.Raw.IsNull(), "the resource should be removed from the state")
				return
			}

			var prefix, value types.String
			var expiresAt timetypes.RFC3339
			require.False(t, resp.State.GetAttribute(context.Background(), path.Root("prefix"), &prefix).HasError())
			require.False(t, resp.State.GetAttribute(context.Background(), path.Root("value"), &value).HasError())
			require.False(t, resp.State.GetAttribute(context.Background(), path.Root("expires_at"), &expiresAt).HasError())
			assert.Equal(t, tt.wantPrefix, prefix.ValueString())
			assert.Equal(t, "generated-by-pocket-id", value.ValueString(), "Read must keep the value it cannot retrieve")
			assert.Equal(t, tt.wantExpiry.IsNull(), expiresAt.IsNull())
			assert.Equal(t, tt.wantExpiry.ValueString(), expiresAt.ValueString())
		})
	}
}

func TestClientSecretResource_Delete(t *testing.T) {
	s := clientSecretSchema(t)

	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"deleted", http.StatusNoContent, false},
		{"already gone", http.StatusNotFound, false},
		{"server error", http.StatusForbidden, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			r := configuredSecretResource(t, func(w http.ResponseWriter, req *http.Request) {
				called = true
				assert.Equal(t, http.MethodDelete, req.Method)
				assert.Equal(t, "/api/oidc/clients/c1/secrets/secret-1", req.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				if tt.status >= 400 {
					_, _ = fmt.Fprint(w, `{"error":"x"}`)
				}
			})

			resp := &resource.DeleteResponse{}
			r.Delete(context.Background(), resource.DeleteRequest{State: existingSecretState(t, s)}, resp)

			assert.True(t, called)
			assert.Equal(t, tt.wantErr, resp.Diagnostics.HasError(), "%+v", resp.Diagnostics)
		})
	}
}

func TestClientSecretResource_UpdateIsRejected(t *testing.T) {
	r := resources.NewClientSecretResource()
	resp := &resource.UpdateResponse{}
	r.Update(context.Background(), resource.UpdateRequest{}, resp)
	assert.True(t, resp.Diagnostics.HasError())
}
