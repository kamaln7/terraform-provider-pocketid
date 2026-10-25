//go:build acc
// +build acc

package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

const (
	testSecretA = "secret-a-0123456789abcdefghij"
	testSecretB = "secret-b-0123456789abcdefghij"
	testSecretC = "secret-c-0123456789abcdefghij"
)

// serverRequest sends a request to the Pocket ID server the tests run against.
func serverRequest(method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = strings.NewReader(string(encoded))
	}

	req, err := http.NewRequestWithContext(context.Background(), method, os.Getenv("POCKETID_BASE_URL")+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", os.Getenv("POCKETID_API_TOKEN"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

// setServerAutoCreateClientSecret sets Pocket ID's autoCreateOidcClientSecret
// setting for the test and restores the previous value afterwards. Pocket ID
// reads AUTO_CREATE_OIDC_CLIENT_SECRET only when UI_CONFIG_DISABLED is set; a
// server that is configured through its API, like the one the acceptance tests
// start, keeps the setting in its database and applies a change at once.
func setServerAutoCreateClientSecret(t *testing.T, want bool) {
	t.Helper()

	read := func() (map[string]string, error) {
		data, status, err := serverRequest(http.MethodGet, "/api/application-configuration/all", nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("reading the application configuration: HTTP %d: %s", status, data)
		}
		var vars []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(data, &vars); err != nil {
			return nil, err
		}
		values := make(map[string]string, len(vars))
		for _, v := range vars {
			values[v.Key] = v.Value
		}
		return values, nil
	}

	write := func(values map[string]string, value bool) error {
		update := make(map[string]string, len(values))
		for k, v := range values {
			update[k] = v
		}
		update["autoCreateOidcClientSecret"] = fmt.Sprintf("%t", value)
		data, status, err := serverRequest(http.MethodPut, "/api/application-configuration", update)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("updating the application configuration: HTTP %d: %s", status, data)
		}
		return nil
	}

	original, err := read()
	if err != nil {
		t.Fatal(err)
	}
	was := original["autoCreateOidcClientSecret"] == "true"
	if was == want {
		return
	}
	if err := write(original, want); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := write(original, was); err != nil {
			t.Errorf("restoring autoCreateOidcClientSecret: %v", err)
		}
	})
}

// requestClientCredentialsToken authenticates a client at the token endpoint
// and returns the HTTP status. 200 means the secret was accepted.
func requestClientCredentialsToken(clientID, secret string) (int, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {secret},
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		os.Getenv("POCKETID_BASE_URL")+"/api/oidc/token", strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// checkClientSecretAccepted asserts the secret authenticates the client.
func checkClientSecretAccepted(clientID, secret string) error {
	status, err := requestClientCredentialsToken(clientID, secret)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("the token endpoint refused the secret: HTTP %d", status)
	}
	return nil
}

// checkClientSecretRefused asserts the secret does not authenticate the client.
func checkClientSecretRefused(clientID, secret string) error {
	status, err := requestClientCredentialsToken(clientID, secret)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return fmt.Errorf("the token endpoint accepted a secret that should not work")
	}
	return nil
}

// checkClientSecretCount asserts how many secrets the client has.
func checkClientSecretCount(clientID string, want int) error {
	c, err := testClient()
	if err != nil {
		return err
	}

	secrets, err := c.ListClientSecrets(context.Background(), clientID)
	if err != nil {
		return err
	}
	if len(secrets) != want {
		ids := make([]string, 0, len(secrets))
		for _, s := range secrets {
			ids = append(ids, s.ID)
		}
		return fmt.Errorf("expected %d client secret(s), got %d: %v", want, len(secrets), ids)
	}
	return nil
}

// checkClientSecretPresent asserts a specific secret exists.
func checkClientSecretPresent(clientID, secretID string) error {
	return checkClientSecret(clientID, secretID, true)
}

