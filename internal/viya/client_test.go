package viya_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jacoblamarche/terraform-provider-viya/internal/viya"
	"github.com/jacoblamarche/terraform-provider-viya/internal/viyamock"
)

// newTestClient starts a fake Viya deployment and returns a client that
// authenticates against it with the client credentials grant.
func newTestClient(t *testing.T) (*viya.Client, *viyamock.Server) {
	t.Helper()

	mock := viyamock.New()
	server := mock.Start()
	t.Cleanup(server.Close)

	tokens, err := viya.NewOAuthTokenSource(viya.OAuthConfig{
		Endpoint:     server.URL,
		ClientID:     viyamock.DefaultClientID,
		ClientSecret: viyamock.DefaultClientSecret,
	})
	if err != nil {
		t.Fatalf("NewOAuthTokenSource: %v", err)
	}
	client, err := viya.NewClient(viya.Config{
		Endpoint:     server.URL,
		TokenSource:  tokens,
		RetryWaitMin: time.Millisecond,
		RetryWaitMax: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, mock
}

// countRequests returns how many requests the fake received for a method
// and path.
func countRequests(mock *viyamock.Server, method, path string) int {
	count := 0
	for _, request := range mock.Requests() {
		if request.Method == method && request.Path == path {
			count++
		}
	}
	return count
}

func TestNewClient_validatesConfig(t *testing.T) {
	t.Parallel()

	tests := map[string]viya.Config{
		"missing endpoint":     {TokenSource: viya.StaticToken("t")},
		"endpoint without URL": {Endpoint: "viya.example.com", TokenSource: viya.StaticToken("t")},
		"unsupported scheme":   {Endpoint: "ftp://viya.example.com", TokenSource: viya.StaticToken("t")},
		"missing token source": {Endpoint: "https://viya.example.com"},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := viya.NewClient(cfg); err == nil {
				t.Fatal("NewClient succeeded, want an error")
			}
		})
	}
}

func TestOAuth_passwordGrant(t *testing.T) {
	t.Parallel()

	mock := viyamock.New()
	server := mock.Start()
	t.Cleanup(server.Close)

	tokens, err := viya.NewOAuthTokenSource(viya.OAuthConfig{
		Endpoint:     server.URL,
		ClientID:     viyamock.DefaultClientID,
		ClientSecret: viyamock.DefaultClientSecret,
		Username:     viyamock.DefaultUsername,
		Password:     viyamock.DefaultPassword,
	})
	if err != nil {
		t.Fatalf("NewOAuthTokenSource: %v", err)
	}
	client, err := viya.NewClient(viya.Config{Endpoint: server.URL, TokenSource: tokens})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.ListConfigurations(context.Background(), "logging.level"); err != nil {
		t.Fatalf("request with the password grant: %v", err)
	}
}

