package provider

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/jacoblamarche/terraform-provider-viya/internal/viya"
	"github.com/jacoblamarche/terraform-provider-viya/internal/viyamock"
)

// testAccProtoV6ProviderFactories runs the provider in-process during
// acceptance tests.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"viya": providerserver.NewProtocol6WithError(New("test")()),
}

// testAccEnv is the Viya deployment that an acceptance test runs against.
type testAccEnv struct {
	// ProviderConfig is the provider block to prepend to test configurations.
	ProviderConfig string
	// Client reaches the same deployment, to verify what Terraform did and
	// to make changes behind its back.
	Client *viya.Client
	// Mock is the fake deployment, or nil when testing a real one.
	Mock *viyamock.Server
}

// newTestAccEnv returns the deployment to test against.
//
// By default it starts a fake Viya deployment. When VIYA_ENDPOINT is set,
// the same tests run against that deployment instead, with the credentials
// taken from the other VIYA_* environment variables.
func newTestAccEnv(t *testing.T, mockOptions ...viyamock.Option) testAccEnv {
	t.Helper()

	if os.Getenv(envEndpoint) != "" {
		client, err := newClient(clientSettings{
			Endpoint:     os.Getenv(envEndpoint),
			AccessToken:  os.Getenv(envAccessToken),
			ClientID:     os.Getenv(envClientID),
			ClientSecret: os.Getenv(envClientSecret),
			Username:     os.Getenv(envUsername),
			Password:     os.Getenv(envPassword),
			Version:      "test",
		})
		if err != nil {
			t.Fatalf("building a client from the VIYA_* environment variables: %v", err)
		}
		return testAccEnv{ProviderConfig: "provider \"viya\" {}\n", Client: client}
	}

	mock := viyamock.New(mockOptions...)
	server := mock.Start()
	t.Cleanup(server.Close)

	client, err := newClient(clientSettings{
		Endpoint:     server.URL,
		ClientID:     viyamock.DefaultClientID,
		ClientSecret: viyamock.DefaultClientSecret,
		Version:      "test",
	})
	if err != nil {
		t.Fatalf("building a client for the mock: %v", err)
	}

	return testAccEnv{
		ProviderConfig: fmt.Sprintf(`
provider "viya" {
  endpoint      = %q
  client_id     = %q
  client_secret = %q
}
`, server.URL, viyamock.DefaultClientID, viyamock.DefaultClientSecret),
		Client: client,
		Mock:   mock,
	}
}

// newStrictTestAccEnv returns a fake deployment that rejects updates
// without an If-Match header. It skips the test when a real deployment is
// targeted, because that behaviour cannot be switched on there.
func newStrictTestAccEnv(t *testing.T) testAccEnv {
	t.Helper()
	if os.Getenv(envEndpoint) != "" {
		t.Skip("this test needs the strict mode of the mock")
	}
	return newTestAccEnv(t, viyamock.WithRequireIfMatch())
}

// clearProviderEnv unsets the VIYA_* variables for the duration of a test.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{envEndpoint, envAccessToken, envClientID, envClientSecret, envUsername, envPassword} {
		t.Setenv(name, "")
	}
}

const testAccCustomGroupForProviderConfig = `
resource "viya_custom_group" "test" {
  id   = "unused"
  name = "Unused"
}
`

func TestAccProvider_configurationErrors(t *testing.T) {
	tests := map[string]struct {
		config string
		want   string
	}{
		"missing endpoint": {
			config: `provider "viya" { access_token = "t" }`,
			want:   "Missing Viya endpoint",
		},
		"missing credentials": {
			config: `provider "viya" { endpoint = "https://viya.example.com" }`,
			want:   "Missing Viya credentials",
		},
		"username without password": {
			config: `provider "viya" {
  endpoint  = "https://viya.example.com"
  client_id = "c"
  username  = "u"
}`,
			want: "Incomplete Viya user credentials",
		},
		"endpoint that is not a URL": {
			config: `provider "viya" {
  endpoint     = "viya.example.com"
  access_token = "t"
}`,
			want: "Invalid Viya provider configuration",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			clearProviderEnv(t)
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      tc.config + testAccCustomGroupForProviderConfig,
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
}

func TestAccProvider_environmentVariables(t *testing.T) {
	clearProviderEnv(t)

	mock := viyamock.New()
	server := mock.Start()
	t.Cleanup(server.Close)

	t.Setenv(envEndpoint, server.URL)
	t.Setenv(envClientID, viyamock.DefaultClientID)
	t.Setenv(envClientSecret, viyamock.DefaultClientSecret)
	t.Setenv(envUsername, viyamock.DefaultUsername)
	t.Setenv(envPassword, viyamock.DefaultPassword)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
provider "viya" {}

resource "viya_custom_group" "test" {
  id   = "from-env"
  name = "Configured from the environment"
}
`,
			Check: resource.TestCheckResourceAttr("viya_custom_group.test", "id", "from-env"),
		}},
	})
}

func TestAccProvider_badCredentials(t *testing.T) {
	clearProviderEnv(t)

	mock := viyamock.New()
	server := mock.Start()
	t.Cleanup(server.Close)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
provider "viya" {
  endpoint      = %q
  client_id     = %q
  client_secret = "wrong"
}
`, server.URL, viyamock.DefaultClientID) + testAccCustomGroupForProviderConfig,
			ExpectError: regexp.MustCompile(`authentication failed[\s\S]*Bad credentials`),
		}},
	})
}