// checkClientSecretAbsent asserts a specific secret does not exist.
func checkClientSecretAbsent(clientID, secretID string) error {
	return checkClientSecret(clientID, secretID, false)
}

func checkClientSecret(clientID, secretID string, wantPresent bool) error {
	c, err := testClient()
	if err != nil {
		return err
	}

	secrets, err := c.ListClientSecrets(context.Background(), clientID)
	if err != nil {
		return err
	}
	for _, s := range secrets {
		if s.ID == secretID {
			if !wantPresent {
				return fmt.Errorf("secret %s is still present", secretID)
			}
			return nil
		}
	}
	if wantPresent {
		return fmt.Errorf("secret %s does not exist", secretID)
	}
	return nil
}

// stateExcludes is a state check that fails when the state, as Terraform
// serialises it with its sensitive values, contains the text anywhere.
type stateExcludes struct{ text string }

func (s stateExcludes) CheckState(_ context.Context, req statecheck.CheckStateRequest, resp *statecheck.CheckStateResponse) {
	encoded, err := json.Marshal(req.State)
	if err != nil {
		resp.Error = err
		return
	}
	if strings.Contains(string(encoded), s.text) {
		resp.Error = fmt.Errorf("the state contains %q", s.text)
	}
}

// stateIncludes is the opposite check, which shows that stateExcludes could
// find the text if it were there.
type stateIncludes struct{ text func() string }

func (s stateIncludes) CheckState(_ context.Context, req statecheck.CheckStateRequest, resp *statecheck.CheckStateResponse) {
	encoded, err := json.Marshal(req.State)
	if err != nil {
		resp.Error = err
		return
	}
	if text := s.text(); text == "" || !strings.Contains(string(encoded), text) {
		resp.Error = fmt.Errorf("the state does not contain %q", text)
	}
}

const testAccClientSecretClient = `
resource "pocketid_client" "test" {
  name          = "secret-test-client"
  callback_urls = ["https://example.com/callback"]
}
`

const testAccClientSecretVariables = `
variable "secret" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "secret_version" {
  type = number
}
`

func testAccClientSecretConfig_writeOnly(extra string) string {
	return testAccProviderConfig() + testAccClientSecretClient + testAccClientSecretVariables + fmt.Sprintf(`
resource "pocketid_client_secret" "test" {
  client_id         = pocketid_client.test.id
  secret_wo         = var.secret
  secret_wo_version = var.secret_version
%s}
`, extra)
}

func testAccClientSecretConfig_generated(extra string) string {
	return testAccProviderConfig() + testAccClientSecretClient + fmt.Sprintf(`
resource "pocketid_client_secret" "test" {
  client_id = pocketid_client.test.id
%s}
`, extra)
}

func testAccClientSecretVariables_for(secret string, version int) config.Variables {
	return config.Variables{
		"secret":         config.StringVariable(secret),
		"secret_version": config.IntegerVariable(version),
	}
}