func TestOAuth_badCredentials(t *testing.T) {
	t.Parallel()

	mock := viyamock.New()
	server := mock.Start()
	t.Cleanup(server.Close)

	tests := map[string]viya.OAuthConfig{
		"wrong client secret": {
			ClientID: viyamock.DefaultClientID, ClientSecret: "wrong",
		},
		"wrong user password": {
			ClientID: viyamock.DefaultClientID, ClientSecret: viyamock.DefaultClientSecret,
			Username: viyamock.DefaultUsername, Password: "wrong",
		},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg.Endpoint = server.URL
			tokens, err := viya.NewOAuthTokenSource(cfg)
			if err != nil {
				t.Fatalf("NewOAuthTokenSource: %v", err)
			}
			_, err = tokens.Token(context.Background())
			if err == nil {
				t.Fatal("Token succeeded, want an authentication error")
			}
			for _, want := range []string{"authentication failed", "Bad credentials"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestOAuth_cachesToken(t *testing.T) {
	t.Parallel()
	client, mock := newTestClient(t)

	for range 3 {
		if _, err := client.ListConfigurations(context.Background(), "logging.level"); err != nil {
			t.Fatalf("ListConfigurations: %v", err)
		}
	}
	if got := countRequests(mock, http.MethodPost, "/SASLogon/oauth/token"); got != 1 {
		t.Errorf("token requests = %d, want 1", got)
	}
}

func TestClient_renewsTokenAfterUnauthorized(t *testing.T) {
	t.Parallel()
	client, mock := newTestClient(t)
	ctx := context.Background()

	if _, err := client.ListConfigurations(ctx, "logging.level"); err != nil {
		t.Fatalf("ListConfigurations: %v", err)
	}

	mock.RevokeTokens()

	if _, err := client.ListConfigurations(ctx, "logging.level"); err != nil {
		t.Fatalf("request after the token was revoked: %v", err)
	}
	if got := countRequests(mock, http.MethodPost, "/SASLogon/oauth/token"); got != 2 {
		t.Errorf("token requests = %d, want 2", got)
	}
}

func TestClient_staticTokenUnauthorized(t *testing.T) {
	t.Parallel()

	mock := viyamock.New()
	server := mock.Start()
	t.Cleanup(server.Close)

	client, err := viya.NewClient(viya.Config{Endpoint: server.URL, TokenSource: viya.StaticToken("not-a-valid-token")})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.ListConfigurations(context.Background(), "logging.level")
	var apiErr *viya.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("error = %v, want an HTTP 401 API error", err)
	}
	// One initial attempt and one retry after invalidating the token.
	if got := countRequests(mock, http.MethodGet, "/configuration/configurations"); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
}

func TestClient_retries(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		fault        viyamock.Fault
		call         func(context.Context, *viya.Client) error
		wantErr      bool
		wantRequests int
	}{
		"GET is retried after 503": {
			fault:        viyamock.Fault{Status: http.StatusServiceUnavailable, Times: 2},
			call:         func(ctx context.Context, c *viya.Client) error { _, err := c.GetGroup(ctx, "g"); return err },
			wantRequests: 3,
		},
		"GET is retried after 502": {
			fault:        viyamock.Fault{Status: http.StatusBadGateway},
			call:         func(ctx context.Context, c *viya.Client) error { _, err := c.GetGroup(ctx, "g"); return err },
			wantRequests: 2,
		},
		"GET is retried after 429": {
			fault:        viyamock.Fault{Status: http.StatusTooManyRequests, RetryAfter: 1},
			call:         func(ctx context.Context, c *viya.Client) error { _, err := c.GetGroup(ctx, "g"); return err },
			wantRequests: 2,
		},
		"GET gives up after the maximum number of retries": {
			fault:        viyamock.Fault{Status: http.StatusServiceUnavailable, Times: 10},
			call:         func(ctx context.Context, c *viya.Client) error { _, err := c.GetGroup(ctx, "g"); return err },
			wantErr:      true,
			wantRequests: 4,
		},
		"POST is retried after 503": {
			fault: viyamock.Fault{Status: http.StatusServiceUnavailable},
			call: func(ctx context.Context, c *viya.Client) error {
				_, err := c.CreateGroup(ctx, viya.Group{ID: "new", Name: "New"})
				return err
			},
			wantRequests: 2,
		},
		"POST is not retried after 502": {
			fault: viyamock.Fault{Status: http.StatusBadGateway},
			call: func(ctx context.Context, c *viya.Client) error {
				_, err := c.CreateGroup(ctx, viya.Group{ID: "new", Name: "New"})
				return err
			},
			wantErr:      true,
			wantRequests: 1,
		},
		"400 is not retried": {
			fault:        viyamock.Fault{Status: http.StatusBadRequest},
			call:         func(ctx context.Context, c *viya.Client) error { _, err := c.GetGroup(ctx, "g"); return err },
			wantErr:      true,
			wantRequests: 1,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client, mock := newTestClient(t)
			ctx := context.Background()

			if _, err := client.CreateGroup(ctx, viya.Group{ID: "g", Name: "G"}); err != nil {
				t.Fatalf("CreateGroup: %v", err)
			}
			before := len(mock.Requests())

			tc.fault.PathPrefix = "/identities/"
			mock.InjectFault(tc.fault)

			err := tc.call(ctx, client)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr = %t", err, tc.wantErr)
			}
			if got := len(mock.Requests()) - before; got != tc.wantRequests {
				t.Errorf("requests = %d, want %d", got, tc.wantRequests)
			}
		})
	}
}

