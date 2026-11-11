package viyamock_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jacoblamarche/terraform-provider-viya/internal/viyamock"
)

// start serves a fake and returns it with its base URL.
func start(t *testing.T, opts ...viyamock.Option) (*viyamock.Server, *httptest.Server) {
	t.Helper()
	mock := viyamock.New(opts...)
	server := mock.Start()
	t.Cleanup(server.Close)
	return mock, server
}

// token obtains an access token with the client credentials grant.
func token(t *testing.T, baseURL string) string {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, baseURL+"/SASLogon/oauth/token",
		strings.NewReader(url.Values{"grant_type": {"client_credentials"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(viyamock.DefaultClientID, viyamock.DefaultClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var body struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	resp := do(t, req, &body)
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" || body.TokenType != "bearer" || body.ExpiresIn <= 0 {
		t.Fatalf("token response: status %d, body %+v", resp.StatusCode, body)
	}
	return body.AccessToken
}

// call sends an authenticated JSON request.
func call(t *testing.T, method, target, bearer, body string, header http.Header, out any) *http.Response {
	t.Helper()

	req, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range header {
		req.Header[name] = values
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return do(t, req, out)
}

func do(t *testing.T, req *http.Request, out any) *http.Response {
	t.Helper()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decoding %s %s response %q: %v", req.Method, req.URL.Path, raw, err)
		}
	}
	return resp
}

func TestToken_formClientCredentials(t *testing.T) {
	t.Parallel()
	_, server := start(t)

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {viyamock.DefaultClientID},
		"client_secret": {viyamock.DefaultClientSecret},
	}
	resp, err := http.PostForm(server.URL+"/SASLogon/oauth/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestToken_unsupportedGrant(t *testing.T) {
	t.Parallel()
	_, server := start(t)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/SASLogon/oauth/token",
		strings.NewReader(url.Values{"grant_type": {"authorization_code"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(viyamock.DefaultClientID, viyamock.DefaultClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var body struct {
		Error string `json:"error"`
	}
	resp := do(t, req, &body)
	if resp.StatusCode != http.StatusBadRequest || body.Error != "unsupported_grant_type" {
		t.Errorf("status = %d, error = %q; want 400 unsupported_grant_type", resp.StatusCode, body.Error)
	}
}

func TestAPI_requiresValidToken(t *testing.T) {
	t.Parallel()
	_, server := start(t)

	for name, bearer := range map[string]string{"no token": "", "unknown token": "nope"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var body struct {
				HTTPStatusCode int `json:"httpStatusCode"`
			}
			resp := call(t, http.MethodGet, server.URL+"/configuration/configurations", bearer, "", nil, &body)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", resp.StatusCode)
			}
			if got := resp.Header.Get("Content-Type"); got != "application/vnd.sas.error+json" {
				t.Errorf("Content-Type = %q, want the SAS error media type", got)
			}
			if body.HTTPStatusCode != http.StatusUnauthorized {
				t.Errorf("httpStatusCode = %d, want 401", body.HTTPStatusCode)
			}
		})
	}
}

func TestAPI_tokenExpires(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, server := start(t,
		viyamock.WithTokenTTL(10*time.Minute),
		viyamock.WithClock(func() time.Time { return now }),
	)
	bearer := token(t, server.URL)

	if resp := call(t, http.MethodGet, server.URL+"/configuration/configurations", bearer, "", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("status before expiry = %d, want 200", resp.StatusCode)
	}

	now = now.Add(11 * time.Minute)

	if resp := call(t, http.MethodGet, server.URL+"/configuration/configurations", bearer, "", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status after expiry = %d, want 401", resp.StatusCode)
	}
}

func TestGroups_representation(t *testing.T) {
	t.Parallel()
	_, server := start(t)
	bearer := token(t, server.URL)

	var group struct {
		ID                string `json:"id"`
		State             string `json:"state"`
		ProviderID        string `json:"providerId"`
		CreationTimeStamp string `json:"creationTimeStamp"`
		Links             []struct {
			Rel string `json:"rel"`
		} `json:"links"`
	}
	// The trailing slash is what SAS's own pyviyatools sends.
	resp := call(t, http.MethodPost, server.URL+"/identities/groups/", bearer,
		`{"id":"g1","name":"Group 1"}`, nil, &group)

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/vnd.sas.identity.group+json" {
		t.Errorf("Content-Type = %q, want the group media type", got)
	}
	if got := resp.Header.Get("Location"); got != "/identities/groups/g1" {
		t.Errorf("Location = %q, want /identities/groups/g1", got)
	}
	if resp.Header.Get("ETag") == "" || resp.Header.Get("Last-Modified") == "" {
		t.Error("ETag or Last-Modified header is missing")
	}
	if group.ID != "g1" || group.State != "active" || group.ProviderID != "local" {
		t.Errorf("group = %+v, want defaults applied", group)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", group.CreationTimeStamp); err != nil {
		t.Errorf("creationTimeStamp %q is not in the Viya format", group.CreationTimeStamp)
	}
	if len(group.Links) == 0 {
		t.Error("the group has no links")
	}
}

func TestGroups_validation(t *testing.T) {
	t.Parallel()
	_, server := start(t)
	bearer := token(t, server.URL)

	tests := map[string]string{
		"missing id":    `{"name":"No ID"}`,
		"missing name":  `{"id":"g"}`,
		"invalid state": `{"id":"g","name":"G","state":"paused"}`,
		"invalid JSON":  `{"id":`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resp := call(t, http.MethodPost, server.URL+"/identities/groups", bearer, body, nil, nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestGroups_updatePreconditions(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		opts    []viyamock.Option
		ifMatch func(etag string) string
		want    int
	}{
		"matching If-Match":                  {ifMatch: func(etag string) string { return etag }, want: http.StatusOK},
		"wildcard If-Match":                  {ifMatch: func(string) string { return "*" }, want: http.StatusOK},
		"stale If-Match":                     {ifMatch: func(string) string { return `"stale"` }, want: http.StatusPreconditionFailed},
		"no If-Match, lenient (the default)": {ifMatch: func(string) string { return "" }, want: http.StatusOK},
		"no If-Match, strict": {
			opts:    []viyamock.Option{viyamock.WithRequireIfMatch()},
			ifMatch: func(string) string { return "" },
			want:    http.StatusPreconditionRequired,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, server := start(t, tc.opts...)
			bearer := token(t, server.URL)

			created := call(t, http.MethodPost, server.URL+"/identities/groups", bearer, `{"id":"g","name":"G"}`, nil, nil)
			header := http.Header{}
			if value := tc.ifMatch(created.Header.Get("ETag")); value != "" {
				header.Set("If-Match", value)
			}

			resp := call(t, http.MethodPut, server.URL+"/identities/groups/g", bearer, `{"id":"g","name":"Renamed"}`, header, nil)
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

func TestGroups_updateRejectsMismatchedID(t *testing.T) {
	t.Parallel()
	_, server := start(t)
	bearer := token(t, server.URL)

	call(t, http.MethodPost, server.URL+"/identities/groups", bearer, `{"id":"g","name":"G"}`, nil, nil)
	resp := call(t, http.MethodPut, server.URL+"/identities/groups/g", bearer, `{"id":"other","name":"G"}`, nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUnknownEndpoint(t *testing.T) {
	t.Parallel()
	_, server := start(t)

	resp := call(t, http.MethodGet, server.URL+"/folders/folders", "", "", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRemoveGroup(t *testing.T) {
	t.Parallel()
	mock, server := start(t)
	bearer := token(t, server.URL)

	call(t, http.MethodPost, server.URL+"/identities/groups", bearer, `{"id":"g","name":"G"}`, nil, nil)

	if _, ok := mock.Group("g"); !ok {
		t.Fatal("the group was not stored")
	}
	if !mock.RemoveGroup("g") {
		t.Error("RemoveGroup = false for an existing group")
	}
	if mock.RemoveGroup("g") {
		t.Error("RemoveGroup = true for a group that is gone")
	}
	if resp := call(t, http.MethodGet, server.URL+"/identities/groups/g", bearer, "", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

const ldapMediaType = "application/vnd.sas.configuration.config.sas.identities.providers.ldap.connection+json;version=1"

// ldapConnection returns the ID of the seeded LDAP connection instance.
func ldapConnection(t *testing.T, mock *viyamock.Server) string {
	t.Helper()
	found := mock.FindConfigurations("sas.identities.providers.ldap.connection")
	if len(found) != 1 {
		t.Fatalf("the mock starts with %d LDAP connection instances, want 1", len(found))
	}
	return found[0].ID
}

func TestConfigurations_secretsAreMasked(t *testing.T) {
	t.Parallel()
	mock, server := start(t)
	bearer := token(t, server.URL)
	target := server.URL + "/configuration/configurations/" + ldapConnection(t, mock)

	var read map[string]any
	resp := call(t, http.MethodGet, target, bearer, "", nil, &read)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") == "" {
		t.Fatalf("status = %d, ETag = %q", resp.StatusCode, resp.Header.Get("ETag"))
	}
	if read["password"] != viyamock.SecretMask {
		t.Fatalf("password is returned as %q, want it masked", read["password"])
	}
	if read["host"] != "ldap.example.com" {
		t.Errorf("host = %q, want it in clear", read["host"])
	}

	// Writing the masked value back leaves the stored secret unchanged.
	read["host"] = "ldap2.example.com"
	body, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{"Content-Type": {ldapMediaType}}
	if resp := call(t, http.MethodPut, target, bearer, string(body), header, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}
	stored, _ := mock.Configuration(ldapConnection(t, mock))
	if stored.Properties["password"] != "initial-ldap-password" || stored.Properties["host"] != "ldap2.example.com" {
		t.Errorf("stored properties = %v", stored.Properties)
	}

	// Writing another value replaces the secret.
	read["password"] = "rotated"
	body, err = json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	call(t, http.MethodPut, target, bearer, string(body), header, nil)
	if stored, _ := mock.Configuration(ldapConnection(t, mock)); stored.Properties["password"] != "rotated" {
		t.Errorf("password = %q after it was rotated", stored.Properties["password"])
	}
}

func TestConfigurations_contentTypeNamesTheDefinition(t *testing.T) {
	t.Parallel()
	mock, server := start(t)
	bearer := token(t, server.URL)
	collection := server.URL + "/configuration/configurations"

	tests := map[string]struct {
		method, target, contentType string
		want                        int
	}{
		"create with a plain JSON content type": {
			http.MethodPost, collection, "application/json", http.StatusBadRequest,
		},
		"create for an unknown definition": {
			http.MethodPost, collection, "application/vnd.sas.configuration.config.no.such+json;version=1", http.StatusBadRequest,
		},
		"create for a known definition": {
			http.MethodPost, collection, "application/vnd.sas.configuration.config.logging.level+json;version=1", http.StatusCreated,
		},
		"update with the media type of another definition": {
			http.MethodPut, collection + "/" + ldapConnection(t, mock),
			"application/vnd.sas.configuration.config.logging.level+json;version=1", http.StatusBadRequest,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := `{"metadata":{"services":["identities"]},"name":"com.sas","level":"INFO"}`
			resp := call(t, tc.method, tc.target, bearer, body, http.Header{"Content-Type": {tc.contentType}}, nil)
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

func TestConfigurations_listByDefinition(t *testing.T) {
	t.Parallel()
	_, server := start(t)
	bearer := token(t, server.URL)

	var page struct {
		Count int `json:"count"`
		Items []struct {
			Name     string `json:"name"`
			Metadata struct {
				IsDefault bool     `json:"isDefault"`
				MediaType string   `json:"mediaType"`
				Services  []string `json:"services"`
			} `json:"metadata"`
		} `json:"items"`
	}
	call(t, http.MethodGet, server.URL+"/configuration/configurations?definitionName=sas.compute.server", bearer, "", nil, &page)

	if page.Count != 3 || len(page.Items) != 3 {
		t.Fatalf("count = %d with %d items, want the 3 seeded compute instances", page.Count, len(page.Items))
	}
	for _, item := range page.Items {
		if !item.Metadata.IsDefault || item.Metadata.Services[0] != "compute" ||
			item.Metadata.MediaType != "application/vnd.sas.configuration.config.sas.compute.server+json;version=1" {
			t.Errorf("item %q has metadata %+v", item.Name, item.Metadata)
		}
	}

	resp := call(t, http.MethodGet, server.URL+"/configuration/configurations?serviceId=compute", bearer, "", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status for an unimplemented query parameter = %d, want 400", resp.StatusCode)
	}
}
