package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/jacoblamarche/terraform-provider-viya/internal/viya"
	"github.com/jacoblamarche/terraform-provider-viya/internal/viyamock"
)

const (
	testAccConfigurationAddress = "viya_configuration.test"

	definitionCompute = "sas.compute.server"
	definitionLDAP    = "sas.identities.providers.ldap.connection"
	definitionLogging = "logging.level"
)

// requireMock skips a test that relies on the instances seeded in the fake
// deployment. Such a test adopts and rewrites real settings, like the LDAP
// connection, which must never happen to a real deployment.
func requireMock(t *testing.T, env testAccEnv) *viyamock.Server {
	t.Helper()
	if env.Mock == nil {
		t.Skip("this test rewrites seeded configuration instances and only runs against the mock")
	}
	return env.Mock
}

// testAccLoggerName returns a logger name that no real logger uses, so that
// a logging.level instance created for it is harmless on a real deployment.
func testAccLoggerName() string {
	return "tfacc." + acctest.RandStringFromCharSet(10, acctest.CharSetAlpha)
}

func testAccLoggingConfig(env testAccEnv, logger, level string) string {
	return env.ProviderConfig + fmt.Sprintf(`
resource "viya_configuration" "test" {
  definition = %q
  services   = ["identities"]
  name       = %q

  properties = {
    level = %q
  }
}
`, definitionLogging, logger, level)
}

// testAccConfigurationID returns the ID that Terraform recorded.
func testAccConfigurationID(state *terraform.State) (string, error) {
	rs, ok := state.RootModule().Resources[testAccConfigurationAddress]
	if !ok {
		return "", fmt.Errorf("%s is not in the state", testAccConfigurationAddress)
	}
	return rs.Primary.ID, nil
}

// testAccCheckConfigurationProperties verifies properties as Viya stores
// them, through the API.
func testAccCheckConfigurationProperties(env testAccEnv, want map[string]any) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		id, err := testAccConfigurationID(state)
		if err != nil {
			return err
		}
		configuration, err := env.Client.GetConfiguration(context.Background(), id)
		if err != nil {
			return fmt.Errorf("reading configuration %q from Viya: %w", id, err)
		}
		for name, wantValue := range want {
			if got := configuration.Properties[name]; got != wantValue {
				return fmt.Errorf("property %q in Viya is %#v, want %#v", name, got, wantValue)
			}
		}
		if configuration.Metadata.IsDefault {
			return errors.New("the configuration is still flagged as default: a service would overwrite it at startup")
		}
		return nil
	}
}

// testAccCheckConfigurationsDeleted verifies that the configuration
// instances that Terraform created are gone.
func testAccCheckConfigurationsDeleted(env testAccEnv) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		for _, rs := range state.RootModule().Resources {
			if rs.Type != "viya_configuration" {
				continue
			}
			_, err := env.Client.GetConfiguration(context.Background(), rs.Primary.ID)
			if err == nil {
				return fmt.Errorf("configuration %q still exists", rs.Primary.ID)
			}
			if !viya.IsNotFound(err) {
				return fmt.Errorf("checking that configuration %q is gone: %w", rs.Primary.ID, err)
			}
		}
		return nil
	}
}

// seeded returns the instance of a definition that the fake starts with,
// selected by name when the definition has several.
func seeded(t *testing.T, mock *viyamock.Server, definition, name string) viyamock.Configuration {
	t.Helper()
	for _, configuration := range mock.FindConfigurations(definition) {
		if name == "" || configuration.Properties["name"] == name {
			return configuration
		}
	}
	t.Fatalf("the mock has no instance of %q named %q", definition, name)
	return viyamock.Configuration{}
}