func TestClient_retryStopsWhenContextIsCancelled(t *testing.T) {
	t.Parallel()

	mock := viyamock.New()
	server := mock.Start()
	t.Cleanup(server.Close)

	tokens, err := viya.NewOAuthTokenSource(viya.OAuthConfig{
		Endpoint: server.URL, ClientID: viyamock.DefaultClientID, ClientSecret: viyamock.DefaultClientSecret,
	})
	if err != nil {
		t.Fatalf("NewOAuthTokenSource: %v", err)
	}
	client, err := viya.NewClient(viya.Config{
		Endpoint: server.URL, TokenSource: tokens,
		RetryWaitMin: time.Hour, RetryWaitMax: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	mock.InjectFault(viyamock.Fault{Status: http.StatusServiceUnavailable, PathPrefix: "/configuration/", Times: 10})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.ListConfigurations(ctx, "logging.level"); err == nil {
		t.Fatal("the request succeeded, want a context error")
	}
}

func TestGroups_lifecycle(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	ctx := context.Background()

	created, err := client.CreateGroup(ctx, viya.Group{ID: "analysts", Name: "Analysts", Description: "Risk analysts"})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if created.State != viya.GroupStateActive {
		t.Errorf("State = %q, want %q", created.State, viya.GroupStateActive)
	}
	if created.ProviderID != "local" {
		t.Errorf("ProviderID = %q, want %q", created.ProviderID, "local")
	}
	read, err := client.GetGroup(ctx, "analysts")
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if read.Name != "Analysts" || read.Description != "Risk analysts" {
		t.Errorf("GetGroup = %+v, want the group as created", read)
	}

	read.Name = "Risk Analysts"
	read.Description = ""
	read.State = viya.GroupStateInactive
	updated, err := client.UpdateGroup(ctx, *read)
	if err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	if updated.Name != "Risk Analysts" || updated.Description != "" || updated.State != viya.GroupStateInactive {
		t.Errorf("UpdateGroup = %+v, want the new values", updated)
	}
	if err := client.DeleteGroup(ctx, "analysts"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if _, err := client.GetGroup(ctx, "analysts"); !viya.IsNotFound(err) {
		t.Errorf("GetGroup after deletion: error = %v, want not found", err)
	}
	if err := client.DeleteGroup(ctx, "analysts"); !viya.IsNotFound(err) {
		t.Errorf("second DeleteGroup: error = %v, want not found", err)
	}
}

func TestGroups_createConflict(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	ctx := context.Background()

	if _, err := client.CreateGroup(ctx, viya.Group{ID: "dup", Name: "First"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := client.CreateGroup(ctx, viya.Group{ID: "dup", Name: "Second"}); !viya.IsConflict(err) {
		t.Errorf("second CreateGroup: error = %v, want a conflict", err)
	}
}

func TestGroups_escapesID(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	ctx := context.Background()

	const id = "Équipe risque/crédit"
	if _, err := client.CreateGroup(ctx, viya.Group{ID: id, Name: "Team"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	group, err := client.GetGroup(ctx, id)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if group.ID != id {
		t.Errorf("ID = %q, want %q", group.ID, id)
	}
}

func TestAPIError_message(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)

	_, err := client.GetGroup(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetGroup succeeded, want an error")
	}
	for _, want := range []string{"GET /identities/groups/missing", "HTTP 404", "could not be found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}
