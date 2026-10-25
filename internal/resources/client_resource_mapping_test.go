package resources

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

// testJWK is an Ed25519 public key as Pocket ID returns it: keys sorted, with
// the key ID the server requires.
const testJWK = `{"alg":"EdDSA","crv":"Ed25519","kid":"test-key","kty":"OKP","use":"sig","x":"yvYSrUQ7W-ZdbgGplSlqbqdVj8nDC074evhHLOUiKJM"}`

func TestBuildCreateRequestFromPlan(t *testing.T) {
	ctx := context.Background()

	cb, diags := types.ListValueFrom(ctx, types.StringType, []string{"https://example.com/callback"})
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}

	launch := "https://example.com/launch"

	keys, diags := types.ListValueFrom(ctx, jsontypes.NormalizedType{}, []string{testJWK})
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}

	fedList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: federatedIdentityAttrTypes}, []clientFederatedIdentityModel{
		{
			Issuer:           types.StringValue("https://issuer.example.com"),
			Subject:          types.StringValue("subject-1"),
			Audience:         types.StringValue("audience-1"),
			JWKS:             types.StringNull(),
			PublicKeys:       keys,
			ReplayProtection: types.BoolValue(true),
		},
	})
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}

	plan := &clientResourceModel{
		Name:                     types.StringValue("Test Client"),
		CallbackURLs:             cb,
		IsPublic:                 types.BoolValue(false),
		PkceEnabled:              types.BoolValue(true),
		RequiresReauthentication: types.BoolValue(true),
		SkipConsent:              types.BoolValue(true),
		LaunchURL:                types.StringValue(launch),
		FederatedIdentities:      fedList,
	}

	req := buildCreateRequestFromPlan(ctx, plan)

	if req == nil {
		t.Fatal("expected request, got nil")
	}
	assert.Equal(t, "Test Client", req.Name)
	assert.Equal(t, []string{"https://example.com/callback"}, req.CallbackURLs)
	assert.True(t, req.RequiresReauthentication)
	assert.True(t, req.SkipConsent)
	if assert.NotNil(t, req.LaunchURL) {
		assert.Equal(t, launch, *req.LaunchURL)
	}
	if assert.Len(t, req.Credentials.FederatedIdentities, 1) {
		assert.Equal(t, "https://issuer.example.com", req.Credentials.FederatedIdentities[0].Issuer)
		assert.Equal(t, "subject-1", req.Credentials.FederatedIdentities[0].Subject)
		assert.Equal(t, "audience-1", req.Credentials.FederatedIdentities[0].Audience)
		assert.Equal(t, "", req.Credentials.FederatedIdentities[0].JWKS)
		assert.True(t, req.Credentials.FederatedIdentities[0].ReplayProtection)
		if assert.Len(t, req.Credentials.FederatedIdentities[0].PublicKeys, 1) {
			assert.JSONEq(t, testJWK, string(req.Credentials.FederatedIdentities[0].PublicKeys[0]))
		}
	}
}

