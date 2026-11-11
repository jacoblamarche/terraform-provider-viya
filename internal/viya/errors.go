package viya

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// APIError is an error response returned by a SAS Viya REST service.
type APIError struct {
	StatusCode  int
	Method      string
	Path        string
	ErrorCode   int
	Message     string
	Details     []string
	Remediation string
}

// errorBody mirrors application/vnd.sas.error+json. SASLogon uses the OAuth2
// shape (error / error_description) instead, so both are decoded.
type errorBody struct {
	ErrorCode        int      `json:"errorCode"`
	Message          string   `json:"message"`
	Details          []string `json:"details"`
	Remediation      string   `json:"remediation"`
	OAuthError       string   `json:"error"`
	OAuthDescription string   `json:"error_description"`
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	if text := http.StatusText(e.StatusCode); text != "" {
		fmt.Fprintf(&b, " %s", text)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if len(e.Details) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(e.Details, "; "))
	}
	if e.Remediation != "" {
		fmt.Fprintf(&b, " Remediation: %s", e.Remediation)
	}
	return b.String()
}

func newAPIError(method, path string, status int, body []byte) *APIError {
	apiErr := &APIError{StatusCode: status, Method: method, Path: path}

	var decoded errorBody
	if err := json.Unmarshal(body, &decoded); err != nil {
		// Not JSON (for example an HTML page from an ingress): keep a short
		// excerpt so the failure is still diagnosable.
		apiErr.Message = excerpt(body, 200)
		return apiErr
	}

	apiErr.ErrorCode = decoded.ErrorCode
	apiErr.Message = decoded.Message
	apiErr.Details = decoded.Details
	apiErr.Remediation = decoded.Remediation
	if apiErr.Message == "" {
		switch {
		case decoded.OAuthDescription != "":
			apiErr.Message = decoded.OAuthDescription
		case decoded.OAuthError != "":
			apiErr.Message = decoded.OAuthError
		}
	}
	return apiErr
}

func excerpt(body []byte, limit int) string {
	text := strings.TrimSpace(string(body))
	if len(text) > limit {
		return text[:limit] + "…"
	}
	return text
}

// IsNotFound reports whether err is a Viya API error with HTTP status 404.
func IsNotFound(err error) bool {
	return hasStatus(err, http.StatusNotFound)
}

// IsConflict reports whether err is a Viya API error with HTTP status 409.
func IsConflict(err error) bool {
	return hasStatus(err, http.StatusConflict)
}

// IsPreconditionFailed reports whether err is a Viya API error with HTTP
// status 412, which means the resource changed since its ETag was read.
func IsPreconditionFailed(err error) bool {
	return hasStatus(err, http.StatusPreconditionFailed)
}

func hasStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}
