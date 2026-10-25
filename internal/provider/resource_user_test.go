//go:build acc
// +build acc

package provider_test

import (
	"bytes"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccResourceUser_basic(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccResourceUserConfig_basic("test-user", "test@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "test-user"),
					resource.TestCheckResourceAttr(resourceName, "email", "test@example.com"),
					resource.TestCheckResourceAttr(resourceName, "first_name", "Test"),
					resource.TestCheckResourceAttr(resourceName, "last_name", "User"),
					resource.TestCheckResourceAttr(resourceName, "is_admin", "false"),
					resource.TestCheckResourceAttr(resourceName, "disabled", "false"),
					resource.TestCheckResourceAttrSet(resourceName, "id"),
				),
			},
			// ImportState testing
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Update and Read testing
			{
				Config: testAccResourceUserConfig_basic("test-user", "updated@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "email", "updated@example.com"),
				),
			},
		},
	})
}

func TestAccResourceUser_customClaims(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create with custom claims
			{
				Config: testAccResourceUserConfig_customClaims("test-claims-user", "claims@example.com", map[string]string{
					"department": "engineering",
					"level":      "senior",
				}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "test-claims-user"),
					resource.TestCheckResourceAttr(resourceName, "custom_claims.%", "2"),
					resource.TestCheckResourceAttr(resourceName, "custom_claims.department", "engineering"),
					resource.TestCheckResourceAttr(resourceName, "custom_claims.level", "senior"),
				),
			},
			// ImportState testing
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Update claims (change value, remove one, add one)
			{
				Config: testAccResourceUserConfig_customClaims("test-claims-user", "claims@example.com", map[string]string{
					"department": "platform",
					"location":   "remote",
				}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "custom_claims.%", "2"),
					resource.TestCheckResourceAttr(resourceName, "custom_claims.department", "platform"),
					resource.TestCheckResourceAttr(resourceName, "custom_claims.location", "remote"),
					resource.TestCheckNoResourceAttr(resourceName, "custom_claims.level"),
				),
			},
			// Clear claims
			{
				Config: testAccResourceUserConfig_basic("test-claims-user", "claims@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceName, "custom_claims.%"),
				),
			},
		},
	})
}

func TestAccResourceUser_displayNameDefault(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create user without display_name - should default to "Test User"
			{
				Config: testAccResourceUserConfig_displayNameDefault("test-display-name", "testdisplayname@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "test-display-name"),
					resource.TestCheckResourceAttr(resourceName, "first_name", "Test"),
					resource.TestCheckResourceAttr(resourceName, "last_name", "User"),
					resource.TestCheckResourceAttr(resourceName, "display_name", "Test User"),
				),
			},
		},
	})
}

func TestAccResourceUser_withGroups(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create user with multiple groups
			{
				Config: testAccResourceUserConfig_withGroups("test-user", "test@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "test-user"),
					resource.TestCheckResourceAttr(resourceName, "email", "test@example.com"),
					resource.TestCheckResourceAttr(resourceName, "groups.#", "2"),
					testAccCheckUserGroupsSet(resourceName),
				),
			},
			// Verify no changes on re-apply (groups ordering should not matter)
			{
				Config:   testAccResourceUserConfig_withGroups("test-user", "test@example.com"),
				PlanOnly: true,
			},
			// Update groups
			{
				Config: testAccResourceUserConfig_withSingleGroup("test-user", "test@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "groups.#", "1"),
				),
			},
			// Remove all groups
			{
				Config: testAccResourceUserConfig_basic("test-user", "test@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "groups.#", "0"),
				),
			},
		},
	})
}

func TestAccResourceUser_disabled(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create user directly as disabled (supported by the API).
			{
				Config: testAccResourceUserConfig_disabled("disabled-user", "disabled@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "disabled-user"),
					resource.TestCheckResourceAttr(resourceName, "disabled", "true"),
				),
			},
			// Enable user again
			{
				Config: testAccResourceUserConfig_basic("disabled-user", "disabled@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "disabled", "false"),
				),
			},
		},
	})
}

