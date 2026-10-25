//go:build acc
// +build acc

package provider_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testAccAPIConfig is an API with two permissions, read and write, a
// confidential client with client access to read, another confidential client
// with no access, and a public client.
func testAccAPIConfig(rName, apiName string, extra string) string {
	return fmt.Sprintf(`
resource "pocketid_api" "test" {
  name     = %[2]q
  resource = "https://%[1]s.api.example.com"

  permissions = {
    "read:orders" = {
      name        = "Read orders"
      description = "Read the orders"
    }
    "write:orders" = {
      name = "Write orders"
    }
  }
}

resource "pocketid_client" "granted" {
  name          = "%[1]s granted"
  callback_urls = ["https://%[1]s.example.com/callback"]
  is_public     = false
}

resource "pocketid_client" "denied" {
  name          = "%[1]s denied"
  callback_urls = ["https://%[1]s.example.com/callback"]
  is_public     = false
}

resource "pocketid_client" "public" {
  name          = "%[1]s public"
  callback_urls = ["https://%[1]s.example.com/callback"]
  is_public     = true
}
%[3]s
`, rName, apiName, extra)
}

func TestAccResourceAPI_basic(t *testing.T) {
	resourceName := "pocketid_api.test"
	rName := acctest.RandomWithPrefix("tf-acc")

	var firstID string
	var readID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create with two permissions
			{
				Config: testAccAPIConfig(rName, "Orders API", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "name", "Orders API"),
					resource.TestCheckResourceAttr(resourceName, "resource", "https://"+rName+".api.example.com"),
					resource.TestCheckResourceAttr(resourceName, "allow_cimd_clients", "false"),
					resource.TestCheckResourceAttr(resourceName, "permissions.%", "2"),
					resource.TestCheckResourceAttr(resourceName, "permissions.read:orders.name", "Read orders"),
					resource.TestCheckResourceAttr(resourceName, "permissions.read:orders.description", "Read the orders"),
					resource.TestCheckResourceAttrSet(resourceName, "permissions.read:orders.id"),
					resource.TestCheckResourceAttr(resourceName, "permissions.read:orders.allowed_for_cimd_clients", "false"),
					resource.TestCheckResourceAttr(resourceName, "permissions.write:orders.name", "Write orders"),
					resource.TestCheckNoResourceAttr(resourceName, "permissions.write:orders.description"),
					testAccCheckAPIPermissionKeys(resourceName, "read:orders", "write:orders"),
					testAccCaptureAttr(resourceName, "permissions.read:orders.id", &firstID),
				),
			},
			// Import
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Rename, edit one permission, drop one and add one. The key that stays keeps its ID.
			{
				Config: strings.Replace(
					strings.Replace(testAccAPIConfig(rName, "Orders API v2", ""), `description = "Read the orders"`, `description = "Read all the orders"`, 1),
					`"write:orders" = {
      name = "Write orders"
    }`, `"delete:orders" = {
      name = "Delete orders"
    }`, 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "name", "Orders API v2"),
					resource.TestCheckResourceAttr(resourceName, "permissions.%", "2"),
					resource.TestCheckResourceAttr(resourceName, "permissions.read:orders.description", "Read all the orders"),
					resource.TestCheckResourceAttr(resourceName, "permissions.delete:orders.name", "Delete orders"),
					resource.TestCheckNoResourceAttr(resourceName, "permissions.write:orders.name"),
					testAccCheckAPIPermissionKeys(resourceName, "read:orders", "delete:orders"),
					testAccCaptureAttr(resourceName, "permissions.read:orders.id", &readID),
					func(*terraform.State) error {
						if readID != firstID {
							return fmt.Errorf("permission read:orders changed its ID from %s to %s", firstID, readID)
						}
						return nil
					},
				),
			},
			// Empty permissions delete them all
			{
				Config: fmt.Sprintf(`
resource "pocketid_api" "test" {
  name        = "Orders API v2"
  resource    = "https://%s.api.example.com"
  permissions = {}
}
`, rName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "permissions.%", "0"),
					testAccCheckAPIPermissionKeys(resourceName),
				),
			},
		},
	})
}

