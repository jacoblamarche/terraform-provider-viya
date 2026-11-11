// Package viya is a client for the SAS Viya REST APIs used by the Terraform
// provider. It handles authentication against SAS Logon Manager, optimistic
// concurrency with entity tags, retries of transient failures, and decoding
// of Viya error responses.
package viya

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseBytes = 10 << 20

	defaultMaxRetries   = 3
	defaultMinRetryWait = 500 * time.Millisecond
	defaultMaxRetryWait = 10 * time.Second
)

// Config configures a Client.
type Config struct {
	// Endpoint is the base URL of the Viya deployment, for example
	// https://viya.example.com. It is required.
	Endpoint    string
	TokenSource TokenSource
	// HTTPClient performs the requests. Defaults to http.DefaultClient.
	HTTPClient *http.Client
	UserAgent  string
	// MaxRetries is the number of times a transient failure is retried.
	// Zero selects the default of 3; a negative value disables retries.
	MaxRetries int
	// RetryWaitMin and RetryWaitMax bound the exponential backoff between
	// retries. Zero selects the defaults of 500ms and 10s.
	RetryWaitMin time.Duration
	RetryWaitMax time.Duration
}

// Client calls the SAS Viya REST APIs. It is safe for concurrent use.
type Client struct {
	endpoint     string
	tokens       TokenSource
	http         *http.Client
	userAgent    string
	maxRetries   int
	retryWaitMin time.Duration
	retryWaitMax time.Duration
}

// NewClient returns a Client for the given configuration.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("viya: endpoint is required")
	}
	parsed, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("viya: invalid endpoint: %w", err)
	}
	if (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, fmt.Errorf("viya: endpoint %q must be an http(s) URL with a host", cfg.Endpoint)
	}
	if cfg.TokenSource == nil {
		return nil, errors.New("viya: a token source is required")
	}

	client := &Client{
		endpoint:     strings.TrimRight(cfg.Endpoint, "/"),
		tokens:       cfg.TokenSource,
		http:         cfg.HTTPClient,
		userAgent:    cfg.UserAgent,
		maxRetries:   cfg.MaxRetries,
		retryWaitMin: cfg.RetryWaitMin,
		retryWaitMax: cfg.RetryWaitMax,
	}
	if client.http == nil {
		client.http = http.DefaultClient
	}
	switch {
	case client.maxRetries == 0:
		client.maxRetries = defaultMaxRetries
	case client.maxRetries < 0:
		client.maxRetries = 0
	}
	if client.retryWaitMin <= 0 {
		client.retryWaitMin = defaultMinRetryWait
	}
	if client.retryWaitMax <= 0 {
		client.retryWaitMax = defaultMaxRetryWait
	}
	return client, nil
}

type request struct {
	method string
	path   string
	query  url.Values
	body   any
	// contentType and accept are the media types of the request and of the
	// expected response. Both default to application/json.
	contentType string
	accept      string
	ifMatch     string
}

// do sends a request and decodes a successful JSON response into out, when
// out is not nil. It returns the response headers.
//
// Transient failures are retried with exponential backoff, and an HTTP 401
// triggers one retry with a new token.
func (c *Client) do(ctx context.Context, r request, out any) (http.Header, error) {
	var payload []byte
	if r.body != nil {
		var err error
		if payload, err = json.Marshal(r.body); err != nil {
			return nil, fmt.Errorf("viya: encoding request body: %w", err)
		}
	}

	reauthenticated := false
	for attempt := 0; ; attempt++ {
		status, header, body, err := c.send(ctx, r, payload)

		if err == nil && status == http.StatusUnauthorized && !reauthenticated {
			// The token may have expired or been revoked: get a new one and
			// try again, once, without consuming a retry.
			c.tokens.Invalidate()
			reauthenticated = true
			attempt--
			continue
		}

		if err == nil && status >= 200 && status < 300 {
			if out != nil && len(body) > 0 {
				if err := json.Unmarshal(body, out); err != nil {
					return nil, fmt.Errorf("viya: decoding response of %s %s: %w", r.method, r.path, err)
				}
			}
			return header, nil
		}

		if attempt < c.maxRetries && shouldRetry(r.method, status, err) {
			if waitErr := c.wait(ctx, attempt, header); waitErr != nil {
				return nil, waitErr
			}
			continue
		}

		if err != nil {
			return nil, err
		}
		return header, newAPIError(r.method, r.path, status, body)
	}
}

func (c *Client) send(ctx context.Context, r request, payload []byte) (int, http.Header, []byte, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return 0, nil, nil, err
	}

	target := c.endpoint + r.path
	if len(r.query) > 0 {
		target += "?" + r.query.Encode()
	}

	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, target, reader)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("viya: building request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", orDefault(r.accept, "application/json"))
	if payload != nil {
		req.Header.Set("Content-Type", orDefault(r.contentType, "application/json"))
	}
	if r.ifMatch != "" {
		req.Header.Set("If-Match", r.ifMatch)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("viya: %s %s: %w", r.method, r.path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("viya: reading response of %s %s: %w", r.method, r.path, err)
	}
	return resp.StatusCode, resp.Header, body, nil
}

// shouldRetry reports whether a failed exchange is worth repeating.
//
// HTTP 429 and 503 mean the service refused the request before acting on it,
// so they are retried for every method. Gateway errors and transport errors
// leave the outcome unknown, so they are retried only for idempotent methods.
func shouldRetry(method string, status int, err error) bool {
	idempotent := method != http.MethodPost && method != http.MethodPatch

	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		return idempotent
	}

	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	case http.StatusBadGateway, http.StatusGatewayTimeout:
		return idempotent
	default:
		return false
	}
}

// wait sleeps before the next retry. It honours a Retry-After header given
// in seconds, bounded by the maximum backoff.
func (c *Client) wait(ctx context.Context, attempt int, header http.Header) error {
	delay := c.retryWaitMin << attempt
	if delay <= 0 || delay > c.retryWaitMax {
		delay = c.retryWaitMax
	}
	if header != nil {
		if seconds, err := strconv.Atoi(header.Get("Retry-After")); err == nil && seconds > 0 {
			delay = min(time.Duration(seconds)*time.Second, c.retryWaitMax)
		}
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