func TestMapAPIClientToModel(t *testing.T) {
	ctx := context.Background()

	api := &client.OIDCClient{
		ID:                       "client-1",
		Name:                     "API Client",
		CallbackURLs:             []string{"https://example.com/callback"},
		LogoutCallbackURLs:       []string{"https://example.com/logout"},
		IsPublic:                 false,
		PkceEnabled:              true,
		HasLogo:                  false,
		HasDarkLogo:              true,
		RequiresReauthentication: true,
		SkipConsent:              true,
		LaunchURL:                "https://example.com/launch",
		AllowedUserGroups:        []client.UserGroup{{ID: "g1"}},
		Credentials: client.OIDCClientCredentials{
			FederatedIdentities: []client.OIDCClientFederatedIdentity{
				{
					Issuer:           "https://issuer.example.com",
					Subject:          "subject-1",
					PublicKeys:       []json.RawMessage{json.RawMessage(testJWK)},
					ReplayProtection: true,
				},
			},
		},
	}

	model := mapAPIClientToModel(ctx, api)

	assert.Equal(t, "client-1", model.ID.ValueString())
	assert.Equal(t, "client-1", model.ClientID.ValueString())
	assert.Equal(t, "API Client", model.Name.ValueString())
	assert.Equal(t, true, model.RequiresReauthentication.ValueBool())
	assert.True(t, model.SkipConsent.ValueBool())
	assert.Equal(t, "https://example.com/launch", model.LaunchURL.ValueString())
	assert.False(t, model.HasLogo.ValueBool())
	assert.True(t, model.HasDarkLogo.ValueBool())

	// Callback URLs
	var cb []string
	_ = model.CallbackURLs.ElementsAs(ctx, &cb, false)
	assert.Equal(t, []string{"https://example.com/callback"}, cb)

	// Allowed groups
	var gids []string
	_ = model.AllowedUserGroups.ElementsAs(ctx, &gids, false)
	assert.Equal(t, []string{"g1"}, gids)

	// Federated identities
	assert.False(t, model.FederatedIdentities.IsNull())
	var fed []clientFederatedIdentityModel
	_ = model.FederatedIdentities.ElementsAs(ctx, &fed, false)
	if assert.Len(t, fed, 1) {
		assert.Equal(t, "https://issuer.example.com", fed[0].Issuer.ValueString())
		assert.Equal(t, "subject-1", fed[0].Subject.ValueString())
		assert.True(t, fed[0].Audience.IsNull())
		assert.True(t, fed[0].JWKS.IsNull())
		assert.True(t, fed[0].ReplayProtection.ValueBool())
		var gotKeys []string
		_ = fed[0].PublicKeys.ElementsAs(ctx, &gotKeys, false)
		if assert.Len(t, gotKeys, 1) {
			assert.JSONEq(t, testJWK, gotKeys[0])
		}
	}
}

func TestMapAPIClientToModelAllowedGroupsOrderInsensitive(t *testing.T) {
	ctx := context.Background()

	newAPIClient := func(groups ...client.UserGroup) *client.OIDCClient {
		return &client.OIDCClient{
			ID:                "client-1",
			Name:              "API Client",
			CallbackURLs:      []string{"https://example.com/callback"},
			AllowedUserGroups: groups,
		}
	}

	groupA := client.UserGroup{ID: "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"}
	groupB := client.UserGroup{ID: "d4c3b2a1-f6e5-4b7a-9c8d-f0e1d2c3b4a5"}

	forward := mapAPIClientToModel(ctx, newAPIClient(groupA, groupB))
	reversed := mapAPIClientToModel(ctx, newAPIClient(groupB, groupA))

	assert.True(t,
		forward.AllowedUserGroups.Equal(reversed.AllowedUserGroups),
		"allowed_user_groups must compare equal regardless of API ordering, got %s vs %s",
		forward.AllowedUserGroups, reversed.AllowedUserGroups,
	)
	assert.Len(t, forward.AllowedUserGroups.Elements(), 2)
}

func TestMapAPIClientToModelNoFederatedIdentities(t *testing.T) {
	ctx := context.Background()

	api := &client.OIDCClient{
		ID:           "client-2",
		Name:         "No Fed",
		CallbackURLs: []string{"https://example.com/callback"},
	}

	model := mapAPIClientToModel(ctx, api)

	assert.True(t, model.FederatedIdentities.IsNull())
}

func TestMapAPIClientToModelNoCallbackURLs(t *testing.T) {
	ctx := context.Background()

	model := mapAPIClientToModel(ctx, &client.OIDCClient{ID: "client-4", Name: "Service account"})

	assert.True(t, model.CallbackURLs.IsNull())
}

func TestBuildCreateRequestFromPlanNoCallbackURLs(t *testing.T) {
	ctx := context.Background()

	req := buildCreateRequestFromPlan(ctx, &clientResourceModel{
		Name:         types.StringValue("Service account"),
		CallbackURLs: types.ListNull(types.StringType),
	})

	assert.Empty(t, req.CallbackURLs)
}

