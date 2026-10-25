package client_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

func newAPITestClient(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	c, err := client.NewClient(server.URL, "test-token", false, 30)
	require.NoError(t, err)
	return c
}

const apiJSON = `{
	"id": "api-1",
	"name": "Orders",
	"resource": "https://api.example.com",
	"createdAt": "2026-01-01T00:00:00Z",
	"allowCimdClients": true,
	"permissions": [
		{"id": "p1", "key": "read:orders", "name": "Read orders", "description": "Read", "allowedForCimdClients": true},
		{"id": "p2", "key": "write:orders", "name": "Write orders", "allowedForCimdClients": false}
	]
}`

func TestClient_CreateAPI(t *testing.T) {
	c := newAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/apis", r.URL.Path)

		var req client.APICreateRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "Orders", req.Name)
		assert.Equal(t, "https://api.example.com", req.Resource)

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(apiJSON))
	})

	api, err := c.CreateAPI(&client.APICreateRequest{Name: "Orders", Resource: "https://api.example.com"})
	require.NoError(t, err)
	assert.Equal(t, "api-1", api.ID)
	assert.Equal(t, "https://api.example.com", api.Resource)
	assert.True(t, api.AllowCIMDClients)
	require.Len(t, api.Permissions, 2)
	assert.Equal(t, "read:orders", api.Permissions[0].Key)
	assert.True(t, api.Permissions[0].AllowedForCIMDClients)
	require.NotNil(t, api.Permissions[0].Description)
	assert.Equal(t, "Read", *api.Permissions[0].Description)
	assert.Nil(t, api.Permissions[1].Description)
}

func TestClient_GetUpdateDeleteAPI(t *testing.T) {
	var calls []string
	c := newAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.Method {
		case http.MethodPut:
			var req client.APIUpdateRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, "Renamed", req.Name)
			_, _ = w.Write([]byte(apiJSON))
		case http.MethodGet:
			_, _ = w.Write([]byte(apiJSON))
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	_, err := c.GetAPI("api-1")
	require.NoError(t, err)
	_, err = c.UpdateAPI("api-1", &client.APIUpdateRequest{Name: "Renamed"})
	require.NoError(t, err)
	require.NoError(t, c.DeleteAPI("api-1"))

	assert.Equal(t, []string{"GET /api/apis/api-1", "PUT /api/apis/api-1", "DELETE /api/apis/api-1"}, calls)
}

func TestClient_GetAPI_NotFound(t *testing.T) {
	c := newAPITestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"API not found"}`))
	})

	_, err := c.GetAPI("missing")
	require.Error(t, err)
	assert.True(t, client.IsNotFound(err))
	assert.False(t, client.IsNotFound(nil))
}

func TestClient_UpdateAPIPermissions(t *testing.T) {
	c := newAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/api/apis/api-1/permissions", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{"permissions":[{"key":"read:orders","name":"Read orders","description":"Read"},{"key":"write:orders","name":"Write orders","description":null}]}`, string(body))

		_, _ = w.Write([]byte(apiJSON))
	})

	read := "Read"
	_, err := c.UpdateAPIPermissions("api-1", []client.APIPermissionInput{
		{Key: "read:orders", Name: "Read orders", Description: &read},
		{Key: "write:orders", Name: "Write orders"},
	})
	require.NoError(t, err)
}

func TestClient_UpdateAPIPermissions_EmptySendsEmptyArray(t *testing.T) {
	c := newAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{"permissions":[]}`, string(body))
		_, _ = w.Write([]byte(apiJSON))
	})

	_, err := c.UpdateAPIPermissions("api-1", nil)
	require.NoError(t, err)
}

func TestClient_UpdateAPICIMDAccess(t *testing.T) {
	c := newAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/api/apis/api-1/cimd-access", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{"enabled":true,"permissionIds":["p1"]}`, string(body))
		_, _ = w.Write([]byte(apiJSON))
	})

	_, err := c.UpdateAPICIMDAccess("api-1", true, []string{"p1"})
	require.NoError(t, err)
}

