package client

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
)

// API represents an API (an OAuth resource server) in Pocket-ID, with its
// permissions. Pocket-ID returns the resource identifier as `resource`.
type API struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Resource         string          `json:"resource"`
	CreatedAt        string          `json:"createdAt,omitempty"`
	Permissions      []APIPermission `json:"permissions"`
	AllowCIMDClients bool            `json:"allowCimdClients"`
}

// APIPermission represents a permission of an API: the scope a client may
// request when it asks for a token for the API.
type APIPermission struct {
	ID                    string  `json:"id"`
	Key                   string  `json:"key"`
	Name                  string  `json:"name"`
	Description           *string `json:"description,omitempty"`
	AllowedForCIMDClients bool    `json:"allowedForCimdClients"`
}

// APICreateRequest represents a request to create an API. The resource
// identifier is only accepted here, Pocket-ID cannot change it afterwards.
type APICreateRequest struct {
	Name     string `json:"name"`
	Resource string `json:"resource"`
}

// APIUpdateRequest represents a request to update an API. Only the name can
// be updated.
type APIUpdateRequest struct {
	Name string `json:"name"`
}

// APIPermissionInput is a permission in a request that replaces the
// permissions of an API. Pocket-ID matches it to an existing permission by key.
type APIPermissionInput struct {
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

// APIPermissionsUpdateRequest represents a request to replace the full set of
// permissions of an API.
type APIPermissionsUpdateRequest struct {
	Permissions []APIPermissionInput `json:"permissions"`
}

// APICIMDAccessRequest represents a request to replace which permissions of an
// API every client registered through a Client ID Metadata Document may request.
type APICIMDAccessRequest struct {
	Enabled       bool     `json:"enabled"`
	PermissionIDs []string `json:"permissionIds"`
}

// APIClientGrant is what one client may do with a single API: user-delegated
// access (the client acts for a signed-in user) and client access (the client
// acts as itself, machine to machine), each with its permission IDs.
type APIClientGrant struct {
	UserDelegatedAccess        bool     `json:"userDelegatedAccess"`
	ClientAccess               bool     `json:"clientAccess"`
	UserDelegatedPermissionIDs []string `json:"userDelegatedPermissionIds"`
	ClientPermissionIDs        []string `json:"clientPermissionIds"`
}

// ClientAPIGrant is one API a client may reach, with the grants that apply to
// it. The CIMD fields are what the API's metadata-document opt-in gives the
// client, which is read-only here.
type ClientAPIGrant struct {
	API APIGrantTarget `json:"api"`
	APIClientGrant
	CIMDGrantedAccess        bool     `json:"cimdGrantedAccess"`
	CIMDGrantedPermissionIDs []string `json:"cimdGrantedPermissionIds"`
}

// APIGrantTarget is the part of an API a client's grant list returns.
type APIGrantTarget struct {
	ID string `json:"id"`
}

// CreateAPI creates an API.
func (c *Client) CreateAPI(req *APICreateRequest) (*API, error) {
	body, err := c.doRequest("POST", "/api/apis", req)
	if err != nil {
		return nil, err
	}
	return unmarshalAPI(body)
}

// GetAPI retrieves an API, with its permissions, by ID.
func (c *Client) GetAPI(id string) (*API, error) {
	body, err := c.doRequest("GET", fmt.Sprintf("/api/apis/%s", url.PathEscape(id)), nil)
	if err != nil {
		return nil, err
	}
	return unmarshalAPI(body)
}

// UpdateAPI updates the name of an API.
func (c *Client) UpdateAPI(id string, req *APIUpdateRequest) (*API, error) {
	body, err := c.doRequest("PUT", fmt.Sprintf("/api/apis/%s", url.PathEscape(id)), req)
	if err != nil {
		return nil, err
	}
	return unmarshalAPI(body)
}

// DeleteAPI deletes an API, and with it its permissions and every client's
// access to it.
func (c *Client) DeleteAPI(id string) error {
	_, err := c.doRequest("DELETE", fmt.Sprintf("/api/apis/%s", url.PathEscape(id)), nil)
	return err
}

// UpdateAPIPermissions replaces the full set of permissions of an API.
// Pocket-ID matches permissions by key: a key that stays keeps its ID and the
// grants on it, a key that is left out is deleted together with the grants on
// it, and a new key is created.
func (c *Client) UpdateAPIPermissions(id string, permissions []APIPermissionInput) (*API, error) {
	// Ensure permissions is never nil to serialize as an empty array instead of null
	if permissions == nil {
		permissions = []APIPermissionInput{}
	}
	body, err := c.doRequest("PUT", fmt.Sprintf("/api/apis/%s/permissions", url.PathEscape(id)), APIPermissionsUpdateRequest{Permissions: permissions})
	if err != nil {
		return nil, err
	}
	return unmarshalAPI(body)
}

// UpdateAPICIMDAccess replaces whether every metadata-document client may use
// an API and which permissions they may request. The permission selection is
// kept while access is disabled.
func (c *Client) UpdateAPICIMDAccess(id string, enabled bool, permissionIDs []string) (*API, error) {
	// Ensure permissionIDs is never nil to serialize as an empty array instead of null
	if permissionIDs == nil {
		permissionIDs = []string{}
	}
	body, err := c.doRequest("PUT", fmt.Sprintf("/api/apis/%s/cimd-access", url.PathEscape(id)), APICIMDAccessRequest{Enabled: enabled, PermissionIDs: permissionIDs})
	if err != nil {
		return nil, err
	}
	return unmarshalAPI(body)
}

// SetAPIClientAccess replaces what one client may do with one API, and returns
// what Pocket-ID applied. Pocket-ID ignores permission IDs that do not belong
// to the API, drops client access for a public client, and counts a permission
// as access for its subject type, so the result can differ from the request.
func (c *Client) SetAPIClientAccess(apiID, clientID string, grant *APIClientGrant) (*APIClientGrant, error) {
	req := *grant
	// Ensure the lists are never nil to serialize as empty arrays instead of null
	if req.UserDelegatedPermissionIDs == nil {
		req.UserDelegatedPermissionIDs = []string{}
	}
	if req.ClientPermissionIDs == nil {
		req.ClientPermissionIDs = []string{}
	}
	body, err := c.doRequest("PUT", fmt.Sprintf("/api/apis/%s/clients/%s", url.PathEscape(apiID), clientIDPathSegment(clientID)), req)
	if err != nil {
		return nil, err
	}

	var result APIClientGrant
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}
	return &result, nil
}