func TestAccConfiguration_createsNewInstance(t *testing.T) {
	env := newTestAccEnv(t)
	logger := testAccLoggerName()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckConfigurationsDeleted(env),
		Steps: []resource.TestStep{
			{
				Config: testAccLoggingConfig(env, logger, "DEBUG"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(testAccConfigurationAddress, "id"),
					resource.TestCheckResourceAttr(testAccConfigurationAddress, "definition", definitionLogging),
					resource.TestCheckResourceAttr(testAccConfigurationAddress, "name", logger),
					resource.TestCheckResourceAttr(testAccConfigurationAddress, "services.#", "1"),
					resource.TestCheckResourceAttr(testAccConfigurationAddress, "adopted", "false"),
					testAccCheckConfigurationProperties(env, map[string]any{"name": logger, "level": "DEBUG"}),
				),
			},
			{
				// Applying the same configuration again changes nothing.
				Config: testAccLoggingConfig(env, logger, "DEBUG"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: testAccLoggingConfig(env, logger, "INFO"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(testAccConfigurationAddress, plancheck.ResourceActionUpdate),
					},
				},
				Check: testAccCheckConfigurationProperties(env, map[string]any{"level": "INFO"}),
			},
			{
				ResourceName:      testAccConfigurationAddress,
				ImportState:       true,
				ImportStateVerify: true,
				// An imported instance counts as adopted, whoever created it.
				ImportStateVerifyIgnore: []string{"adopted"},
			},
		},
	})
}

func TestAccConfiguration_deletedOutsideTerraform(t *testing.T) {
	env := newTestAccEnv(t)
	logger := testAccLoggerName()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckConfigurationsDeleted(env),
		Steps: []resource.TestStep{
			{
				Config: testAccLoggingConfig(env, logger, "DEBUG"),
				Check: func(state *terraform.State) error {
					id, err := testAccConfigurationID(state)
					if err != nil {
						return err
					}
					return env.Client.DeleteConfiguration(context.Background(), id)
				},
				ExpectNonEmptyPlan: true,
			},
			{
				Config: testAccLoggingConfig(env, logger, "DEBUG"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(testAccConfigurationAddress, plancheck.ResourceActionCreate),
					},
				},
				Check: testAccCheckConfigurationProperties(env, map[string]any{"level": "DEBUG"}),
			},
		},
	})
}

func TestAccConfiguration_unknownDefinition(t *testing.T) {
	env := newTestAccEnv(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: env.ProviderConfig + `
resource "viya_configuration" "test" {
  definition = "tfacc.no.such.definition"
  services   = ["identities"]
  properties = { enabled = true }
}
`,
			ExpectError: regexp.MustCompile(`Unknown configuration definition`),
		}},
	})
}

func TestAccConfiguration_adoptsNamedInstance(t *testing.T) {
	env := newTestAccEnv(t)
	mock := requireMock(t, env)

	autoexec := seeded(t, mock, definitionCompute, "autoexec_code")
	options := seeded(t, mock, definitionCompute, "configuration_options")

	config := func(contents string) string {
		return env.ProviderConfig + fmt.Sprintf(`
resource "viya_configuration" "test" {
  definition = %q
  services   = ["compute"]
  name       = "autoexec_code"

  properties = {
    contents = %q
  }
}
`, definitionCompute, contents)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		// Destroying an adopted instance leaves it in place, with the values
		// that Terraform last wrote.
		CheckDestroy: func(*terraform.State) error {
			left, ok := mock.Configuration(autoexec.ID)
			if !ok {
				return errors.New("the adopted instance was deleted by the destroy")
			}
			if left.Properties["contents"] != "options fullstimer;" {
				return fmt.Errorf("the adopted instance has contents %q after the destroy", left.Properties["contents"])
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config("options nofullstimer;"),
				Check: resource.ComposeAggregateTestCheckFunc(
					// The existing instance was taken over, not duplicated.
					resource.TestCheckResourceAttr(testAccConfigurationAddress, "id", autoexec.ID),
					resource.TestCheckResourceAttr(testAccConfigurationAddress, "adopted", "true"),
					testAccCheckConfigurationProperties(env, map[string]any{
						"name": "autoexec_code", "contents": "options nofullstimer;",
					}),
					func(*terraform.State) error {
						if got := len(mock.FindConfigurations(definitionCompute)); got != 3 {
							return fmt.Errorf("the mock has %d compute instances, want the 3 it started with", got)
						}
						sibling, _ := mock.Configuration(options.ID)
						if sibling.Properties["contents"] != "" || !sibling.IsDefault {
							return fmt.Errorf("the sibling instance was modified: %+v", sibling)
						}
						return nil
					},
				),
			},
			{
				Config: config("options fullstimer;"),
				Check:  testAccCheckConfigurationProperties(env, map[string]any{"contents": "options fullstimer;"}),
			},
		},
	})
}

