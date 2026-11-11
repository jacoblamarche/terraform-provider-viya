package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/jacoblamarche/terraform-provider-viya/internal/viya"
)

const testAccCustomGroupAddress = "viya_custom_group.test"

// testAccCustomGroupID returns a group ID that is unlikely to collide with an
// existing group when the tests run against a shared deployment.
func testAccCustomGroupID() string {
	return "tfacc-" + acctest.RandStringFromCharSet(10, acctest.CharSetAlphaNum)
}

// testAccCustomGroupConfig renders a viya_custom_group resource. Empty optional
// arguments are left out.
func testAccCustomGroupConfig(env testAccEnv, id, name, description, state string) string {
	config := env.ProviderConfig + fmt.Sprintf("\nresource \"viya_custom_group\" \"test\" {\n  id   = %q\n  name = %q\n", id, name)
	if description != "" {
		config += fmt.Sprintf("  description = %q\n", description)
	}
	if state != "" {
		config += fmt.Sprintf("  state = %q\n", state)
	}
	return config + "}\n"
}

// testAccCheckCustomGroupDestroyed verifies that no group managed by the test is
// left in Viya.
func testAccCheckCustomGroupDestroyed(env testAccEnv) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		for _, rs := range state.RootModule().Resources {
			if rs.Type != "viya_custom_group" {
				continue
			}
			_, err := env.Client.GetGroup(context.Background(), rs.Primary.ID)
			if err == nil {
				return fmt.Errorf("group %q still exists", rs.Primary.ID)
			}
			if !viya.IsNotFound(err) {
				return fmt.Errorf("checking that group %q is gone: %w", rs.Primary.ID, err)
			}
		}
		return nil
	}
}

// testAccCheckCustomGroupInViya verifies the group as Viya stores it, rather than
// as Terraform recorded it.
func testAccCheckCustomGroupInViya(env testAccEnv, id string, want viya.Group) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got, err := env.Client.GetGroup(context.Background(), id)
		if err != nil {
			return fmt.Errorf("reading group %q from Viya: %w", id, err)
		}
		if got.Name != want.Name || got.Description != want.Description || got.State != want.State {
			return fmt.Errorf("group %q in Viya is {name: %q, description: %q, state: %q}, want {name: %q, description: %q, state: %q}",
				id, got.Name, got.Description, got.State, want.Name, want.Description, want.State)
		}
		return nil
	}
}

