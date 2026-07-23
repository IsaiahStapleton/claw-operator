/*
Copyright 2026 Red Hat.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"golang.org/x/oauth2/google"
)

// GCPInjector loads a GCP credential JSON (service account or authorized user),
// obtains OAuth2 tokens, and injects Authorization: Bearer <token>.
// Tokens are cached and auto-refreshed.
// It also implements token vending: intercepts POST to oauth2.googleapis.com/token
// and returns a dummy token so Google SDK clients work with placeholder ADC credentials.
type GCPInjector struct {
	saFilePath     string
	defaultHeaders map[string]string

	mu          sync.Mutex
	tokenSource *google.Credentials
}

func NewGCPInjector(route *Route) (*GCPInjector, error) {
	if route.SAFilePath == "" {
		return nil, fmt.Errorf("gcp injector requires saFilePath")
	}
	return &GCPInjector{
		saFilePath:     route.SAFilePath,
		defaultHeaders: route.DefaultHeaders,
	}, nil
}

func (g *GCPInjector) getTokenSource(ctx context.Context) (*google.Credentials, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.tokenSource != nil {
		return g.tokenSource, nil
	}

	saJSON, err := os.ReadFile(g.saFilePath)
	if err != nil {
		return nil, fmt.Errorf("read SA key file %s: %w", g.saFilePath, err)
	}

	credType, err := detectCredentialType(saJSON)
	if err != nil {
		return nil, err
	}

	creds, err := google.CredentialsFromJSONWithType(ctx, saJSON, credType, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("parse GCP credentials: %w", err)
	}

	g.tokenSource = creds
	return creds, nil
}

func (g *GCPInjector) Inject(req *http.Request) error {
	// Token vending: intercept token endpoint requests from Google SDK clients
	if isTokenVendingRequest(req) {
		return nil // handled separately in the server's token vending path
	}

	// A workload-owned Authorization (restored by the server after header
	// stripping) takes precedence: the caller brought its own Google
	// credential, so the service-account token must not overwrite it.
	if req.Header.Get("Authorization") != "" {
		for k, v := range g.defaultHeaders {
			if strings.EqualFold(k, "Authorization") {
				continue
			}
			req.Header.Set(k, v)
		}
		return nil
	}

	creds, err := g.getTokenSource(req.Context())
	if err != nil {
		return fmt.Errorf("get GCP token source: %w", err)
	}

	token, err := creds.TokenSource.Token()
	if err != nil {
		return fmt.Errorf("obtain GCP token: %w", err)
	}

	for k, v := range g.defaultHeaders {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	return nil
}

const (
	// VendedAccessToken is the dummy access token returned to Google SDK
	// clients holding the operator's placeholder ADC credentials.
	VendedAccessToken = "claw-proxy-vended-token"
	// StubClientID and StubRefreshToken are the placeholder ADC credential
	// values the operator writes into the workload's adc.json. Token-endpoint
	// requests carrying them are answered with VendedAccessToken; all other
	// token requests belong to the workload and are forwarded to Google.
	StubClientID     = "stub.apps.googleusercontent.com"
	StubRefreshToken = "proxy-managed-token"
)

// isStubTokenRequest reports whether a token-endpoint request carries the
// operator's placeholder ADC credentials. The request body is restored so a
// non-stub request can still be forwarded upstream intact. Google auth
// libraries send the exchange form-encoded; JSON is accepted as a fallback.
func isStubTokenRequest(req *http.Request) bool {
	if req.Body == nil {
		return false
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) == 0 {
		return false
	}

	if vals, err := url.ParseQuery(string(body)); err == nil {
		if vals.Get("refresh_token") == StubRefreshToken || vals.Get("client_id") == StubClientID {
			return true
		}
	}

	var creds struct {
		ClientID     string `json:"client_id"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(body, &creds) == nil {
		return creds.RefreshToken == StubRefreshToken || creds.ClientID == StubClientID
	}
	return false
}

// workloadAuthorization returns the Authorization value to restore after
// StripAuthHeaders when the workload owns the credential. Only GCP routes
// support workload-owned credentials: SDKs holding the placeholder ADC send
// the vended dummy (replaced with a service-account token at injection),
// while anything else is a credential the workload obtained itself (e.g. a
// user OAuth token from a plugin's own consent flow) and must pass through.
func workloadAuthorization(route *Route, auth string) string {
	if route.Injector != injectorGCP {
		return ""
	}
	if auth == "" || auth == "Bearer "+VendedAccessToken {
		return ""
	}
	return auth
}

// isTokenVendingRequest checks if this is a POST to oauth2.googleapis.com/token.
// Host values are normalized to strip ports before comparison.
func isTokenVendingRequest(req *http.Request) bool {
	if req.Method != http.MethodPost || req.URL.Path != "/token" {
		return false
	}
	return hostnameOnly(req.Host) == "oauth2.googleapis.com" ||
		hostnameOnly(req.URL.Host) == "oauth2.googleapis.com"
}

func hostnameOnly(hostPort string) string {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return strings.ToLower(hostPort)
	}
	return strings.ToLower(host)
}

// allowedCredentialTypes is the set of GCP credential types the proxy accepts.
var allowedCredentialTypes = map[google.CredentialsType]bool{
	google.ServiceAccount: true,
	google.AuthorizedUser: true,
}

// detectCredentialType reads the "type" field from a GCP credential JSON and
// validates it against the allowed set. This avoids the deprecated unvalidated
// CredentialsFromJSON while still supporting both service accounts and user credentials.
func detectCredentialType(jsonData []byte) (google.CredentialsType, error) {
	var f struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(jsonData, &f); err != nil {
		return "", fmt.Errorf("parse GCP credential file: %w", err)
	}
	ct := google.CredentialsType(f.Type)
	if !allowedCredentialTypes[ct] {
		return "", fmt.Errorf("unsupported GCP credential type %q (expected service_account or authorized_user)", f.Type)
	}
	return ct, nil
}

// TokenVendingResponse returns a dummy access token for Google SDK client satisfaction.
func TokenVendingResponse() []byte {
	resp := map[string]any{
		"access_token": VendedAccessToken,
		"token_type":   "Bearer",
		"expires_in":   3600,
	}
	data, _ := json.Marshal(resp)
	return data
}
