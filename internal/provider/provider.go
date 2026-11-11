// Package provider implements the Terraform provider for SAS Viya.
package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jacoblamarche/terraform-provider-viya/internal/viya"
)

// Environment variables read when the matching provider argument is not set.
const (
	envEndpoint     = "VIYA_ENDPOINT"
	envAccessToken  = "VIYA_ACCESS_TOKEN"
	envClientID     = "VIYA_CLIENT_ID"
	envClientSecret = "VIYA_CLIENT_SECRET"
	envUsername     = "VIYA_USERNAME"
	envPassword     = "VIYA_PASSWORD"
)

var _ provider.Provider = (*viyaProvider)(nil)

type viyaProvider struct {
	version string
}

type viyaProviderModel struct {
	Endpoint     types.String `tfsdk:"endpoint"`
	AccessToken  types.String `tfsdk:"access_token"`
	ClientID     types.String `tfsdk:"client_id"`
	ClientSecret types.String `tfsdk:"client_secret"`
	Username     types.String `tfsdk:"username"`
	Password     types.String `tfsdk:"password"`
}

// New returns a function that creates the provider, as providerserver
// expects.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &viyaProvider{version: version}
	}
}

func (p *viyaProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "viya"
	resp.Version = p.version
}

func (p *viyaProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Viya provider manages the configuration of a " +
			"[SAS Viya](https://www.sas.com/en_us/software/viya.html) deployment through its REST APIs.\n\n" +
			"The provider authenticates against SAS Logon Manager in one of three ways:\n\n" +
			"- `client_id` and `client_secret`: the OAuth2 client credentials grant. " +
			"This is the recommended way for automation.\n" +
			"- `client_id`, `client_secret`, `username` and `password`: the OAuth2 password grant, " +
			"which acts on behalf of a user.\n" +
			"- `access_token`: a token obtained out of band. It is not renewed when it expires.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Base URL of the SAS Viya deployment, for example `https://viya.example.com`. " +
					"Can also be set with the `" + envEndpoint + "` environment variable.",
			},
			"access_token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "OAuth2 access token. When set, the other credentials are ignored. " +
					"Can also be set with the `" + envAccessToken + "` environment variable.",
			},
			"client_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "ID of an OAuth2 client registered in SAS Logon Manager. " +
					"Can also be set with the `" + envClientID + "` environment variable.",
			},
			"client_secret": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "Secret of the OAuth2 client. " +
					"Can also be set with the `" + envClientSecret + "` environment variable.",
			},
			"username": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "User on whose behalf the provider acts. Selects the OAuth2 password grant. " +
					"Can also be set with the `" + envUsername + "` environment variable.",
			},
			"password": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "Password of the user. " +
					"Can also be set with the `" + envPassword + "` environment variable.",
			},
		},
	}
}

func (p *viyaProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config viyaProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A value that depends on a resource not yet created is unknown during
	// planning. The provider cannot work without it, so say so explicitly
	// rather than fail with a confusing authentication error.
	arguments := []struct {
		name  string
		value types.String
		env   string
	}{
		{"endpoint", config.Endpoint, envEndpoint},
		{"access_token", config.AccessToken, envAccessToken},
		{"client_id", config.ClientID, envClientID},
		{"client_secret", config.ClientSecret, envClientSecret},
		{"username", config.Username, envUsername},
		{"password", config.Password, envPassword},
	}
	resolved := make(map[string]string, len(arguments))
	for _, argument := range arguments {
		if argument.value.IsUnknown() {
			resp.Diagnostics.AddAttributeError(
				path.Root(argument.name),
				"Unknown provider argument",
				fmt.Sprintf("The value of %q is not known yet. Set it to a value that is known "+
					"before apply, or use the %s environment variable.", argument.name, argument.env),
			)
			continue
		}
		resolved[argument.name] = os.Getenv(argument.env)
		if !argument.value.IsNull() {
			resolved[argument.name] = argument.value.ValueString()
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	settings := clientSettings{
		Endpoint:     resolved["endpoint"],
		AccessToken:  resolved["access_token"],
		ClientID:     resolved["client_id"],
		ClientSecret: resolved["client_secret"],
		Username:     resolved["username"],
		Password:     resolved["password"],
		Version:      p.version,
	}

	if settings.Endpoint == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("endpoint"),
			"Missing Viya endpoint",
			"Set the endpoint argument or the "+envEndpoint+" environment variable to the base URL "+
				"of the SAS Viya deployment.",
		)
	}
	if settings.AccessToken == "" && settings.ClientID == "" {
		resp.Diagnostics.AddError(
			"Missing Viya credentials",
			"Set either access_token, or client_id and client_secret (with username and password for "+
				"the password grant). The matching environment variables are "+envAccessToken+", "+
				envClientID+", "+envClientSecret+", "+envUsername+" and "+envPassword+".",
		)
	}
	if settings.AccessToken == "" && (settings.Username == "") != (settings.Password == "") {
		resp.Diagnostics.AddError(
			"Incomplete Viya user credentials",
			"The username and password arguments must be set together.",
		)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	client, err := newClient(settings)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Viya provider configuration", err.Error())
		return
	}

	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *viyaProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newCustomGroupResource,
	}
}

func (p *viyaProvider) DataSources(context.Context) []func() datasource.DataSource {
	return nil
}

type clientSettings struct {
	Endpoint     string
	AccessToken  string
	ClientID     string
	ClientSecret string
	Username     string
	Password     string
	Version      string
}

func newClient(settings clientSettings) (*viya.Client, error) {
	userAgent := "terraform-provider-viya/" + settings.Version

	var tokens viya.TokenSource = viya.StaticToken(settings.AccessToken)
	if settings.AccessToken == "" {
		var err error
		tokens, err = viya.NewOAuthTokenSource(viya.OAuthConfig{
			Endpoint:     settings.Endpoint,
			ClientID:     settings.ClientID,
			ClientSecret: settings.ClientSecret,
			Username:     settings.Username,
			Password:     settings.Password,
			UserAgent:    userAgent,
		})
		if err != nil {
			return nil, err
		}
	}

	return viya.NewClient(viya.Config{
		Endpoint:    settings.Endpoint,
		TokenSource: tokens,
		UserAgent:   userAgent,
	})
}