// testAccCheckUserGroupsSet verifies that groups are stored as a set (unordered)
func testAccCheckUserGroupsSet(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}

		// Get all group attributes
		groupCount := 0
		groups := make(map[string]bool)
		groupsRegex := regexp.MustCompile(`^groups\.\d+$`)
		for key, value := range rs.Primary.Attributes {
			if groupsRegex.MatchString(key) {
				groups[value] = true
				groupCount++
			}
		}

		// Verify we have the expected number of unique groups
		if len(groups) != groupCount {
			return fmt.Errorf("duplicate groups found in state")
		}

		return nil
	}
}

func testAccResourceUserConfig_basic(username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
}
`, username, email)
}

func testAccResourceUserConfig_customClaims(username, email string, claims map[string]string) string {
	var claimLines string
	for k, v := range claims {
		claimLines += fmt.Sprintf("\n    %q = %q", k, v)
	}
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
  custom_claims = {%[3]s
  }
}
`, username, email, claimLines)
}

func testAccResourceUserConfig_displayNameDefault(username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
}
`, username, email)
}

func testAccResourceUserConfig_withGroups(username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_group" "test1" {
  name          = "test-group-1"
  friendly_name = "Test group 1"
}

resource "pocketid_group" "test2" {
  name          = "test-group-2"
  friendly_name = "Test group 2"
}

resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"

  groups = [
    pocketid_group.test2.id,
    pocketid_group.test1.id,
  ]
}
`, username, email)
}

func testAccResourceUserConfig_withSingleGroup(username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_group" "test1" {
  name          = "test-group-1"
  friendly_name = "Test group 1"
}

resource "pocketid_group" "test2" {
  name          = "test-group-2"
  friendly_name = "Test group 2"
}

resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"

  groups = [
    pocketid_group.test1.id,
  ]
}
`, username, email)
}

func testAccResourceUserConfig_disabled(username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
  disabled   = true
}
`, username, email)
}

func TestAccResourceUser_adminUser(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create admin user
			{
				Config: testAccResourceUserConfig_admin("admin-user", "admin@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "admin-user"),
					resource.TestCheckResourceAttr(resourceName, "is_admin", "true"),
				),
			},
			// Remove admin privileges
			{
				Config: testAccResourceUserConfig_basic("admin-user", "admin@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "is_admin", "false"),
				),
			},
		},
	})
}

func TestAccResourceUser_withLocale(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create user with locale
			{
				Config: testAccResourceUserConfig_withLocale("locale-user", "locale@example.com", "fr-FR"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "locale-user"),
					resource.TestCheckResourceAttr(resourceName, "locale", "fr-FR"),
				),
			},
			// Update locale
			{
				Config: testAccResourceUserConfig_withLocale("locale-user", "locale@example.com", "en-US"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "locale", "en-US"),
				),
			},
		},
	})
}

func TestAccResourceUser_invalidEmail(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccResourceUserConfig_basic("test-user", "invalid-email"),
				ExpectError: regexp.MustCompile("Email must be a valid email address"),
			},
		},
	})
}

func TestAccResourceUser_duplicateUsername(t *testing.T) {
	username := "duplicate-user"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create first user
			{
				Config: testAccResourceUserConfig_duplicate(username, "user1@example.com", "first"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("pocketid_user.first", "username", username),
				),
			},
			// Attempt to create duplicate user
			{
				Config:      testAccResourceUserConfig_duplicate(username, "user2@example.com", "second"),
				ExpectError: regexp.MustCompile("Username is already in use"),
			},
		},
	})
}

