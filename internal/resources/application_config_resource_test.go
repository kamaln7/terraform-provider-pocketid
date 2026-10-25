package resources_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
	"github.com/Trozz/terraform-provider-pocketid/internal/resources"
)

func TestNewApplicationConfigResource(t *testing.T) {
	r := resources.NewApplicationConfigResource()
	assert.NotNil(t, r)

	_, ok := r.(resource.ResourceWithConfigure)
	assert.True(t, ok, "should implement ResourceWithConfigure")

	_, ok = r.(resource.ResourceWithImportState)
	assert.True(t, ok, "should implement ResourceWithImportState")
}

func TestApplicationConfigResource_Metadata(t *testing.T) {
	ctx := context.Background()
	r := resources.NewApplicationConfigResource()

	req := resource.MetadataRequest{ProviderTypeName: "pocketid"}
	resp := &resource.MetadataResponse{}

	r.Metadata(ctx, req, resp)

	assert.Equal(t, "pocketid_application_config", resp.TypeName)
}

func TestApplicationConfigResource_Schema(t *testing.T) {
	ctx := context.Background()
	r := resources.NewApplicationConfigResource()

	req := resource.SchemaRequest{}
	resp := &resource.SchemaResponse{}

	r.Schema(ctx, req, resp)

	require.False(t, resp.Diagnostics.HasError())
	assert.NotEmpty(t, resp.Schema.Description)

	// id is computed only.
	idAttr, ok := resp.Schema.Attributes["id"].(schema.StringAttribute)
	require.True(t, ok)
	assert.True(t, idAttr.Computed)
	assert.False(t, idAttr.Optional)

	// Writable attributes are optional + computed.
	for _, name := range []string{"app_name", "session_duration", "ldap_enabled"} {
		attr, ok := resp.Schema.Attributes[name].(schema.StringAttribute)
		require.True(t, ok, "attribute %s should exist", name)
		assert.True(t, attr.Optional, "attribute %s should be optional", name)
		assert.True(t, attr.Computed, "attribute %s should be computed", name)
	}

	// Secrets are marked sensitive.
	for _, name := range []string{"smtp_password", "ldap_bind_password"} {
		attr, ok := resp.Schema.Attributes[name].(schema.StringAttribute)
		require.True(t, ok, "attribute %s should exist", name)
		assert.True(t, attr.Sensitive, "attribute %s should be sensitive", name)
	}

	// Images are optional files, not computed.
	for _, name := range []string{"logo", "dark_logo", "email_logo", "background_image", "favicon", "default_profile_picture"} {
		attr, ok := resp.Schema.Attributes[name].(schema.SingleNestedAttribute)
		require.True(t, ok, "attribute %s should be a single nested attribute", name)
		assert.True(t, attr.Optional, "attribute %s should be optional", name)
		assert.False(t, attr.Computed, "attribute %s should not be computed", name)
		assert.True(t, attr.Attributes["source"].IsRequired(), "%s.source should be required", name)
		assert.True(t, attr.Attributes["sha256"].IsComputed(), "%s.sha256 should be computed", name)
	}
}

func TestApplicationConfigResource_Configure(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name          string
		providerData  interface{}
		expectError   bool
		errorContains string
	}{
		{name: "valid_client", providerData: &client.Client{}, expectError: false},
		{name: "nil_provider_data", providerData: nil, expectError: false},
		{name: "invalid_type", providerData: "invalid", expectError: true, errorContains: "Expected *client.Client"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := resources.NewApplicationConfigResource()
			configurable, ok := r.(resource.ResourceWithConfigure)
			require.True(t, ok)

			req := resource.ConfigureRequest{ProviderData: tc.providerData}
			resp := &resource.ConfigureResponse{}

			configurable.Configure(ctx, req, resp)

			if tc.expectError {
				assert.True(t, resp.Diagnostics.HasError())
				assert.Contains(t, resp.Diagnostics.Errors()[0].Detail(), tc.errorContains)
			} else {
				assert.False(t, resp.Diagnostics.HasError())
			}
		})
	}
}

