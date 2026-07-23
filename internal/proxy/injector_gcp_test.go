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
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTokenRequest(t *testing.T, contentType, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", contentType)
	return req
}

func TestIsStubTokenRequest(t *testing.T) {
	const formType = "application/x-www-form-urlencoded"

	tests := []struct {
		name        string
		contentType string
		body        string
		want        bool
	}{
		{
			name:        "stub ADC refresh (form)",
			contentType: formType,
			body:        "client_id=stub.apps.googleusercontent.com&client_secret=stub&grant_type=refresh_token&refresh_token=proxy-managed-token",
			want:        true,
		},
		{
			name:        "stub refresh token alone (form)",
			contentType: formType,
			body:        "grant_type=refresh_token&refresh_token=proxy-managed-token",
			want:        true,
		},
		{
			name:        "stub ADC refresh (json)",
			contentType: "application/json",
			body:        `{"client_id":"stub.apps.googleusercontent.com","client_secret":"stub","grant_type":"refresh_token","refresh_token":"proxy-managed-token"}`,
			want:        true,
		},
		{
			name:        "workload-owned refresh token (form)",
			contentType: formType,
			body:        "client_id=1234.apps.googleusercontent.com&client_secret=real&grant_type=refresh_token&refresh_token=1//04-real-user-refresh-token",
			want:        false,
		},
		{
			name:        "authorization code exchange (form)",
			contentType: formType,
			body:        "client_id=1234.apps.googleusercontent.com&client_secret=real&grant_type=authorization_code&code=4/abc&redirect_uri=http://localhost:1455",
			want:        false,
		},
		{
			name:        "empty body",
			contentType: formType,
			body:        "",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newTokenRequest(t, tt.contentType, tt.body)
			assert.Equal(t, tt.want, isStubTokenRequest(req))

			// The body must be readable again so a non-stub request can be
			// forwarded upstream intact.
			rest, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			assert.Equal(t, tt.body, string(rest))
		})
	}
}

func TestWorkloadAuthorization(t *testing.T) {
	gcpRoute := &Route{Domain: ".googleapis.com", Injector: "gcp"}
	bearerRoute := &Route{Domain: "api.example.com", Injector: "bearer"}

	tests := []struct {
		name  string
		route *Route
		auth  string
		want  string
	}{
		{
			name:  "workload bearer on gcp route is preserved",
			route: gcpRoute,
			auth:  "Bearer ya29.user-owned-token",
			want:  "Bearer ya29.user-owned-token",
		},
		{
			name:  "vended dummy on gcp route is not preserved",
			route: gcpRoute,
			auth:  "Bearer " + VendedAccessToken,
			want:  "",
		},
		{
			name:  "absent auth on gcp route",
			route: gcpRoute,
			auth:  "",
			want:  "",
		},
		{
			name:  "workload bearer on non-gcp route is not preserved",
			route: bearerRoute,
			auth:  "Bearer some-token",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, workloadAuthorization(tt.route, tt.auth))
		})
	}
}

func TestGCPInjectSkipsWorkloadOwnedAuthorization(t *testing.T) {
	// SAFilePath points nowhere: if Inject tries to mint a token it errors,
	// proving which path it took.
	inj, err := NewGCPInjector(&Route{Injector: "gcp", SAFilePath: "/nonexistent/sa-key.json"})
	require.NoError(t, err)

	t.Run("existing workload Authorization is left untouched", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "https://www.googleapis.com/calendar/v3/calendars/primary/events", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer ya29.user-owned-token")

		require.NoError(t, inj.Inject(req))
		assert.Equal(t, "Bearer ya29.user-owned-token", req.Header.Get("Authorization"))
	})

	t.Run("absent Authorization still goes through SA minting", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "https://aiplatform.googleapis.com/v1/projects", nil)
		require.NoError(t, err)

		err = inj.Inject(req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sa-key.json")
	})
}