func TestMapAPIClientToModelFederatedIdentityWithoutKeys(t *testing.T) {
	ctx := context.Background()

	api := &client.OIDCClient{
		ID:           "client-3",
		Name:         "Fed JWKS",
		CallbackURLs: []string{"https://example.com/callback"},
		Credentials: client.OIDCClientCredentials{
			FederatedIdentities: []client.OIDCClientFederatedIdentity{
				{Issuer: "https://issuer.example.com", JWKS: "https://issuer.example.com/jwks"},
			},
		},
	}

	model := mapAPIClientToModel(ctx, api)

	var fed []clientFederatedIdentityModel
	_ = model.FederatedIdentities.ElementsAs(ctx, &fed, false)
	if assert.Len(t, fed, 1) {
		assert.Equal(t, "https://issuer.example.com/jwks", fed[0].JWKS.ValueString())
		assert.True(t, fed[0].PublicKeys.IsNull())
		assert.False(t, fed[0].ReplayProtection.ValueBool())
		assert.False(t, fed[0].ReplayProtection.IsNull())
	}
}

func TestFederatedIdentitiesUsePublicKeys(t *testing.T) {
	ctx := context.Background()

	build := func(keys types.List) *clientResourceModel {
		list, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: federatedIdentityAttrTypes}, []clientFederatedIdentityModel{
			{
				Issuer:           types.StringValue("https://issuer.example.com"),
				Subject:          types.StringNull(),
				Audience:         types.StringNull(),
				JWKS:             types.StringNull(),
				PublicKeys:       keys,
				ReplayProtection: types.BoolValue(false),
			},
		})
		if diags.HasError() {
			t.Fatalf("diags: %v", diags)
		}
		return &clientResourceModel{FederatedIdentities: list}
	}

	withKeys, diags := types.ListValueFrom(ctx, jsontypes.NormalizedType{}, []string{testJWK})
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}

	assert.True(t, federatedIdentitiesUsePublicKeys(ctx, build(withKeys)))
	assert.False(t, federatedIdentitiesUsePublicKeys(ctx, build(types.ListNull(jsontypes.NormalizedType{}))))
	assert.False(t, federatedIdentitiesUsePublicKeys(ctx, &clientResourceModel{FederatedIdentities: types.ListNull(types.ObjectType{AttrTypes: federatedIdentityAttrTypes})}))
}

func TestLogoFlag(t *testing.T) {
	url := types.StringValue("https://example.com/logo.svg")
	none := types.StringNull()

	tests := []struct {
		name     string
		url      types.String
		priorURL types.String
		server   bool
		want     bool
	}{
		// The update response can predate the download, so a set URL wins.
		{"set by terraform", url, none, false, true},
		{"removed by terraform", none, url, true, false},
		{"unmanaged logo present", none, none, true, true},
		{"unmanaged no logo", none, none, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, types.BoolValue(tt.want), logoFlag(tt.url, tt.priorURL, tt.server))
		})
	}
}

func TestLogoRef(t *testing.T) {
	file := types.ObjectValueMust(sourceFileAttrTypes, map[string]attr.Value{
		"source": types.StringValue("logo.svg"),
		"sha256": types.StringValue("abc"),
	})
	noFile := types.ObjectNull(sourceFileAttrTypes)
	url := types.StringValue("https://example.com/logo.svg")

	assert.True(t, logoRef(types.StringNull(), noFile).IsNull(), "no logo")
	assert.False(t, logoRef(url, noFile).IsNull(), "a URL")
	assert.False(t, logoRef(types.StringNull(), file).IsNull(), "a file")

	// A logo managed through a file is reported as set, like one set by URL.
	assert.True(t, logoFlag(logoRef(types.StringNull(), file), logoRef(types.StringNull(), noFile), false).ValueBool())
	assert.False(t, logoFlag(logoRef(types.StringNull(), noFile), logoRef(types.StringNull(), file), true).ValueBool())
}
