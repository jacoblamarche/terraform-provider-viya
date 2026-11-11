package viya

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const tokenPath = "/SASLogon/oauth/token"

// A cached token is renewed this long before it expires.
const tokenExpiryMargin = 30 * time.Second

// TokenSource supplies bearer tokens for Viya API requests.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	// Invalidate discards any cached token, so that the next call to Token
	// obtains a new one. The client calls it after an HTTP 401.
	Invalidate()
}

// StaticToken is a TokenSource for an access token obtained out of band, for
// example with the sas-viya CLI. It cannot be renewed.
type StaticToken string

// Token implements TokenSource.
func (t StaticToken) Token(context.Context) (string, error) {
	if t == "" {
		return "", errors.New("viya: access token is empty")
	}
	return string(t), nil
}

// Invalidate implements TokenSource. A static token cannot be renewed.
func (t StaticToken) Invalidate() {}

// OAuthConfig describes how to obtain tokens from SAS Logon Manager with the
// client_credentials grant.
type OAuthConfig struct {
	Endpoint     string
	ClientID     string
	ClientSecret string
	UserAgent    string
	// Log, when set, receives one event per token request.
	Log LogFunc
}

// oauthTokenSource obtains tokens from SAS Logon and caches them until
// shortly before they expire.
type oauthTokenSource struct {
	cfg      OAuthConfig
	exchange exchanger
	now      func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewOAuthTokenSource returns a TokenSource backed by SAS Logon Manager.
func NewOAuthTokenSource(cfg OAuthConfig) (TokenSource, error) {
	endpoint, err := normalizeEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	cfg.Endpoint = endpoint
	if cfg.ClientID == "" {
		return nil, errors.New("viya: OAuth client ID is required")
	}
	return &oauthTokenSource{
		cfg:      cfg,
		exchange: newExchanger(defaultRequestTimeout, cfg.Log),
		now:      time.Now,
	}, nil
}

func (s *oauthTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != "" && s.now().Before(s.expires) {
		return s.token, nil
	}

	token, lifetime, err := s.fetch(ctx)
	if err != nil {
		return "", err
	}
	s.token = token
	s.expires = s.now().Add(lifetime - tokenExpiryMargin)
	return s.token, nil
}

func (s *oauthTokenSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

func (s *oauthTokenSource) fetch(ctx context.Context) (string, time.Duration, error) {
	form := url.Values{"grant_type": {"client_credentials"}}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.cfg.Endpoint+tokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("viya: building token request: %w", err)
	}
	req.SetBasicAuth(s.cfg.ClientID, s.cfg.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if s.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", s.cfg.UserAgent)
	}

	status, _, body, err := s.exchange.do(ctx, req)
	if err != nil {
		return "", 0, fmt.Errorf("viya: requesting token: %w", err)
	}
	if status != http.StatusOK {
		return "", 0, fmt.Errorf("viya: authentication failed: %w",
			newAPIError(http.MethodPost, tokenPath, status, body))
	}

	var decoded tokenResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", 0, fmt.Errorf("viya: decoding token response: %w", err)
	}
	if decoded.AccessToken == "" {
		return "", 0, errors.New("viya: token response has no access_token")
	}
	return decoded.AccessToken, time.Duration(decoded.ExpiresIn) * time.Second, nil
}