func TestApplicationConfigResource_Schema219Settings(t *testing.T) {
	r := resources.NewApplicationConfigResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	require.False(t, resp.Diagnostics.HasError())

	// The settings Pocket ID 2.19 renamed or added are writable and inherit the
	// server's value when unset, like the others.
	for _, name := range []string{"email_login_notification_mode", "email_verification", "auto_create_oidc_client_secret"} {
		attr, ok := resp.Schema.Attributes[name].(schema.StringAttribute)
		require.True(t, ok, "attribute %s should exist", name)
		assert.True(t, attr.Optional, "attribute %s should be optional", name)
		assert.True(t, attr.Computed, "attribute %s should be computed", name)
	}
	for _, name := range []string{"email_login_notification_enabled", "email_verification_enabled"} {
		assert.NotContains(t, resp.Schema.Attributes, name)
	}
}

// fakeConfigServer serves the application configuration and keeps what the
// last PUT sent.
type fakeConfigServer struct {
	current  map[string]string
	received map[string]string
}

func (f *fakeConfigServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/application-configuration/all":
		case r.Method == http.MethodPut && r.URL.Path == "/api/application-configuration":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&f.received))
			f.current = f.received
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		variables := make([]client.AppConfigVariable, 0, len(f.current))
		for key, value := range f.current {
			variables = append(variables, client.AppConfigVariable{Key: key, Type: "string", Value: value})
		}
		_ = json.NewEncoder(w).Encode(variables)
	}
}

// The update endpoint of Pocket ID 2.19 requires emailLoginNotificationMode and
// emailVerification, and resets what it is not sent.
func TestApplicationConfigResource_Create219Settings(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		// set is the configured attributes, by name.
		set  map[string]string
		want map[string]string
	}{
		{
			name: "unset settings inherit the server's",
			set:  map[string]string{"app_name": "Renamed"},
			want: map[string]string{
				"appName":                    "Renamed",
				"emailLoginNotificationMode": "ipAndUserAgent",
				"emailVerification":          "optional",
				"autoCreateOidcClientSecret": "false",
			},
		},
		{
			name: "configured settings are sent",
			set: map[string]string{
				"email_login_notification_mode":  "always",
				"email_verification":             "disabled",
				"auto_create_oidc_client_secret": "true",
			},
			want: map[string]string{
				"emailLoginNotificationMode": "always",
				"emailVerification":          "disabled",
				"autoCreateOidcClientSecret": "true",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeConfigServer{current: map[string]string{
				"appName":                    "Pocket ID",
				"emailLoginNotificationMode": "ipAndUserAgent",
				"emailVerification":          "optional",
				"autoCreateOidcClientSecret": "false",
			}}
			r := resources.NewApplicationConfigResource()
			r.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{
				ProviderData: createMockServer(t, f.handler(t)),
			}, &resource.ConfigureResponse{})

			var s resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &s)
			raw := tftypes.NewValue(s.Schema.Type().TerraformType(ctx), nil)
			plan := tfsdk.Plan{Schema: s.Schema, Raw: raw}
			for name, value := range tc.set {
				require.False(t, plan.SetAttribute(ctx, path.Root(name), types.StringValue(value)).HasError())
			}

			resp := resource.CreateResponse{State: tfsdk.State{Schema: s.Schema, Raw: raw}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

			for key, want := range tc.want {
				assert.Equal(t, want, f.received[key], key)
			}
			assert.NotContains(t, f.received, "emailLoginNotificationEnabled")
			assert.NotContains(t, f.received, "emailVerificationEnabled")

			// The state shows what Pocket ID applied.
			var mode, verification, auto types.String
			require.False(t, resp.State.GetAttribute(ctx, path.Root("email_login_notification_mode"), &mode).HasError())
			require.False(t, resp.State.GetAttribute(ctx, path.Root("email_verification"), &verification).HasError())
			require.False(t, resp.State.GetAttribute(ctx, path.Root("auto_create_oidc_client_secret"), &auto).HasError())
			assert.Equal(t, f.received["emailLoginNotificationMode"], mode.ValueString())
			assert.Equal(t, f.received["emailVerification"], verification.ValueString())
			assert.Equal(t, f.received["autoCreateOidcClientSecret"], auto.ValueString())
		})
	}
}