func TestAccResourceClientSecret_writeOnly(t *testing.T) {
	const resourceName = "pocketid_client_secret.test"
	var clientID, secretID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); setServerAutoCreateClientSecret(t, false) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:          testAccClientSecretConfig_writeOnly(""),
				ConfigVariables: testAccClientSecretVariables_for(testSecretA, 1),
				ConfigStateChecks: []statecheck.StateCheck{
					// The value is in no attribute of the state.
					stateExcludes{text: testSecretA},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttrPair(resourceName, "client_id", "pocketid_client.test", "id"),
					resource.TestCheckResourceAttr(resourceName, "secret_wo_version", "1"),
					resource.TestCheckResourceAttr(resourceName, "prefix", testSecretA[:4]),
					resource.TestCheckResourceAttrSet(resourceName, "created_at"),
					resource.TestCheckNoResourceAttr(resourceName, "secret_wo"),
					resource.TestCheckNoResourceAttr(resourceName, "value"),
					resource.TestCheckNoResourceAttr(resourceName, "expires_at"),
					resource.TestCheckResourceAttrWith("pocketid_client.test", "id", func(v string) error {
						clientID = v
						return nil
					}),
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						secretID = v
						return nil
					}),
					func(*terraform.State) error {
						// The secret authenticates the client at the token endpoint.
						if err := checkClientSecretAccepted(clientID, testSecretA); err != nil {
							return err
						}
						return checkClientSecretCount(clientID, 1)
					},
				),
			},
			{
				// Nothing changes when the same configuration is applied again.
				Config:          testAccClientSecretConfig_writeOnly(""),
				ConfigVariables: testAccClientSecretVariables_for(testSecretA, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// A new value without a new version is not a change, because
				// Terraform cannot see a write-only argument change.
				Config:          testAccClientSecretConfig_writeOnly(""),
				ConfigVariables: testAccClientSecretVariables_for(testSecretC, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: func(*terraform.State) error {
					if err := checkClientSecretAccepted(clientID, testSecretA); err != nil {
						return err
					}
					if err := checkClientSecretRefused(clientID, testSecretC); err != nil {
						return err
					}
					return checkClientSecretPresent(clientID, secretID)
				},
			},
			{
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateIdFunc:       func(*terraform.State) (string, error) { return clientID + "/" + secretID, nil },
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secret_wo_version"},
				ConfigVariables:         testAccClientSecretVariables_for(testSecretA, 1),
			},
		},
	})
}

func TestAccResourceClientSecret_versionBumpReplaces(t *testing.T) {
	const resourceName = "pocketid_client_secret.test"
	var clientID, firstID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); setServerAutoCreateClientSecret(t, false) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:          testAccClientSecretConfig_writeOnly(""),
				ConfigVariables: testAccClientSecretVariables_for(testSecretA, 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("pocketid_client.test", "id", func(v string) error {
						clientID = v
						return nil
					}),
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						firstID = v
						return nil
					}),
					func(*terraform.State) error { return checkClientSecretAccepted(clientID, testSecretA) },
				),
			},
			{
				Config:          testAccClientSecretConfig_writeOnly(""),
				ConfigVariables: testAccClientSecretVariables_for(testSecretB, 2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectResourceAction("pocketid_client.test", plancheck.ResourceActionNoop),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					stateExcludes{text: testSecretA},
					stateExcludes{text: testSecretB},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "secret_wo_version", "2"),
					resource.TestCheckResourceAttr(resourceName, "prefix", testSecretB[:4]),
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						if v == firstID {
							return fmt.Errorf("the secret was not replaced: its id is still %s", v)
						}
						return nil
					}),
					func(*terraform.State) error {
						// The old secret is gone, not merely unused.
						if err := checkClientSecretAbsent(clientID, firstID); err != nil {
							return err
						}
						if err := checkClientSecretCount(clientID, 1); err != nil {
							return err
						}
						if err := checkClientSecretAccepted(clientID, testSecretB); err != nil {
							return err
						}
						return checkClientSecretRefused(clientID, testSecretA)
					},
				),
			},
		},
	})
}

func TestAccResourceClientSecret_generated(t *testing.T) {
	const resourceName = "pocketid_client_secret.test"
	var clientID string
	var value string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); setServerAutoCreateClientSecret(t, false) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccClientSecretConfig_generated(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttrSet(resourceName, "value"),
					resource.TestCheckResourceAttrSet(resourceName, "prefix"),
					resource.TestCheckNoResourceAttr(resourceName, "secret_wo"),
					resource.TestCheckNoResourceAttr(resourceName, "secret_wo_version"),
					resource.TestCheckResourceAttrWith("pocketid_client.test", "id", func(v string) error {
						clientID = v
						return nil
					}),
					resource.TestCheckResourceAttrWith(resourceName, "value", func(v string) error {
						value = v
						if len(v) < 16 {
							return fmt.Errorf("the generated secret is %d characters long", len(v))
						}
						return nil
					}),
					resource.TestCheckResourceAttrWith(resourceName, "prefix", func(v string) error {
						if v != value[:len(v)] || v == "" {
							return fmt.Errorf("prefix %q is not the start of the generated secret", v)
						}
						return nil
					}),
					func(*terraform.State) error { return checkClientSecretAccepted(clientID, value) },
				),
			},
			{
				// The value stays in the state, and a later refresh leaves it alone.
				Config: testAccClientSecretConfig_generated(""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					// The control for stateExcludes: the generated value is in the state.
					stateIncludes{text: func() string { return value }},
				},
				Check: resource.TestCheckResourceAttrWith(resourceName, "value", func(v string) error {
					if v != value {
						return fmt.Errorf("the value changed after a refresh")
					}
					return nil
				}),
			},
		},
	})
}