func TestAccCustomGroup_lifecycle(t *testing.T) {
	env := newTestAccEnv(t)
	id := testAccCustomGroupID()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCustomGroupDestroyed(env),
		Steps: []resource.TestStep{
			{
				// Create with the required arguments only.
				Config: testAccCustomGroupConfig(env, id, "Analysts", "", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccCustomGroupAddress, "id", id),
					resource.TestCheckResourceAttr(testAccCustomGroupAddress, "name", "Analysts"),
					resource.TestCheckNoResourceAttr(testAccCustomGroupAddress, "description"),
					resource.TestCheckResourceAttr(testAccCustomGroupAddress, "state", "active"),
					resource.TestCheckResourceAttr(testAccCustomGroupAddress, "provider_id", "local"),
					testAccCheckCustomGroupInViya(env, id, viya.Group{Name: "Analysts", State: "active"}),
				),
			},
			{
				// The imported state must match the state after creation.
				ResourceName:      testAccCustomGroupAddress,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Update every mutable argument in place.
				Config: testAccCustomGroupConfig(env, id, "Risk Analysts", "Analysts of the risk team", "inactive"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(testAccCustomGroupAddress, plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccCustomGroupAddress, "name", "Risk Analysts"),
					resource.TestCheckResourceAttr(testAccCustomGroupAddress, "description", "Analysts of the risk team"),
					resource.TestCheckResourceAttr(testAccCustomGroupAddress, "state", "inactive"),
					testAccCheckCustomGroupInViya(env, id, viya.Group{
						Name: "Risk Analysts", Description: "Analysts of the risk team", State: "inactive",
					}),
				),
			},
			{
				ResourceName:      testAccCustomGroupAddress,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Removing the optional arguments clears the description and
				// restores the default state.
				Config: testAccCustomGroupConfig(env, id, "Risk Analysts", "", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(testAccCustomGroupAddress, "description"),
					resource.TestCheckResourceAttr(testAccCustomGroupAddress, "state", "active"),
					testAccCheckCustomGroupInViya(env, id, viya.Group{Name: "Risk Analysts", State: "active"}),
				),
			},
			{
				// Applying the same configuration again changes nothing.
				Config: testAccCustomGroupConfig(env, id, "Risk Analysts", "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccCustomGroup_changingIDReplaces(t *testing.T) {
	env := newTestAccEnv(t)
	first, second := testAccCustomGroupID(), testAccCustomGroupID()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCustomGroupDestroyed(env),
		Steps: []resource.TestStep{
			{
				Config: testAccCustomGroupConfig(env, first, "Analysts", "", ""),
			},
			{
				Config: testAccCustomGroupConfig(env, second, "Analysts", "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(testAccCustomGroupAddress, plancheck.ResourceActionReplace),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccCustomGroupAddress, "id", second),
					func(*terraform.State) error {
						if _, err := env.Client.GetGroup(context.Background(), first); !viya.IsNotFound(err) {
							return fmt.Errorf("the replaced group %q was not deleted: %w", first, err)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccCustomGroup_deletedOutsideTerraform(t *testing.T) {
	env := newTestAccEnv(t)
	id := testAccCustomGroupID()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCustomGroupDestroyed(env),
		Steps: []resource.TestStep{
			{
				Config: testAccCustomGroupConfig(env, id, "Analysts", "", ""),
				// Delete the group behind Terraform's back. The refresh that
				// follows must notice, and plan to create it again.
				Check: func(*terraform.State) error {
					return env.Client.DeleteGroup(context.Background(), id)
				},
				ExpectNonEmptyPlan: true,
			},
			{
				Config: testAccCustomGroupConfig(env, id, "Analysts", "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(testAccCustomGroupAddress, plancheck.ResourceActionCreate),
					},
				},
				Check: testAccCheckCustomGroupInViya(env, id, viya.Group{Name: "Analysts", State: "active"}),
			},
		},
	})
}

func TestAccCustomGroup_changedOutsideTerraform(t *testing.T) {
	env := newTestAccEnv(t)
	id := testAccCustomGroupID()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCustomGroupDestroyed(env),
		Steps: []resource.TestStep{
			{
				Config: testAccCustomGroupConfig(env, id, "Analysts", "", ""),
				// Rename the group behind Terraform's back.
				Check: func(*terraform.State) error {
					ctx := context.Background()
					group, err := env.Client.GetGroup(ctx, id)
					if err != nil {
						return err
					}
					group.Name = "Renamed by hand"
					_, err = env.Client.UpdateGroup(ctx, *group)
					return err
				},
				ExpectNonEmptyPlan: true,
			},
			{
				// Terraform puts the configured name back.
				Config: testAccCustomGroupConfig(env, id, "Analysts", "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(testAccCustomGroupAddress, plancheck.ResourceActionUpdate),
					},
				},
				Check: testAccCheckCustomGroupInViya(env, id, viya.Group{Name: "Analysts", State: "active"}),
			},
		},
	})
}

func TestAccCustomGroup_alreadyExists(t *testing.T) {
	env := newTestAccEnv(t)
	id := testAccCustomGroupID()
	ctx := context.Background()

	if _, err := env.Client.CreateGroup(ctx, viya.Group{ID: id, Name: "Created by hand"}); err != nil {
		t.Fatalf("creating the pre-existing group: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Client.DeleteGroup(ctx, id); err != nil && !viya.IsNotFound(err) {
			t.Errorf("deleting the pre-existing group: %v", err)
		}
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccCustomGroupConfig(env, id, "Analysts", "", ""),
			ExpectError: regexp.MustCompile(`Group already exists[\s\S]*terraform\s+import`),
		}},
	})

	// The failed creation must not have touched the existing group.
	group, err := env.Client.GetGroup(ctx, id)
	if err != nil {
		t.Fatalf("reading the pre-existing group: %v", err)
	}
	if group.Name != "Created by hand" {
		t.Errorf("the pre-existing group was renamed to %q", group.Name)
	}
}

func TestAccCustomGroup_validation(t *testing.T) {
	env := newTestAccEnv(t)

	tests := map[string]struct {
		config string
		want   string
	}{
		"empty id": {
			config: testAccCustomGroupConfig(env, "", "Analysts", "", ""),
			want:   `Attribute id string length must be at least 1`,
		},
		"empty name": {
			config: testAccCustomGroupConfig(env, "g", "", "", ""),
			want:   `Attribute name string length must be at least 1`,
		},
		"invalid state": {
			config: testAccCustomGroupConfig(env, "g", "Analysts", "", "paused"),
			want:   `Attribute state value must be one of`,
		},
		"empty description": {
			config: env.ProviderConfig + `
resource "viya_custom_group" "test" {
  id          = "g"
  name        = "Analysts"
  description = ""
}
`,
			want: `Attribute description string length must be at least 1`,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      tc.config,
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
}

func TestAccCustomGroup_requiresIfMatch(t *testing.T) {
	// A deployment may reject updates that carry no If-Match header. The
	// strict mode of the mock proves that the provider always sends one.
	env := newStrictTestAccEnv(t)
	id := testAccCustomGroupID()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCustomGroupDestroyed(env),
		Steps: []resource.TestStep{
			{Config: testAccCustomGroupConfig(env, id, "Analysts", "", "")},
			{
				Config: testAccCustomGroupConfig(env, id, "Risk Analysts", "", ""),
				Check:  testAccCheckCustomGroupInViya(env, id, viya.Group{Name: "Risk Analysts", State: "active"}),
			},
		},
	})
}