// The resource identifier cannot change in Pocket-ID, so a change replaces the API.
func TestAccResourceAPI_resourceReplaces(t *testing.T) {
	resourceName := "pocketid_api.test"
	rName := acctest.RandomWithPrefix("tf-acc")
	config := func(resourceURI string) string {
		return fmt.Sprintf(`
resource "pocketid_api" "test" {
  name     = "Replace test"
  resource = %q
}
`, resourceURI)
	}

	var firstID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("https://" + rName + "-a.example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "permissions.%", "0"),
					testAccCaptureAttr(resourceName, "id", &firstID),
				),
			},
			{
				Config: config("https://" + rName + "-b.example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionReplace),
					},
				},
				Check: func(s *terraform.State) error {
					if s.RootModule().Resources[resourceName].Primary.ID == firstID {
						return fmt.Errorf("API was not replaced")
					}
					return nil
				},
			},
		},
	})
}

// Pocket-ID stores the resource without trailing slashes, so the provider
// refuses one rather than plan a value the API would not return.
func TestAccResourceAPI_resourceTrailingSlash(t *testing.T) {
	rName := acctest.RandomWithPrefix("tf-acc")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "pocketid_api" "test" {
  name     = "Slash"
  resource = "https://%s.example.com/"
}
`, rName),
				ExpectError: regexp.MustCompile(`must\s+not\s+end\s+with\s+a\s+slash`),
			},
		},
	})
}

// Setting up allow_cimd_clients and which permissions the metadata-document
// clients may request.
func TestAccResourceAPI_cimdClients(t *testing.T) {
	resourceName := "pocketid_api.test"
	rName := acctest.RandomWithPrefix("tf-acc")
	config := func(enabled bool, readAllowed bool) string {
		return fmt.Sprintf(`
resource "pocketid_api" "test" {
  name               = "CIMD test"
  resource           = "https://%[1]s.api.example.com"
  allow_cimd_clients = %[2]t

  permissions = {
    "read:orders" = {
      name                     = "Read orders"
      allowed_for_cimd_clients = %[3]t
    }
    "write:orders" = {
      name = "Write orders"
    }
  }
}
`, rName, enabled, readAllowed)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(true, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "allow_cimd_clients", "true"),
					resource.TestCheckResourceAttr(resourceName, "permissions.read:orders.allowed_for_cimd_clients", "true"),
					resource.TestCheckResourceAttr(resourceName, "permissions.write:orders.allowed_for_cimd_clients", "false"),
					testAccCheckAPICIMD(resourceName, true, "read:orders"),
				),
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Off, with the selection kept, as Pocket-ID does
			{
				Config: config(false, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "allow_cimd_clients", "false"),
					testAccCheckAPICIMD(resourceName, false, "read:orders"),
				),
			},
			// On again, with no permission selected
			{
				Config: config(true, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "allow_cimd_clients", "true"),
					testAccCheckAPICIMD(resourceName, true),
				),
			},
		},
	})
}

// A client with client access gets a token for the API whose audience is the
// resource and whose scope carries the granted permission. A client without
// access is refused.
func TestAccResourceAPIClientAccess_clientCredentialsToken(t *testing.T) {
	resourceName := "pocketid_api_client_access.granted"
	rName := acctest.RandomWithPrefix("tf-acc")
	apiResource := "https://" + rName + ".api.example.com"

	config := testAccAPIConfig(rName, "Orders API", `
resource "pocketid_api_client_access" "granted" {
  api_id                = pocketid_api.test.id
  client_id             = pocketid_client.granted.id
  client_permission_ids = [pocketid_api.test.permissions["read:orders"].id]
}
`)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "client_access", "true"),
					resource.TestCheckResourceAttr(resourceName, "client_permission_ids.#", "1"),
					resource.TestCheckTypeSetElemAttrPair(resourceName, "client_permission_ids.*", "pocketid_api.test", "permissions.read:orders.id"),
					resource.TestCheckResourceAttr(resourceName, "user_delegated_access", "false"),
					resource.TestCheckResourceAttr(resourceName, "user_delegated_permission_ids.#", "0"),
					func(s *terraform.State) error {
						granted := s.RootModule().Resources["pocketid_client.granted"].Primary.ID
						denied := s.RootModule().Resources["pocketid_client.denied"].Primary.ID

						// The granted client gets a token for the API with the permission.
						token, err := testAccClientCredentialsToken(granted, apiResource, "read:orders")
						if err != nil {
							return err
						}
						if token.Error != "" {
							return fmt.Errorf("granted client was refused: %s %s", token.Error, token.ErrorDescription)
						}
						claims, err := testAccJWTClaims(token.AccessToken)
						if err != nil {
							return err
						}
						if !testAccClaimContains(claims["aud"], apiResource) {
							return fmt.Errorf("aud = %v, want %s", claims["aud"], apiResource)
						}
						if got, _ := claims["scope"].(string); got != "read:orders" {
							return fmt.Errorf("scope = %q, want %q", got, "read:orders")
						}

						// Without a scope it gets everything it is granted for the API.
						token, err = testAccClientCredentialsToken(granted, apiResource, "")
						if err != nil {
							return err
						}
						if token.Error != "" {
							return fmt.Errorf("granted client was refused without a scope: %s %s", token.Error, token.ErrorDescription)
						}
						claims, err = testAccJWTClaims(token.AccessToken)
						if err != nil {
							return err
						}
						if got, _ := claims["scope"].(string); got != "read:orders" {
							return fmt.Errorf("scope without a request = %q, want %q", got, "read:orders")
						}

						// It cannot ask for a permission it is not granted.
						if token, err = testAccClientCredentialsToken(granted, apiResource, "write:orders"); err != nil {
							return err
						}
						if token.Error != "invalid_scope" {
							return fmt.Errorf("ungranted permission: error = %q (%s), want invalid_scope", token.Error, token.ErrorDescription)
						}

						// The client without access to the API is refused with access_denied
						// when it asks for a token for the API, and with invalid_scope when
						// it also asks for a permission, which it cannot request either.
						token, err = testAccClientCredentialsToken(denied, apiResource, "")
						if err != nil {
							return err
						}
						if token.Error != "access_denied" {
							return fmt.Errorf("client without access: error = %q (%s), want access_denied", token.Error, token.ErrorDescription)
						}
						token, err = testAccClientCredentialsToken(denied, apiResource, "read:orders")
						if err != nil {
							return err
						}
						if token.Error != "invalid_scope" {
							return fmt.Errorf("client without access asking for a permission: error = %q (%s), want invalid_scope", token.Error, token.ErrorDescription)
						}
						return nil
					},
				),
			},
			// Import
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Revoking the access, by removing the resource, refuses the client.
			{
				Config: testAccAPIConfig(rName, "Orders API", ""),
				Check: func(s *terraform.State) error {
					granted := s.RootModule().Resources["pocketid_client.granted"].Primary.ID
					token, err := testAccClientCredentialsToken(granted, apiResource, "")
					if err != nil {
						return err
					}
					if token.Error != "access_denied" {
						return fmt.Errorf("client after revoking: error = %q (%s), want access_denied", token.Error, token.ErrorDescription)
					}
					return nil
				},
			},
		},
	})
}

// User-delegated access, both kinds at once, changing the permissions, and
// access with no permission.
func TestAccResourceAPIClientAccess_kinds(t *testing.T) {
	resourceName := "pocketid_api_client_access.test"
	rName := acctest.RandomWithPrefix("tf-acc")
	config := func(body string) string {
		return testAccAPIConfig(rName, "Orders API", `
resource "pocketid_api_client_access" "test" {
  api_id    = pocketid_api.test.id
  client_id = pocketid_client.granted.id
`+body+`
}
`)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// User-delegated access only
			{
				Config: config(`user_delegated_permission_ids = [pocketid_api.test.permissions["read:orders"].id]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "user_delegated_access", "true"),
					resource.TestCheckResourceAttr(resourceName, "user_delegated_permission_ids.#", "1"),
					resource.TestCheckResourceAttr(resourceName, "client_access", "false"),
					resource.TestCheckResourceAttr(resourceName, "client_permission_ids.#", "0"),
				),
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Both kinds, with different permissions
			{
				Config: config(`
  user_delegated_permission_ids = [pocketid_api.test.permissions["read:orders"].id]
  client_permission_ids = [
    pocketid_api.test.permissions["read:orders"].id,
    pocketid_api.test.permissions["write:orders"].id,
  ]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "user_delegated_access", "true"),
					resource.TestCheckResourceAttr(resourceName, "client_access", "true"),
					resource.TestCheckResourceAttr(resourceName, "user_delegated_permission_ids.#", "1"),
					resource.TestCheckResourceAttr(resourceName, "client_permission_ids.#", "2"),
				),
			},
			// Access without a permission
			{
				Config: config(`client_access = true`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "user_delegated_access", "false"),
					resource.TestCheckResourceAttr(resourceName, "client_access", "true"),
					resource.TestCheckResourceAttr(resourceName, "client_permission_ids.#", "0"),
				),
			},
		},
	})
}

func TestAccResourceAPIClientAccess_invalid(t *testing.T) {
	rName := acctest.RandomWithPrefix("tf-acc")
	config := func(clientName, body string) string {
		return testAccAPIConfig(rName, "Orders API", fmt.Sprintf(`
resource "pocketid_api_client_access" "test" {
  api_id    = pocketid_api.test.id
  client_id = pocketid_client.%s.id
%s
}
`, clientName, body))
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// A public client cannot have client access
			{
				Config:      config("public", `client_access = true`),
				ExpectError: regexp.MustCompile(`client_access\s+is\s+false,\s+not\s+true`),
			},
			{
				Config:      config("public", `client_permission_ids = [pocketid_api.test.permissions["read:orders"].id]`),
				ExpectError: regexp.MustCompile(`client_permission_ids\s+not\s+applied`),
			},
			// A permission that is not the API's
			{
				Config:      config("granted", `client_permission_ids = ["00000000-0000-0000-0000-000000000000"]`),
				ExpectError: regexp.MustCompile(`client_permission_ids\s+not\s+applied`),
			},
			// False next to permissions
			{
				Config: config("granted", `
  client_access         = false
  client_permission_ids = [pocketid_api.test.permissions["read:orders"].id]`),
				ExpectError: regexp.MustCompile(`Access\s+is\s+false\s+but\s+permissions\s+are\s+listed`),
			},
			// Nothing granted
			{
				Config:      config("granted", ``),
				ExpectError: regexp.MustCompile(`No\s+access\s+granted`),
			},
		},
	})
}

// A grant that was revoked in Pocket-ID is planned again.
func TestAccResourceAPIClientAccess_revokedOutsideTerraform(t *testing.T) {
	resourceName := "pocketid_api_client_access.test"
	rName := acctest.RandomWithPrefix("tf-acc")
	config := testAccAPIConfig(rName, "Orders API", `
resource "pocketid_api_client_access" "test" {
  api_id                = pocketid_api.test.id
  client_id             = pocketid_client.granted.id
  client_permission_ids = [pocketid_api.test.permissions["read:orders"].id]
}
`)

	var apiID, clientID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCaptureAttr(resourceName, "api_id", &apiID),
					testAccCaptureAttr(resourceName, "client_id", &clientID),
				),
			},
			{
				PreConfig: func() {
					c, err := testClient()
					if err != nil {
						t.Fatal(err)
					}
					if err := c.RemoveAPIClientAccess(apiID, clientID); err != nil {
						t.Fatal(err)
					}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionCreate),
					},
				},
				Check: resource.TestCheckResourceAttr(resourceName, "client_permission_ids.#", "1"),
			},
		},
	})
}

func testAccCaptureAttr(resourceName, attribute string, into *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("Not found: %s", resourceName)
		}
		*into = rs.Primary.Attributes[attribute]
		return nil
	}
}

// testAccCheckAPIPermissionKeys verifies, through the Pocket ID API, that the
// API has exactly these permission keys.
func testAccCheckAPIPermissionKeys(resourceName string, keys ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("Not found: %s", resourceName)
		}
		c, err := testClient()
		if err != nil {
			return err
		}
		api, err := c.GetAPI(rs.Primary.ID)
		if err != nil {
			return err
		}
		got := map[string]bool{}
		for _, permission := range api.Permissions {
			got[permission.Key] = true
		}
		if len(got) != len(keys) {
			return fmt.Errorf("API has permissions %v, want %v", got, keys)
		}
		for _, key := range keys {
			if !got[key] {
				return fmt.Errorf("API has permissions %v, want %v", got, keys)
			}
		}
		return nil
	}
}

// testAccCheckAPICIMD verifies, through the Pocket ID API, the metadata-document
// client access of an API: whether it is on and which permission keys are selected.
func testAccCheckAPICIMD(resourceName string, enabled bool, selected ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("Not found: %s", resourceName)
		}
		c, err := testClient()
		if err != nil {
			return err
		}
		api, err := c.GetAPI(rs.Primary.ID)
		if err != nil {
			return err
		}
		if api.AllowCIMDClients != enabled {
			return fmt.Errorf("allowCimdClients = %t, want %t", api.AllowCIMDClients, enabled)
		}
		got := map[string]bool{}
		for _, permission := range api.Permissions {
			if permission.AllowedForCIMDClients {
				got[permission.Key] = true
			}
		}
		if len(got) != len(selected) {
			return fmt.Errorf("CIMD permissions %v, want %v", got, selected)
		}
		for _, key := range selected {
			if !got[key] {
				return fmt.Errorf("CIMD permissions %v, want %v", got, selected)
			}
		}
		return nil
	}
}

type testAccTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// testAccClientCredentialsToken asks Pocket-ID for a client_credentials token for
// resource, authenticating as clientID with a secret it creates for the
// client through the API. Pocket-ID answers a refusal with a JSON error, which is
// returned in the response, not as an error.
func testAccClientCredentialsToken(clientID, resourceURI, scope string) (*testAccTokenResponse, error) {
	secret, err := testAccCreateClientSecret(clientID)
	if err != nil {
		return nil, err
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {secret},
		"resource":      {resourceURI},
		"scope":         {scope},
	}
	resp, err := http.PostForm(os.Getenv("POCKETID_BASE_URL")+"/api/oidc/token", form)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var token testAccTokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, fmt.Errorf("token response %d is not JSON: %s", resp.StatusCode, body)
	}
	if resp.StatusCode >= 400 && token.Error == "" {
		return nil, fmt.Errorf("token response %d: %s", resp.StatusCode, body)
	}
	return &token, nil
}

// testAccCreateClientSecret creates a secret for a client directly through the
// Pocket-ID API and returns its value. It does not depend on how the provider
// manages secrets.
func testAccCreateClientSecret(clientID string) (string, error) {
	req, err := http.NewRequest(http.MethodPost, os.Getenv("POCKETID_BASE_URL")+"/api/oidc/clients/"+clientID+"/secrets", bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", os.Getenv("POCKETID_API_TOKEN"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("creating a secret for %s: %d %s", clientID, resp.StatusCode, body)
	}
	var created struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Secret == "" {
		return "", fmt.Errorf("creating a secret for %s: no secret in %s", clientID, body)
	}
	return created.Secret, nil
}

// testAccJWTClaims decodes the claims of a JWT without verifying it: the token
// comes straight from Pocket-ID.
func testAccJWTClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("access token is not a JWT: %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// testAccClaimContains reports whether a string-or-array claim holds value.
func testAccClaimContains(claim any, value string) bool {
	switch v := claim.(type) {
	case string:
		return v == value
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == value {
				return true
			}
		}
	}
	return false
}