func TestAccResourceClientSecret_expiresAt(t *testing.T) {
	const resourceName = "pocketid_client_secret.test"
	var clientID, firstID string

	expires := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	expiresOffset := expires.In(time.FixedZone("", 2*3600)).Format(time.RFC3339)
	later := expires.Add(24 * time.Hour).Format(time.RFC3339)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); setServerAutoCreateClientSecret(t, false) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccClientSecretConfig_generated(fmt.Sprintf("  expires_at = %q\n", expiresOffset)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "expires_at", expiresOffset),
					resource.TestCheckResourceAttrWith("pocketid_client.test", "id", func(v string) error {
						clientID = v
						return nil
					}),
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						firstID = v
						return nil
					}),
				),
			},
			{
				Config: testAccClientSecretConfig_generated(fmt.Sprintf("  expires_at = %q\n", later)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "expires_at", later),
					func(*terraform.State) error { return checkClientSecretAbsent(clientID, firstID) },
				),
			},
			{
				// Removing the expiry replaces the secret with one that does not expire.
				Config: testAccClientSecretConfig_generated(""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: resource.TestCheckNoResourceAttr(resourceName, "expires_at"),
			},
		},
	})
}

func TestAccResourceClientSecret_validation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig() + testAccClientSecretClient + `
resource "pocketid_client_secret" "test" {
  client_id         = pocketid_client.test.id
  secret_wo_version = 1
}
`,
				ExpectError: regexp.MustCompile(`(?s)secret_wo_version.*secret_wo`),
			},
			{
				Config: testAccProviderConfig() + testAccClientSecretClient + `
resource "pocketid_client_secret" "test" {
  client_id = pocketid_client.test.id
  secret_wo = "too-short"
}
`,
				ExpectError: regexp.MustCompile(`(?s)secret_wo.*at least 16`),
			},
			{
				Config: testAccProviderConfig() + testAccClientSecretClient + `
resource "pocketid_client_secret" "test" {
  client_id  = pocketid_client.test.id
  expires_at = "tomorrow"
}
`,
				ExpectError: regexp.MustCompile(`(?s)expires_at`),
			},
			{
				Config: testAccProviderConfig() + `
resource "pocketid_client_secret" "test" {
  client_id = "https://client.example.com/metadata.json"
}
`,
				ExpectError: regexp.MustCompile(`(?s)Client ID must contain only`),
			},
		},
	})
}

func TestAccResourceClientSecret_publicClientRejected(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); setServerAutoCreateClientSecret(t, false) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig() + `
resource "pocketid_client" "test" {
  name          = "secret-test-public-client"
  callback_urls = ["https://example.com/callback"]
  is_public     = true
}

resource "pocketid_client_secret" "test" {
  client_id = pocketid_client.test.id
}
`,
				ExpectError: regexp.MustCompile(`(?s)Cannot\s+create a secret for a\s+public client`),
			},
		},
	})
}

