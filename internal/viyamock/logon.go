package viyamock

import (
	"crypto/subtle"
	"net/http"
)

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope"`
	JTI         string `json:"jti"`
}

type oauthError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "The request body is not a valid form.")
		return
	}

	clientID, clientSecret, basic := r.BasicAuth()
	if !basic {
		clientID, clientSecret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if !equal(clientID, DefaultClientID) || !equal(clientSecret, DefaultClientSecret) {
		w.Header().Set("WWW-Authenticate", `Basic realm="UAA/client"`)
		writeOAuthError(w, http.StatusUnauthorized, "unauthorized", "Bad credentials")
		return
	}

	if r.PostForm.Get("grant_type") != "client_credentials" {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"The mock supports the client_credentials grant only.")
		return
	}

	token := randomHex(32)
	s.mu.Lock()
	s.tokens[token] = s.now().Add(s.tokenTTL)
	s.mu.Unlock()

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, "application/json", tokenResponse{
		AccessToken: token,
		TokenType:   "bearer",
		ExpiresIn:   int64(s.tokenTTL.Seconds()),
		Scope:       "openid",
		JTI:         randomHex(16),
	})
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, "application/json", oauthError{Error: code, Description: description})
}

func equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