func TestAccConfiguration_singletonWithSecret(t *testing.T) {
	env := newTestAccEnv(t)
	mock := requireMock(t, env)
	ldap := seeded(t, mock, definitionLDAP, "")

	config := func(host, secretBlock string) string {
		return env.ProviderConfig + fmt.Sprintf(`
resource "viya_configuration" "test" {
  definition = %q
  services   = ["identities"]

  properties = {
    host          = %q
    port          = 636
    anonymousBind = false
  }
%s
}
`, definitionLDAP, host, secretBlock)
	}
	const secret = `
  sensitive_properties = {
    password = "new-ldap-password"
  }`

	stored := func(name string) any {
		configuration, _ := mock.Configuration(ldap.ID)
		return configuration.Properties[name]
	}
	checkStored := func(want map[string]any) resource.TestCheckFunc {
		return func(*terraform.State) error {
			for name, wantValue := range want {
				if got := stored(name); got != wantValue {
					return fmt.Errorf("the mock stores %q = %#v, want %#v", name, got, wantValue)
				}
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("ldap.bank.example", secret),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccConfigurationAddress, "id", ldap.ID),
					resource.TestCheckResourceAttr(testAccConfigurationAddress, "adopted", "true"),
					resource.TestCheckNoResourceAttr(testAccConfigurationAddress, "name"),
					checkStored(map[string]any{
						"host":          "ldap.bank.example",
						"port":          json.Number("636"),
						"anonymousBind": false,
						"password":      "new-ldap-password",
						// Not managed by Terraform: untouched.
						"userDN": "cn=viya,ou=service,dc=example,dc=com",
					}),
				),
			},
			{
				// The secret is never read back, so it causes no difference.
				Config: config("ldap.bank.example", secret),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				// Change a property that Terraform does not manage, and
				// one that it does, behind its back.
				Check: func(*terraform.State) error {
					mock.SetConfigurationProperty(ldap.ID, "userDN", "cn=changed-by-hand")
					return nil
				},
			},
			{
				// The unmanaged change is not reported.
				Config: config("ldap.bank.example", secret),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: func(*terraform.State) error {
					mock.SetConfigurationProperty(ldap.ID, "host", "ldap.rogue.example")
					return nil
				},
				ExpectNonEmptyPlan: true,
			},
			{
				// The managed change is reported and put right.
				Config: config("ldap.bank.example", secret),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(testAccConfigurationAddress, plancheck.ResourceActionUpdate),
					},
				},
				Check: checkStored(map[string]any{
					"host":   "ldap.bank.example",
					"userDN": "cn=changed-by-hand",
				}),
			},
			{
				// Terraform stops managing the secret. An update of another
				// property must not overwrite it with the masked value that
				// Viya returns.
				Config: config("ldap2.bank.example", ""),
				Check: checkStored(map[string]any{
					"host":     "ldap2.bank.example",
					"password": "new-ldap-password",
				}),
			},
		},
	})
}