func TestAccResourceClientSecret_importThenRemove(t *testing.T) {
	const resourceName = "pocketid_client_secret.test"
	var clientID, secretID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); setServerAutoCreateClientSecret(t, false) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig() + testAccClientSecretClient,
				Check: resource.TestCheckResourceAttrWith("pocketid_client.test", "id", func(v string) error {
					clientID = v
					return nil
				}),
			},
			{
				// A secret created outside Terraform, which Pocket ID tracks
				// only by id, is imported as "<client_id>/<secret_id>".
				PreConfig: func() {
					c, err := testClient()
					if err != nil {
						t.Fatal(err)
					}
					created, err := c.CreateClientSecret(context.Background(), clientID, &client.ClientSecretCreateRequest{Secret: testSecretA})
					if err != nil {
						t.Fatalf("creating the secret to import: %v", err)
					}
					secretID = created.ID
				},
				Config:             testAccClientSecretConfig_generated(""),
				ResourceName:       resourceName,
				ImportState:        true,
				ImportStatePersist: true,
				ImportStateIdFunc:  func(*terraform.State) (string, error) { return clientID + "/" + secretID, nil },
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					var attrs map[string]string
					for _, s := range states {
						if s.ID == secretID {
							attrs = s.Attributes
						}
					}
					if attrs == nil {
						return fmt.Errorf("the secret %s was not imported: %d resource(s) in the state", secretID, len(states))
					}
					if attrs["id"] != secretID || attrs["client_id"] != clientID {
						return fmt.Errorf("unexpected imported ids: %v", attrs)
					}
					if attrs["prefix"] != testSecretA[:4] {
						return fmt.Errorf("prefix %q, want %q", attrs["prefix"], testSecretA[:4])
					}
					if attrs["created_at"] == "" {
						return fmt.Errorf("created_at was not read")
					}
					if v, ok := attrs["value"]; ok && v != "" {
						return fmt.Errorf("an imported secret must not have a value")
					}
					return nil
				},
			},
			{
				// The imported secret is the one Terraform tracks: nothing changes.
				Config: testAccClientSecretConfig_generated(""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: func(*terraform.State) error { return checkClientSecretAccepted(clientID, testSecretA) },
			},
			{
				// Removing it from the configuration deletes it on the server.
				Config: testAccProviderConfig() + testAccClientSecretClient,
				Check: func(*terraform.State) error {
					if err := checkClientSecretAbsent(clientID, secretID); err != nil {
						return err
					}
					if err := checkClientSecretCount(clientID, 0); err != nil {
						return err
					}
					return checkClientSecretRefused(clientID, testSecretA)
				},
			},
		},
	})
}

func TestAccResourceClientSecret_deletedOutOfBand(t *testing.T) {
	const resourceName = "pocketid_client_secret.test"
	var clientID, firstID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); setServerAutoCreateClientSecret(t, false) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:          testAccClientSecretConfig_writeOnly(""),
				ConfigVariables: testAccClientSecretVariables_for(testSecretA, 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("pocketid_client.test", "id", func(v string) error {
						clientID = v
						return nil
					}),
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						firstID = v
						return nil
					}),
				),
			},
			{
				// Someone deletes the secret in the Pocket ID UI.
				PreConfig: func() {
					c, err := testClient()
					if err != nil {
						t.Fatal(err)
					}
					if err := c.DeleteClientSecret(context.Background(), clientID, firstID); err != nil {
						t.Fatalf("deleting the secret out of band: %v", err)
					}
				},
				Config:          testAccClientSecretConfig_writeOnly(""),
				ConfigVariables: testAccClientSecretVariables_for(testSecretA, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionCreate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						if v == firstID {
							return fmt.Errorf("the secret was not recreated")
						}
						return nil
					}),
					func(*terraform.State) error {
						if err := checkClientSecretCount(clientID, 1); err != nil {
							return err
						}
						// The secret is recreated with the configured value.
						return checkClientSecretAccepted(clientID, testSecretA)
					},
				),
			},
		},
	})
}