func TestAccResourceUser_updateImmutableField(t *testing.T) {
	resourceName := "pocketid_user.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create user
			{
				Config: testAccResourceUserConfig_basic("original-username", "test@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "original-username"),
				),
			},
			// Attempt to update username (should recreate resource)
			{
				Config: testAccResourceUserConfig_basic("new-username", "test@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "new-username"),
				),
			},
		},
	})
}

func testAccResourceUserConfig_admin(username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
  is_admin   = true
}
`, username, email)
}

func testAccResourceUserConfig_withLocale(username, email, locale string) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
  locale     = %[3]q
}
`, username, email, locale)
}

func testAccResourceUserConfig_duplicate(username, email, label string) string {
	if label == "first" {
		return fmt.Sprintf(`
resource "pocketid_user" "first" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User"
}
`, username, email)
	}
	return fmt.Sprintf(`
resource "pocketid_user" "first" {
  username   = %[1]q
  email      = "user1@example.com"
  first_name = "Test"
  last_name  = "User"
}

resource "pocketid_user" "second" {
  username   = %[1]q
  email      = %[2]q
  first_name = "Test"
  last_name  = "User2"
}
`, username, email)
}

func TestAccResourceUser_emailVerified(t *testing.T) {
	resourceName := "pocketid_user.test"
	username := "emailverified-user"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceUserConfig_emailVerified(username, "emailverified@example.com", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "email_verified", "true"),
				),
			},
			{
				Config: testAccResourceUserConfig_emailVerified(username, "emailverified@example.com", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "email_verified", "false"),
				),
			},
		},
	})
}

func testAccResourceUserConfig_emailVerified(username, email string, verified bool) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username       = %[1]q
  email          = %[2]q
  first_name     = "Test"
  last_name      = "User"
  email_verified = %[3]t
}
`, username, email, verified)
}

func TestAccResourceUser_customID(t *testing.T) {
	resourceName := "pocketid_user.test"
	customID := "11111111-2222-3333-4444-555555555555"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create with an explicit UUID and verify it is honored.
			{
				Config: testAccResourceUserConfig_customID(customID, "custom-id-user", "customid@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", customID),
					resource.TestCheckResourceAttr(resourceName, "username", "custom-id-user"),
				),
			},
			// Re-applying the same config should not produce a diff.
			{
				Config:   testAccResourceUserConfig_customID(customID, "custom-id-user", "customid@example.com"),
				PlanOnly: true,
			},
		},
	})
}

func TestAccResourceUser_idChangeRecreatesResource(t *testing.T) {
	resourceName := "pocketid_user.test"
	idA := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	idB := "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceUserConfig_customID(idA, "recreate-user", "recreate@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", idA),
				),
			},
			// Changing id must force replacement (destroy + create), not an
			// in-place update.
			{
				Config: testAccResourceUserConfig_customID(idB, "recreate-user", "recreate@example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionReplace),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", idB),
				),
			},
		},
	})
}

func testAccResourceUserConfig_customID(id, username, email string) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  id         = %[1]q
  username   = %[2]q
  email      = %[3]q
  first_name = "Test"
  last_name  = "User"
}
`, id, username, email)
}