func TestAccConfiguration_removedPropertyIsLeftAlone(t *testing.T) {
	env := newTestAccEnv(t)
	mock := requireMock(t, env)
	ldap := seeded(t, mock, definitionLDAP, "")

	config := func(properties string) string {
		return env.ProviderConfig + fmt.Sprintf(`
resource "viya_configuration" "test" {
  definition = %q
  services   = ["identities"]
  properties = %s
}
`, definitionLDAP, properties)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config(`{ host = "ldap.bank.example", port = 636 }`)},
			{
				Config: config(`{ host = "ldap.bank.example" }`),
				Check: func(*terraform.State) error {
					configuration, _ := mock.Configuration(ldap.ID)
					if got := configuration.Properties["port"]; got != json.Number("636") {
						return fmt.Errorf("port is %#v after it was removed from the configuration, want it left at 636", got)
					}
					return nil
				},
			},
			{
				Config: config(`{ host = "ldap.bank.example" }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccConfiguration_equivalentTypesAreNotADifference(t *testing.T) {
	env := newTestAccEnv(t)
	mock := requireMock(t, env)
	ldap := seeded(t, mock, definitionLDAP, "")

	// The port is written as a string, and the boolean too.
	config := env.ProviderConfig + fmt.Sprintf(`
resource "viya_configuration" "test" {
  definition = %q
  services   = ["identities"]

  properties = {
    port          = "636"
    anonymousBind = "false"
  }
}
`, definitionLDAP)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				// Viya normalises the values to their real types.
				Check: func(*terraform.State) error {
					mock.SetConfigurationProperty(ldap.ID, "port", json.Number("636"))
					mock.SetConfigurationProperty(ldap.ID, "anonymousBind", false)
					return nil
				},
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccConfiguration_ambiguous(t *testing.T) {
	env := newTestAccEnv(t)
	mock := requireMock(t, env)

	logger := testAccLoggerName()
	for range 2 {
		mock.AddConfiguration(viyamock.Configuration{
			Definition: definitionLogging,
			Services:   []string{"identities"},
			Properties: map[string]any{"name": logger, "level": "INFO"},
		})
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccLoggingConfig(env, logger, "DEBUG"),
			ExpectError: regexp.MustCompile(`Ambiguous configuration`),
		}},
	})

	for _, configuration := range mock.FindConfigurations(definitionLogging) {
		if configuration.Properties["level"] != "INFO" {
			t.Errorf("an instance was modified despite the ambiguity: %+v", configuration.Properties)
		}
	}
}

func TestAccConfiguration_matchesServicesExactly(t *testing.T) {
	env := newTestAccEnv(t)
	mock := requireMock(t, env)

	// An instance for other services is not the one the resource describes.
	logger := testAccLoggerName()
	other := mock.AddConfiguration(viyamock.Configuration{
		Definition: definitionLogging,
		Services:   []string{"identities", "authorization"},
		Properties: map[string]any{"name": logger, "level": "WARN"},
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckConfigurationsDeleted(env),
		Steps: []resource.TestStep{{
			Config: testAccLoggingConfig(env, logger, "DEBUG"),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(testAccConfigurationAddress, "adopted", "false"),
				func(*terraform.State) error {
					untouched, _ := mock.Configuration(other)
					if untouched.Properties["level"] != "WARN" {
						return fmt.Errorf("the instance for other services was modified: %+v", untouched.Properties)
					}
					return nil
				},
			),
		}},
	})
}

func TestAccConfiguration_validation(t *testing.T) {
	env := newTestAccEnv(t)

	body := func(arguments string) string {
		return env.ProviderConfig + fmt.Sprintf(`
resource "viya_configuration" "test" {
  definition = %q
  services   = ["identities"]
%s
}
`, definitionLogging, arguments)
	}

	tests := map[string]struct {
		arguments string
		want      string
	}{
		"neither properties nor sensitive_properties": {
			arguments: ``,
			want:      `No property to manage`,
		},
		"properties that is not an object": {
			arguments: `properties = "level=DEBUG"`,
			want:      `Invalid properties`,
		},
		"nested value": {
			arguments: `properties = { level = ["DEBUG"] }`,
			want:      `Unsupported property value`,
		},
		"null value": {
			arguments: `properties = { level = null }`,
			want:      `Unsupported property value`,
		},
		"name as a property": {
			arguments: `properties = { name = "com.sas", level = "DEBUG" }`,
			want:      `Reserved property name`,
		},
		"reserved field": {
			arguments: `properties = { metadata = "x" }`,
			want:      `Reserved property name`,
		},
		"property set twice": {
			arguments: `properties = { level = "DEBUG" }
  sensitive_properties = { level = "DEBUG" }`,
			want: `Property set twice`,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      body(tc.arguments),
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
}

func TestAccConfiguration_sendsIfMatch(t *testing.T) {
	// SAS Viya accepts updates without If-Match, but then cannot
	// detect a concurrent change. The strict mode of the mock proves that
	// the provider always sends the precondition.
	env := newStrictTestAccEnv(t)
	logger := testAccLoggerName()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckConfigurationsDeleted(env),
		Steps: []resource.TestStep{
			{Config: testAccLoggingConfig(env, logger, "DEBUG")},
			{
				Config: testAccLoggingConfig(env, logger, "INFO"),
				Check:  testAccCheckConfigurationProperties(env, map[string]any{"level": "INFO"}),
			},
		},
	})
}
