// Package viyamock is an in-memory fake of the SAS Viya REST APIs that the
// Terraform provider uses.
//
// It is stateful: resources that are created can be read, updated and
// deleted, which is what Terraform's plan/apply cycle needs. It also
// reproduces the conventions that matter to a client: OAuth2 tokens issued
// by SAS Logon Manager, entity tags with If-Match preconditions, SAS media
// types, paginated collections and SAS error bodies.
//
// The fake is not a substitute for testing against a real deployment. See
// the README next to this file for what it covers and how its behaviour was
// established.
package viyamock

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Default credentials accepted by the fake.
const (
	DefaultClientID     = "viya-mock-client"
	DefaultClientSecret = "viya-mock-secret"

	defaultTokenTTL = time.Hour
)

const mediaTypeError = "application/vnd.sas.error+json"

// Server is a fake Viya deployment. Create one with New. It implements
// http.Handler and is safe for concurrent use.
type Server struct {
	tokenTTL       time.Duration
	requireIfMatch bool
	now            func() time.Time
	mux            *http.ServeMux

	mu             sync.Mutex
	tokens         map[string]time.Time // token -> expiry
	groups         map[string]*Group
	definitions    map[string]ConfigurationDefinition
	configurations map[string]*Configuration
	revision       int64
	faults         []*Fault
	requests       []Request
}

// Option customises a Server.
type Option func(*Server)

// WithTokenTTL sets the lifetime of the tokens that the fake issues.
func WithTokenTTL(ttl time.Duration) Option {
	return func(s *Server) { s.tokenTTL = ttl }
}

// WithRequireIfMatch makes configuration updates fail with HTTP 428 when the
// request has no If-Match header. SAS Viya accepts them; the option proves
// that the provider always sends the precondition.
func WithRequireIfMatch() Option {
	return func(s *Server) { s.requireIfMatch = true }
}

// WithClock replaces the clock, which controls token expiry and timestamps.
func WithClock(now func() time.Time) Option {
	return func(s *Server) { s.now = now }
}

// New returns a fake Viya deployment with no resources.
func New(opts ...Option) *Server {
	s := &Server{
		tokenTTL: defaultTokenTTL,
		now:      time.Now,
		tokens:   make(map[string]time.Time),
		groups:   make(map[string]*Group),

		definitions:    make(map[string]ConfigurationDefinition),
		configurations: make(map[string]*Configuration),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.seedSampleConfigurations()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /SASLogon/oauth/token", s.handleToken)
	mux.HandleFunc("POST /identities/groups", s.authenticated(s.handleCreateGroup))
	mux.HandleFunc("POST /identities/groups/{$}", s.authenticated(s.handleCreateGroup))
	mux.HandleFunc("GET /identities/groups/{id}", s.authenticated(s.handleGetGroup))
	mux.HandleFunc("PUT /identities/groups/{id}", s.authenticated(s.handleUpdateGroup))
	mux.HandleFunc("DELETE /identities/groups/{id}", s.authenticated(s.handleDeleteGroup))
	mux.HandleFunc("GET /configuration/definitions", s.authenticated(s.handleListDefinitions))
	mux.HandleFunc("GET /configuration/configurations", s.authenticated(s.handleListConfigurations))
	mux.HandleFunc("POST /configuration/configurations", s.authenticated(s.handleCreateConfiguration))
	mux.HandleFunc("GET /configuration/configurations/{id}", s.authenticated(s.handleGetConfiguration))
	mux.HandleFunc("PUT /configuration/configurations/{id}", s.authenticated(s.handleUpdateConfiguration))
	mux.HandleFunc("DELETE /configuration/configurations/{id}", s.authenticated(s.handleDeleteConfiguration))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "The mock does not implement this endpoint.")
	})
	s.mux = mux
	return s
}

// Start serves the fake on a local port and returns the running server. The
// caller must Close it.
func (s *Server) Start() *httptest.Server {
	return httptest.NewServer(s)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path})
	fault := s.takeFault(r)
	s.mu.Unlock()

	if fault != nil {
		if fault.Delay > 0 {
			select {
			case <-time.After(fault.Delay):
			case <-r.Context().Done():
				return
			}
		}
		if fault.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(fault.RetryAfter))
		}
		writeError(w, r, fault.Status, "Fault injected by the mock.")
		return
	}
	s.mux.ServeHTTP(w, r)
}

// Request is a request that the fake received.
type Request struct {
	Method string
	Path   string
}

// Requests returns the requests received so far, oldest first.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Fault makes matching requests fail without reaching their handler.
type Fault struct {
	// Method restricts the fault to one HTTP method. Empty matches any.
	Method string
	// PathPrefix restricts the fault to paths with this prefix. Empty
	// matches any.
	PathPrefix string
	Status     int
	// RetryAfter is sent as the Retry-After header, in seconds, when
	// positive.
	RetryAfter int
	// Delay is how long the fake stays silent before it answers.
	Delay time.Duration
	// Times is how many requests the fault affects. Zero means one.
	Times int
}

// InjectFault registers a fault. Faults are consumed in registration order.
func (s *Server) InjectFault(fault Fault) {
	if fault.Times <= 0 {
		fault.Times = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, &fault)
}

// takeFault consumes one use of the first fault matching r. It must be
// called with s.mu held.
func (s *Server) takeFault(r *http.Request) *Fault {
	for i, fault := range s.faults {
		if fault.Method != "" && fault.Method != r.Method {
			continue
		}
		if !strings.HasPrefix(r.URL.Path, fault.PathPrefix) {
			continue
		}
		fault.Times--
		if fault.Times == 0 {
			s.faults = append(s.faults[:i], s.faults[i+1:]...)
		}
		return fault
	}
	return nil
}

// RevokeTokens invalidates every token issued so far, as if they had
// expired or been revoked.
func (s *Server) RevokeTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.tokens)
}

func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

		s.mu.Lock()
		expiry, known := s.tokens[token]
		valid := found && known && s.now().Before(expiry)
		s.mu.Unlock()

		if !valid {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			writeError(w, r, http.StatusUnauthorized, "Full authentication is required to access this resource.")
			return
		}
		next(w, r)
	}
}

// nextETag returns a weak entity tag, as the Configuration service does. It
// must be called with s.mu held.
func (s *Server) nextETag() string {
	s.revision++
	return `W/"` + strconv.FormatInt(s.revision, 10) + `"`
}

type link struct {
	Method string `json:"method"`
	Rel    string `json:"rel"`
	Href   string `json:"href"`
	URI    string `json:"uri"`
	Type   string `json:"type,omitempty"`
}

// errorResponse mirrors application/vnd.sas.error+json.
type errorResponse struct {
	ErrorCode      int      `json:"errorCode"`
	Message        string   `json:"message"`
	Details        []string `json:"details"`
	Links          []link   `json:"links"`
	Version        int      `json:"version"`
	HTTPStatusCode int      `json:"httpStatusCode"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, message string) {
	writeJSON(w, status, mediaTypeError, errorResponse{
		Message:        message,
		Details:        []string{"path: " + r.URL.Path},
		Links:          []link{},
		Version:        2,
		HTTPStatusCode: status,
	})
}

func writeJSON(w http.ResponseWriter, status int, mediaType string, body any) {
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(status)
	// An encoding failure cannot be reported once the header is written, and
	// only happens if the client went away.
	_ = json.NewEncoder(w).Encode(body)
}

func randomHex(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("viyamock: reading random bytes: %v", err))
	}
	return hex.EncodeToString(buf)
}
