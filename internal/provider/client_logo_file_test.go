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

// testAccResourceClientConfig_logoFiles renders a client with the given logo
// files; an empty path leaves that attribute out of the configuration.
func testAccResourceClientConfig_logoFiles(name, logo, darkLogo string) string {
	var logos string
	if logo != "" {
		logos += fmt.Sprintf("  logo = {\n    source = %q\n  }\n", logo)
	}
	if darkLogo != "" {
		logos += fmt.Sprintf("  dark_logo = {\n    source = %q\n  }\n", darkLogo)
	}
	return fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = %[1]q
  callback_urls = ["https://example.com/callback"]
%[2]s}
`, name, logos)
}

// TestAccResourceClient_logoFile walks logos uploaded from files through their
// lifecycle: set on create, import, change of the file, and removal.
func TestAccResourceClient_logoFile(t *testing.T) {
	resourceName := "pocketid_client.test"
	dir := t.TempDir()
	logo := filepath.Join(dir, "logo.png")
	darkLogo := filepath.Join(dir, "logo-dark.svg")
	writeTestPNG(t, logo, color.RGBA{R: 255, A: 255})
	// Logos may be SVG, the format a logo is most often kept in.
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><rect width="8" height="8" fill="#00ff00"/></svg>`
	if err := os.WriteFile(darkLogo, []byte(svg), 0o600); err != nil {
		t.Fatal(err)
	}

	var clientID string
	var firstLogo []byte

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceClientConfig_logoFiles("logo-file", logo, darkLogo),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
						clientID = v
						return nil
					}),
					resource.TestCheckResourceAttr(resourceName, "logo.source", logo),
					resource.TestCheckResourceAttrSet(resourceName, "logo.sha256"),
					resource.TestCheckResourceAttr(resourceName, "dark_logo.source", darkLogo),
					resource.TestCheckResourceAttrSet(resourceName, "dark_logo.sha256"),
					resource.TestCheckResourceAttr(resourceName, "has_logo", "true"),
					resource.TestCheckResourceAttr(resourceName, "has_dark_logo", "true"),
					func(*terraform.State) error {
						if err := checkClientLogoFlags(clientID, true, true); err != nil {
							return err
						}
						light, err := getClientLogo(clientID, true)
						if err != nil {
							return err
						}
						dark, err := getClientLogo(clientID, false)
						if err != nil {
							return err
						}
						if bytes.Equal(light, dark) {
							return fmt.Errorf("the light and dark logos are the same image")
						}
						firstLogo = light
						return nil
					},
				),
			},
			{
				// Pocket ID does not return the files, so an import has none.
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"logo", "dark_logo"},
			},
			{
				PreConfig: func() {
					writeTestPNG(t, logo, color.RGBA{B: 255, A: 255})
				},
				Config: testAccResourceClientConfig_logoFiles("logo-file", logo, darkLogo),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
					},
				},
				Check: func(*terraform.State) error {
					light, err := getClientLogo(clientID, true)
					if err != nil {
						return err
					}
					if bytes.Equal(light, firstLogo) {
						return fmt.Errorf("logo was not replaced after the file changed")
					}
					return nil
				},
			},
			{
				Config: testAccResourceClientConfig_logoFiles("logo-file", logo, darkLogo),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: testAccResourceClientConfig_logoFiles("logo-file", logo, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceName, "dark_logo"),
					resource.TestCheckResourceAttr(resourceName, "has_logo", "true"),
					resource.TestCheckResourceAttr(resourceName, "has_dark_logo", "false"),
					func(*terraform.State) error {
						return checkClientLogoFlags(clientID, true, false)
					},
				),
			},
			{
				Config: testAccResourceClientConfig_logoFiles("logo-file", "", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceName, "logo"),
					resource.TestCheckResourceAttr(resourceName, "has_logo", "false"),
					func(*terraform.State) error {
						return checkClientLogoFlags(clientID, false, false)
					},
				),
			},
		},
	})
}

// TestAccResourceClient_logoFileDeletedOutOfBand checks that a logo file
// deleted in the UI is uploaded again on the next apply.
func TestAccResourceClient_logoFileDeletedOutOfBand(t *testing.T) {
	resourceName := "pocketid_client.test"
	logo := filepath.Join(t.TempDir(), "logo.png")
	writeTestPNG(t, logo, color.RGBA{R: 255, A: 255})

	var clientID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccResourceClientConfig_logoFiles("logo-file-oob", logo, ""),
				Check: resource.TestCheckResourceAttrWith(resourceName, "id", func(v string) error {
					clientID = v
					return nil
				}),
			},
			{
				PreConfig: func() {
					c, err := testClient()
					if err != nil {
						t.Fatal(err)
					}
					if err := c.DeleteClientLogo(clientID, true); err != nil {
						t.Fatalf("deleting logo: %v", err)
					}
				},
				Config: testAccResourceClientConfig_logoFiles("logo-file-oob", logo, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "has_logo", "true"),
					func(*terraform.State) error {
						return checkClientLogoFlags(clientID, true, false)
					},
				),
			},
		},
	})
}

// TestAccResourceClient_logoFileRejected checks that a file Pocket ID refuses (here
// one with an unsupported extension)
// fails the apply without leaving the new client behind.
func TestAccResourceClient_logoFileRejected(t *testing.T) {
	notAnImage := filepath.Join(t.TempDir(), "logo.txt")
	if err := os.WriteFile(notAnImage, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccResourceClientConfig_logoFiles("rejected-logo-file", notAnImage, ""),
				ExpectError: regexp.MustCompile(`Pocket ID rejected "logo.txt"`),
			},
			{
				PreConfig: func() {
					if err := checkNoClientNamed("rejected-logo-file"); err != nil {
						t.Fatal(err)
					}
				},
				Config:             testAccResourceClientConfig_logoFiles("rejected-logo-file-check", "", ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccResourceClient_logoFileConflictsWithURL checks that a logo cannot
// come from both a file and a URL.
func TestAccResourceClient_logoFileConflictsWithURL(t *testing.T) {
	logo := filepath.Join(t.TempDir(), "logo.png")
	writeTestPNG(t, logo, color.RGBA{R: 255, A: 255})

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "pocketid_client" "test" {
  name          = "logo-conflict"
  callback_urls = ["https://example.com/callback"]
  logo_url      = %q
  logo = {
    source = %q
  }
}
`, testLogoURL, logo),
				ExpectError: regexp.MustCompile(`(?s)logo_url.*logo|logo.*logo_url`),
			},
		},
	})
}