func testAccResourceUserConfig_profilePicture(username, source string) string {
	var picture string
	if source != "" {
		picture = fmt.Sprintf("\n  profile_picture = {\n    source = %q\n  }\n", source)
	}
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username   = %[1]q
  email      = "%[1]s@example.com"
  first_name = "Test"
  last_name  = "User"
%[2]s}
`, username, picture)
}

// TestAccResourceUser_profilePicture walks a managed profile picture through
// its lifecycle: upload, import, re-upload after the file changes, and reset.
func TestAccResourceUser_profilePicture(t *testing.T) {
	resourceName := "pocketid_user.test"
	source := filepath.Join(t.TempDir(), "avatar.png")
	writeTestPNG(t, source, color.RGBA{R: 255, A: 255})
	var userID string
	var defaultPicture, firstPicture []byte

	servedPicture := func(want string, check func([]byte) error) resource.TestCheckFunc {
		return func(*terraform.State) error {
			got, err := getUserProfilePicture(userID)
			if err != nil {
				return err
			}
			if err := check(got); err != nil {
				return fmt.Errorf("served picture: %s: %w", want, err)
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceUserConfig_profilePicture("picture-user", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						userID = v
						return nil
					}),
					servedPicture("default", func(got []byte) error {
						defaultPicture = got
						return nil
					}),
				),
			},
			{
				Config: testAccResourceUserConfig_profilePicture("picture-user", source),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "profile_picture.source", source),
					resource.TestCheckResourceAttrSet(resourceName, "profile_picture.sha256"),
					servedPicture("custom", func(got []byte) error {
						if bytes.Equal(got, defaultPicture) {
							return fmt.Errorf("still the default")
						}
						firstPicture = got
						return nil
					}),
				),
			},
			{
				// Pocket ID does not report a custom picture, so an import
				// leaves it unmanaged.
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"profile_picture"},
			},
			{
				PreConfig: func() {
					writeTestPNG(t, source, color.RGBA{B: 255, A: 255})
				},
				Config: testAccResourceUserConfig_profilePicture("picture-user", source),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
					},
				},
				Check: servedPicture("replaced", func(got []byte) error {
					if bytes.Equal(got, firstPicture) {
						return fmt.Errorf("still the first picture")
					}
					return nil
				}),
			},
			{
				Config: testAccResourceUserConfig_profilePicture("picture-user", source),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: testAccResourceUserConfig_profilePicture("picture-user", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceName, "profile_picture.source"),
					servedPicture("default again", func(got []byte) error {
						if !bytes.Equal(got, defaultPicture) {
							return fmt.Errorf("not reset to the default")
						}
						return nil
					}),
				),
			},
		},
	})
}

// TestAccResourceUser_unmanagedProfilePicturePreserved checks that a picture
// uploaded outside Terraform survives an update of a user with no
// profile_picture, and causes no diff.
func TestAccResourceUser_unmanagedProfilePicturePreserved(t *testing.T) {
	resourceName := "pocketid_user.test"
	source := filepath.Join(t.TempDir(), "manual.png")
	writeTestPNG(t, source, color.RGBA{G: 255, A: 255})
	var userID string
	var defaultPicture []byte

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceUserConfig_basic("manual-picture", "manual-picture@example.com"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						userID = v
						return nil
					}),
					func(*terraform.State) error {
						var err error
						defaultPicture, err = getUserProfilePicture(userID)
						return err
					},
				),
			},
			{
				PreConfig: func() {
					if err := uploadUserProfilePicture(userID, source); err != nil {
						t.Fatalf("uploading profile picture: %v", err)
					}
				},
				Config: testAccResourceUserConfig_basic("manual-picture", "manual-picture-changed@example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
					},
				},
				Check: func(*terraform.State) error {
					got, err := getUserProfilePicture(userID)
					if err != nil {
						return err
					}
					if bytes.Equal(got, defaultPicture) {
						return fmt.Errorf("the picture uploaded outside Terraform was reset")
					}
					return nil
				},
			},
			{
				Config: testAccResourceUserConfig_basic("manual-picture", "manual-picture-changed@example.com"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccResourceUser_profilePictureRejected checks a missing file is caught
// at plan time, and a file Pocket ID rejects fails the apply without leaving
// the new user behind.
func TestAccResourceUser_profilePictureRejected(t *testing.T) {
	dir := t.TempDir()
	notAnImage := filepath.Join(dir, "avatar.png")
	if err := os.WriteFile(notAnImage, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccResourceUserConfig_profilePicture("missing-picture", filepath.Join(dir, "missing.png")),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`could not read file`),
			},
			{
				Config:      testAccResourceUserConfig_profilePicture("rejected-picture", notAnImage),
				ExpectError: regexp.MustCompile(`Pocket ID rejected "avatar.png"`),
			},
			{
				PreConfig: func() {
					if err := checkNoUserNamed("rejected-picture"); err != nil {
						t.Fatal(err)
					}
				},
				Config:      testAccResourceUserConfig_profilePicture("missing-picture", filepath.Join(dir, "missing.png")),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`could not read file`),
			},
		},
	})
}

// setRequireUserEmail sets Pocket ID's "require user email" setting for the
// rest of the test and restores the previous value afterwards.
func setRequireUserEmail(t *testing.T, value string) {
	t.Helper()

	c, err := testClient()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := c.GetApplicationConfig()
	if err != nil {
		t.Fatal(err)
	}

	previous := cfg.RequireUserEmail
	set := func(v string) error {
		current, err := c.GetApplicationConfig()
		if err != nil {
			return err
		}
		current.RequireUserEmail = v
		_, err = c.UpdateApplicationConfig(current)
		return err
	}
	if err := set(value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := set(previous); err != nil {
			t.Errorf("restoring require_user_email: %v", err)
		}
	})
}

func testAccResourceUserConfig_withoutEmail(username, displayName, extra string) string {
	return fmt.Sprintf(`
