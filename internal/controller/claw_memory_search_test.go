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

package controller

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clawv1alpha1 "github.com/codeready-toolchain/claw-operator/api/v1alpha1"
)

func memorySearchFromConfig(t *testing.T, config map[string]any) map[string]any {
	t.Helper()
	memory, ok := config["memory"].(map[string]any)
	require.True(t, ok, "config should contain 'memory' key")
	ms, ok := memory["search"].(map[string]any)
	require.True(t, ok, "memory should contain 'search' key as map, got %T", memory["search"])
	return ms
}

func TestInjectMemorySearch(t *testing.T) {
	tests := []struct {
		name        string
		credentials []clawv1alpha1.CredentialSpec
		userConfig  map[string]any
		wantKey     string
		wantValue   any
	}{
		{
			name: "OpenAI credential sets provider to openai",
			credentials: []clawv1alpha1.CredentialSpec{
				{Name: "openai", Type: clawv1alpha1.CredentialTypeBearer, Provider: "openai"},
			},
			wantKey:   "provider",
			wantValue: "openai",
		},
		{
			name: "Google apiKey credential sets provider to gemini",
			credentials: []clawv1alpha1.CredentialSpec{
				{Name: "gemini", Type: clawv1alpha1.CredentialTypeAPIKey, Provider: "google"},
			},
			wantKey:   "provider",
			wantValue: "gemini",
		},
		{
			name: "Anthropic-only disables memory search",
			credentials: []clawv1alpha1.CredentialSpec{
				{Name: "claude", Type: clawv1alpha1.CredentialTypeAPIKey, Provider: "anthropic"},
			},
			wantKey:   "enabled",
			wantValue: false,
		},
		{
			name: "Google GCP (Vertex AI) is skipped",
			credentials: []clawv1alpha1.CredentialSpec{
				{
					Name: "gemini-vertex", Type: clawv1alpha1.CredentialTypeGCP, Provider: "google",
					GCP: &clawv1alpha1.GCPConfig{Project: "p", Location: "us-east5"},
				},
			},
			wantKey:   "enabled",
			wantValue: false,
		},
		{
			name: "xAI-only disables memory search",
			credentials: []clawv1alpha1.CredentialSpec{
				{Name: "grok", Type: clawv1alpha1.CredentialTypeBearer, Provider: "xai"},
			},
			wantKey:   "enabled",
			wantValue: false,
		},
		{
			name:      "no credentials disables memory search",
			wantKey:   "enabled",
			wantValue: false,
		},
		{
			name: "multiple credentials picks first embedding-capable",
			credentials: []clawv1alpha1.CredentialSpec{
				{Name: "claude", Type: clawv1alpha1.CredentialTypeAPIKey, Provider: "anthropic"},
				{Name: "openai", Type: clawv1alpha1.CredentialTypeBearer, Provider: "openai"},
			},
			wantKey:   "provider",
			wantValue: "openai",
		},
		{
			name: "OpenAI before Google picks OpenAI",
			credentials: []clawv1alpha1.CredentialSpec{
				{Name: "openai", Type: clawv1alpha1.CredentialTypeBearer, Provider: "openai"},
				{Name: "gemini", Type: clawv1alpha1.CredentialTypeAPIKey, Provider: "google"},
			},
			wantKey:   "provider",
			wantValue: "openai",
		},
		{
			name: "Google before OpenAI picks Google",
			credentials: []clawv1alpha1.CredentialSpec{
				{Name: "gemini", Type: clawv1alpha1.CredentialTypeAPIKey, Provider: "google"},
				{Name: "openai", Type: clawv1alpha1.CredentialTypeBearer, Provider: "openai"},
			},
			wantKey:   "provider",
			wantValue: "gemini",
		},
		{
			name: "GCP credential skipped, falls through to OpenAI",
			credentials: []clawv1alpha1.CredentialSpec{
				{
					Name: "gemini-vertex", Type: clawv1alpha1.CredentialTypeGCP, Provider: "google",
					GCP: &clawv1alpha1.GCPConfig{Project: "p", Location: "us-east5"},
				},
				{Name: "openai", Type: clawv1alpha1.CredentialTypeBearer, Provider: "openai"},
			},
			wantKey:   "provider",
			wantValue: "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := map[string]any{}
			instance := testClawWithCredentials(tt.credentials)

			injectMemorySearch(config, instance)

			ms := memorySearchFromConfig(t, config)
			assert.Equal(t, tt.wantValue, ms[tt.wantKey])
		})
	}
}

func TestInjectMemorySearchUserOverride(t *testing.T) {
	t.Run("user memory.search.provider skips injection", func(t *testing.T) {
		config := map[string]any{
			"memory": map[string]any{
				"search": map[string]any{
					"provider": "custom",
				},
			},
		}
		instance := testClawWithCredentials([]clawv1alpha1.CredentialSpec{
			{Name: "openai", Type: clawv1alpha1.CredentialTypeBearer, Provider: "openai"},
		})

		injectMemorySearch(config, instance)

		ms := memorySearchFromConfig(t, config)
		assert.Equal(t, "custom", ms["provider"])
		assert.NotContains(t, ms, "enabled")
	})

	t.Run("user memory.search.enabled false is respected", func(t *testing.T) {
		config := map[string]any{
			"memory": map[string]any{
				"search": map[string]any{
					"enabled": false,
				},
			},
		}
		instance := testClawWithCredentials([]clawv1alpha1.CredentialSpec{
			{Name: "openai", Type: clawv1alpha1.CredentialTypeBearer, Provider: "openai"},
		})

		injectMemorySearch(config, instance)

		ms := memorySearchFromConfig(t, config)
		assert.Equal(t, false, ms["enabled"])
		assert.NotContains(t, ms, "provider")
	})
}