func TestClient_UpdateAPICIMDAccess_EmptySendsEmptyArray(t *testing.T) {
	c := newAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{"enabled":false,"permissionIds":[]}`, string(body))
		_, _ = w.Write([]byte(apiJSON))
	})

	_, err := c.UpdateAPICIMDAccess("api-1", false, nil)
	require.NoError(t, err)
}

func TestClient_SetAPIClientAccess(t *testing.T) {
	c := newAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/api/apis/api-1/clients/client-1", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{"userDelegatedAccess":false,"clientAccess":true,"userDelegatedPermissionIds":[],"clientPermissionIds":["p1"]}`, string(body))

		_, _ = w.Write([]byte(`{"userDelegatedAccess":false,"clientAccess":true,"userDelegatedPermissionIds":[],"clientPermissionIds":["p1"]}`))
	})

	applied, err := c.SetAPIClientAccess("api-1", "client-1", &client.APIClientGrant{
		ClientAccess:        true,
		ClientPermissionIDs: []string{"p1"},
	})
	require.NoError(t, err)
	assert.True(t, applied.ClientAccess)
	assert.False(t, applied.UserDelegatedAccess)
	assert.Equal(t, []string{"p1"}, applied.ClientPermissionIDs)
	assert.Empty(t, applied.UserDelegatedPermissionIDs)
}

func TestClient_RemoveAPIClientAccess(t *testing.T) {
	c := newAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/apis/api-1/clients/client-1", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})

	require.NoError(t, c.RemoveAPIClientAccess("api-1", "client-1"))
}

func TestClient_ListClientAPIs(t *testing.T) {
	c := newAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/api-access/client-1/apis", r.URL.Path)

		_, _ = w.Write([]byte(`[{
			"api": ` + apiJSON + `,
			"userDelegatedAccess": true,
			"clientAccess": true,
			"userDelegatedPermissionIds": ["p1"],
			"clientPermissionIds": ["p1", "p2"],
			"cimdGrantedAccess": false,
			"cimdGrantedPermissionIds": []
		}]`))
	})

	grants, err := c.ListClientAPIs("client-1")
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.Equal(t, "api-1", grants[0].API.ID)
	assert.True(t, grants[0].UserDelegatedAccess)
	assert.True(t, grants[0].ClientAccess)
	assert.Equal(t, []string{"p1"}, grants[0].UserDelegatedPermissionIDs)
	assert.Equal(t, []string{"p1", "p2"}, grants[0].ClientPermissionIDs)
	assert.False(t, grants[0].CIMDGrantedAccess)
}

func TestClient_ListClientAPIs_UnmarshalError(t *testing.T) {
	c := newAPITestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{`))
	})

	_, err := c.ListClientAPIs("client-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "error unmarshaling response")
}

// The client ID of a metadata-document client is a URL. Pocket-ID reads it from
// a path as "~" and its unpadded base64url encoding, and a plain ID as is.
func TestClient_APIClientAccess_URLClientIDPath(t *testing.T) {
	const urlID = "https://client.example.com/metadata.json"
	encoded := "~" + base64.RawURLEncoding.EncodeToString([]byte(urlID))

	var calls []string
	c := newAPITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// RawPath is set when the path has escapes; EscapedPath is what was sent.
		calls = append(calls, r.Method+" "+r.URL.EscapedPath())
		switch r.Method {
		case http.MethodPut:
			_, _ = w.Write([]byte(`{"userDelegatedAccess":true,"clientAccess":false,"userDelegatedPermissionIds":[],"clientPermissionIds":[]}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	_, err := c.SetAPIClientAccess("api-1", urlID, &client.APIClientGrant{UserDelegatedAccess: true})
	require.NoError(t, err)
	_, err = c.ListClientAPIs(urlID)
	require.NoError(t, err)
	require.NoError(t, c.RemoveAPIClientAccess("api-1", urlID))
	_, err = c.ListClientAPIs("plain.client-1_x")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"PUT /api/apis/api-1/clients/" + encoded,
		"GET /api/api-access/" + encoded + "/apis",
		"DELETE /api/apis/api-1/clients/" + encoded,
		"GET /api/api-access/plain.client-1_x/apis",
	}, calls)
	assert.NotContains(t, encoded, "/")
	assert.NotContains(t, encoded, "=")
}