func TestAccResourceClientSecret_clientUpdateLeavesSecrets(t *testing.T) {
	const resourceName = "pocketid_client_secret.test"
	var clientID, secretID string

	renamed := strings.Replace(testAccClientSecretConfig_writeOnly(""), "secret-test-client", "secret-test-client-renamed", 1)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); setServerAutoCreateClientSecret(t, false) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:          testAccClientSecretConfig_writeOnly(""),
				ConfigVariables: testAccClientSecretVariables_for(testSecretA, 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("pocketid_client.test", "id", func(v string) error {
						clientID = v
						return nil
					}),
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						secretID = v
						return nil
					}),
				),
			},
			{
				Config:          renamed,
				ConfigVariables: testAccClientSecretVariables_for(testSecretA, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pocketid_client.test", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_client.test", "name", "secret-test-client-renamed"),
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						if v != secretID {
							return fmt.Errorf("the secret changed when the client was updated")
						}
						return nil
					}),
					func(*terraform.State) error {
						if err := checkClientSecretCount(clientID, 1); err != nil {
							return err
						}
						return checkClientSecretAccepted(clientID, testSecretA)
					},
				),
			},
		},
	})
}

func TestAccResourceClientSecret_clientWithoutSecretResource(t *testing.T) {
	var clientID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); setServerAutoCreateClientSecret(t, false) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig() + testAccClientSecretClient,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("pocketid_client.test", "client_secret"),
					resource.TestCheckNoResourceAttr("pocketid_client.test", "client_secret_id"),
					resource.TestCheckResourceAttrWith("pocketid_client.test", "id", func(v string) error {
						clientID = v
						return nil
					}),
					func(*terraform.State) error { return checkClientSecretCount(clientID, 0) },
				),
			},
		},
	})
}

// TestAccResourceClientSecret_untrackedAutoCreatedSecret covers a Pocket ID
// that creates a secret for every new confidential client, which is its
// default. No resource tracks that secret, and Terraform leaves it alone.
func TestAccResourceClientSecret_untrackedAutoCreatedSecret(t *testing.T) {
	const resourceName = "pocketid_client_secret.test"
	var clientID, trackedID, autoID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t); setServerAutoCreateClientSecret(t, true) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Only the client: Pocket ID gives it a secret that nothing tracks.
				Config: testAccProviderConfig() + testAccClientSecretClient,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("pocketid_client.test", "id", func(v string) error {
						clientID = v
						return nil
					}),
					func(*terraform.State) error {
						if err := checkClientSecretCount(clientID, 1); err != nil {
							return err
						}
						c, err := testClient()
						if err != nil {
							return err
						}
						secrets, err := c.ListClientSecrets(context.Background(), clientID)
						if err != nil {
							return err
						}
						autoID = secrets[0].ID
						return nil
					},
				),
			},
			{
				Config:          testAccClientSecretConfig_writeOnly(""),
				ConfigVariables: testAccClientSecretVariables_for(testSecretA, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("pocketid_client.test", plancheck.ResourceActionNoop),
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionCreate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						trackedID = v
						if v == autoID {
							return fmt.Errorf("the tracked secret is the auto-created one")
						}
						return nil
					}),
					func(*terraform.State) error {
						// The client has the auto-created secret and the tracked one.
						if err := checkClientSecretCount(clientID, 2); err != nil {
							return err
						}
						return checkClientSecretPresent(clientID, autoID)
					},
				),
			},
			{
				// Nothing changes, and a refresh keeps both secrets.
				Config:          testAccClientSecretConfig_writeOnly(""),
				ConfigVariables: testAccClientSecretVariables_for(testSecretA, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Removing the secret resource deletes only the secret it tracks.
				Config: testAccProviderConfig() + testAccClientSecretClient,
				Check: func(*terraform.State) error {
					if err := checkClientSecretAbsent(clientID, trackedID); err != nil {
						return err
					}
					if err := checkClientSecretPresent(clientID, autoID); err != nil {
						return err
					}
					return checkClientSecretCount(clientID, 1)
				},
			},
		},
	})
}