// RemoveAPIClientAccess revokes every grant a client holds on one API.
func (c *Client) RemoveAPIClientAccess(apiID, clientID string) error {
	_, err := c.doRequest("DELETE", fmt.Sprintf("/api/apis/%s/clients/%s", url.PathEscape(apiID), clientIDPathSegment(clientID)), nil)
	return err
}

// ListClientAPIs returns every API a client may request tokens for, with the
// client's grants on each. Pocket-ID has no endpoint for one client's grant on
// one API, so a grant is read from this list.
func (c *Client) ListClientAPIs(clientID string) ([]ClientAPIGrant, error) {
	body, err := c.doRequest("GET", fmt.Sprintf("/api/api-access/%s/apis", clientIDPathSegment(clientID)), nil)
	if err != nil {
		return nil, err
	}

	var result []ClientAPIGrant
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}
	return result, nil
}

func unmarshalAPI(body []byte) (*API, error) {
	var result API
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}
	return &result, nil
}

// clientIDPathSegment returns a client ID as Pocket-ID reads it from a URL
// path. A client ID made of letters, digits, '.', '_' and '-' goes in as is. The
// ID of a client registered through a Client ID Metadata Document is a URL,
// which cannot be carried in one path segment, so it is sent as "~" and its
// unpadded base64url encoding, which Pocket-ID decodes
// (backend/internal/middleware/client_id_param.go).
func clientIDPathSegment(clientID string) string {
	plain := clientID != ""
	for _, r := range clientID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			plain = false
			break
		}
	}
	if plain {
		return clientID
	}
	return "~" + base64.RawURLEncoding.EncodeToString([]byte(clientID))
}