func TestInjectMemorySearchLegacyConfig(t *testing.T) {
	instance := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: namespace},
		Spec: clawv1alpha1.ClawSpec{Credentials: []clawv1alpha1.CredentialSpec{
			{Name: "openai", Type: clawv1alpha1.CredentialTypeBearer, Provider: "openai"},
		}},
	}
	config := map[string]any{}

	injectMemorySearchForGeneration(config, instance, legacyConfigGeneration)

	defaults := config["agents"].(map[string]any)["defaults"].(map[string]any)
	legacySearch := defaults["memorySearch"].(map[string]any)
	assert.Equal(t, "openai", legacySearch["provider"])
	assert.NotContains(t, config, "memory")
}

func TestInjectMemorySearchJSONRoundTrip(t *testing.T) {
	const baseJSON = `{"gateway": {"port": 18789}, "models": {"providers": {}}}`

	t.Run("should produce valid JSON after memory search injection", func(t *testing.T) {
		var config map[string]any
		require.NoError(t, json.Unmarshal([]byte(baseJSON), &config))

		instance := testClawWithCredentials([]clawv1alpha1.CredentialSpec{
			{Name: "openai", Type: clawv1alpha1.CredentialTypeBearer, Provider: "openai"},
		})
		injectMemorySearch(config, instance)

		result, err := json.Marshal(config)
		require.NoError(t, err)

		var roundTripped map[string]any
		require.NoError(t, json.Unmarshal(result, &roundTripped))

		ms := memorySearchFromConfig(t, roundTripped)
		assert.Equal(t, "openai", ms["provider"])
	})

	t.Run("should produce valid JSON with deep-merge user config override", func(t *testing.T) {
		userJSON := `{
			"memory": {
				"search": {
						"provider": "openai-compatible",
						"model": "custom-embedding",
						"remote": {"baseUrl": "http://local/v1", "apiKey": "placeholder"}
				}
			}
		}`

		var base map[string]any
		require.NoError(t, json.Unmarshal([]byte(baseJSON), &base))
		var user map[string]any
		require.NoError(t, json.Unmarshal([]byte(userJSON), &user))

		config := deepMerge(base, user)

		instance := testClawWithCredentials([]clawv1alpha1.CredentialSpec{
			{Name: "openai", Type: clawv1alpha1.CredentialTypeBearer, Provider: "openai"},
		})
		injectMemorySearch(config, instance)

		result, err := json.Marshal(config)
		require.NoError(t, err)

		var roundTripped map[string]any
		require.NoError(t, json.Unmarshal(result, &roundTripped))

		ms := memorySearchFromConfig(t, roundTripped)
		assert.Equal(t, "openai-compatible", ms["provider"],
			"user override should be preserved, not replaced with openai")
		assert.Equal(t, "custom-embedding", ms["model"])
	})

	t.Run("should disable when no embedding provider after deep-merge", func(t *testing.T) {
		var config map[string]any
		require.NoError(t, json.Unmarshal([]byte(baseJSON), &config))

		instance := testClawWithCredentials([]clawv1alpha1.CredentialSpec{
			{Name: "claude", Type: clawv1alpha1.CredentialTypeAPIKey, Provider: "anthropic"},
		})
		injectMemorySearch(config, instance)

		result, err := json.Marshal(config)
		require.NoError(t, err)

		var roundTripped map[string]any
		require.NoError(t, json.Unmarshal(result, &roundTripped))

		ms := memorySearchFromConfig(t, roundTripped)
		assert.Equal(t, false, ms["enabled"])
	})
}

func TestUserHasMemorySearchConfig(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]any
		want   bool
	}{
		{
			name:   "empty config",
			config: map[string]any{},
			want:   false,
		},
		{
			name:   "memory without search",
			config: map[string]any{"memory": map[string]any{}},
			want:   false,
		},
		{
			name: "memory without search config",
			config: map[string]any{
				"memory": map[string]any{"other": map[string]any{}},
			},
			want: false,
		},
		{
			name: "memory.search present as map",
			config: map[string]any{
				"memory": map[string]any{"search": map[string]any{"provider": "openai"}},
			},
			want: true,
		},
		{
			name: "memory.search present as bool",
			config: map[string]any{
				"memory": map[string]any{"search": false},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, userHasMemorySearchConfig(tt.config, true))
		})
	}

	t.Run("legacy agents.defaults.memorySearch", func(t *testing.T) {
		config := map[string]any{
			"agents": map[string]any{"defaults": map[string]any{"memorySearch": map[string]any{"enabled": false}}},
		}
		assert.True(t, userHasMemorySearchConfig(config, false))
	})
}
