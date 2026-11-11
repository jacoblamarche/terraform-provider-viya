package viya

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestNewAPIError(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body string
		want string
	}{
		"SAS error body": {
			body: `{"errorCode":1234,"message":"The group is invalid.","details":["name is required"],"remediation":"Set a name.","httpStatusCode":400}`,
			want: "POST /identities/groups: HTTP 400 Bad Request: The group is invalid. (name is required) Remediation: Set a name.",
		},
		"OAuth error body": {
			body: `{"error":"unauthorized","error_description":"Bad credentials"}`,
			want: "POST /identities/groups: HTTP 400 Bad Request: Bad credentials",
		},
		"body that is not JSON": {
			body: "<html><body>Bad Gateway</body></html>",
			want: "POST /identities/groups: HTTP 400 Bad Request: <html><body>Bad Gateway</body></html>",
		},
		"empty body": {
			body: "",
			want: "POST /identities/groups: HTTP 400 Bad Request",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := newAPIError(http.MethodPost, "/identities/groups", http.StatusBadRequest, []byte(tc.body))
			if got := err.Error(); got != tc.want {
				t.Errorf("Error() =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

func TestStatusHelpers(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("reading group: %w", &APIError{StatusCode: http.StatusNotFound})
	if !IsNotFound(wrapped) {
		t.Error("IsNotFound is false for a wrapped 404")
	}
	if IsConflict(wrapped) || IsPreconditionFailed(wrapped) {
		t.Error("a 404 was reported as a conflict or a failed precondition")
	}
	if IsNotFound(errors.New("some other error")) || IsNotFound(nil) {
		t.Error("IsNotFound is true for an error that is not an API error")
	}
}