resource "pocketid_user" "test" {
  username     = %[1]q
  display_name = %[2]q
%[3]s}
`, username, displayName, extra)
}

// TestAccResourceUser_withoutEmail checks a user with only a username and a
// display name, which Pocket ID accepts once it no longer requires emails.
func TestAccResourceUser_withoutEmail(t *testing.T) {
	resourceName := "pocketid_user.test"

	setRequireUserEmail(t, "false")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceUserConfig_withoutEmail("no-email-user", "No Email", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "username", "no-email-user"),
					resource.TestCheckResourceAttr(resourceName, "display_name", "No Email"),
					resource.TestCheckNoResourceAttr(resourceName, "email"),
					resource.TestCheckNoResourceAttr(resourceName, "first_name"),
					resource.TestCheckNoResourceAttr(resourceName, "last_name"),
					resource.TestCheckResourceAttr(resourceName, "is_admin", "false"),
				),
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Make the user an administrator and change the display name.
			{
				Config: testAccResourceUserConfig_withoutEmail("no-email-user", "No Email Renamed", "  is_admin     = true\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "display_name", "No Email Renamed"),
					resource.TestCheckResourceAttr(resourceName, "is_admin", "true"),
					resource.TestCheckNoResourceAttr(resourceName, "email"),
				),
			},
			// Add an email, then remove it again.
			{
				Config: testAccResourceUserConfig_withoutEmail("no-email-user", "No Email Renamed", "  email        = \"no-email@example.com\"\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "email", "no-email@example.com"),
				),
			},
			{
				Config: testAccResourceUserConfig_withoutEmail("no-email-user", "No Email Renamed", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceName, "email"),
				),
			},
		},
	})
}

// TestAccResourceUser_withoutEmailTwoUsers checks that two users without an
// email do not collide.
func TestAccResourceUser_withoutEmailTwoUsers(t *testing.T) {
	setRequireUserEmail(t, "false")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "pocketid_user" "a" {
  username     = "no-email-a"
  display_name = "No Email A"
}

resource "pocketid_user" "b" {
  username     = "no-email-b"
  display_name = "No Email B"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("pocketid_user.a", "email"),
					resource.TestCheckNoResourceAttr("pocketid_user.b", "email"),
				),
			},
		},
	})
}

// TestAccResourceUser_emailRequired checks that Pocket ID's refusal of a user
// without an email, while it requires one, is reported.
func TestAccResourceUser_emailRequired(t *testing.T) {
	setRequireUserEmail(t, "true")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccResourceUserConfig_withoutEmail("email-required", "Email Required", ""),
				ExpectError: regexp.MustCompile(`Email is required`),
			},
		},
	})
}
