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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	clawv1alpha1 "github.com/codeready-toolchain/claw-operator/api/v1alpha1"
)

func TestGitHubRepoAccess(t *testing.T) {
	t.Run("credentials", func(t *testing.T) {
		tests := []struct {
			name              string
			existing          []clawv1alpha1.CredentialSpec
			allowedRepos      []string
			wantNames         []string
			wantGitCredential bool
		}{
			{
				name:              "creates api and git credentials",
				allowedRepos:      []string{"sallyom/clawboard"},
				wantNames:         []string{githubAPIRepoAccessCredentialName, githubGitRepoAccessCredentialName},
				wantGitCredential: true,
			},
			{
				name: "skips explicit api github credential",
				existing: []clawv1alpha1.CredentialSpec{{
					Name:   "github",
					Type:   clawv1alpha1.CredentialTypeBearer,
					Domain: githubAPIDomain,
				}},
				wantNames:         []string{githubGitRepoAccessCredentialName},
				wantGitCredential: true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				instance := &clawv1alpha1.Claw{}
				instance.Spec.Credentials = tt.existing
				instance.Spec.RepoAccess = &clawv1alpha1.RepoAccessSpec{
					GitHub: &clawv1alpha1.GitHubRepoAccessSpec{
						SecretRef:           clawv1alpha1.SecretRefEntry{Name: "github-pat", Key: "token"},
						AllowedRepositories: tt.allowedRepos,
					},
				}

				creds := githubRepoAccessCredentials(instance)
				require.Len(t, creds, len(tt.wantNames))
				for i, want := range tt.wantNames {
					assert.Equal(t, want, creds[i].Name)
				}
				if len(creds) > 0 && creds[0].Name == githubAPIRepoAccessCredentialName {
					assert.Equal(t, clawv1alpha1.CredentialTypeBearer, creds[0].Type)
					assert.Equal(t, githubAPIDomain, creds[0].Domain)
					assert.Equal(t, []clawv1alpha1.SecretRefEntry{{Name: "github-pat", Key: "token"}}, creds[0].SecretRef)
				}
				if tt.wantGitCredential {
					gitCred := creds[len(creds)-1]
					assert.Equal(t, githubGitRepoAccessCredentialName, gitCred.Name)
					assert.Equal(t, clawv1alpha1.CredentialTypeBasic, gitCred.Type)
					assert.Equal(t, githubGitDomain, gitCred.Domain)
					require.NotNil(t, gitCred.Basic)
					assert.Equal(t, githubBasicUsername, gitCred.Basic.Username)
					if len(tt.allowedRepos) > 0 {
						assert.Equal(t, []string{"/sallyom/clawboard/", "/sallyom/clawboard.git/"}, gitCred.AllowedPaths)
					}
				}
			})
		}
	})

	t.Run("proxy routes", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{}
		instance.Spec.RepoAccess = &clawv1alpha1.RepoAccessSpec{
			GitHub: &clawv1alpha1.GitHubRepoAccessSpec{
				SecretRef: clawv1alpha1.SecretRefEntry{Name: "github-pat", Key: "token"},
			},
		}

		raw, err := generateProxyConfig(toResolved(githubRepoAccessCredentials(instance)), nil, nil, nil)
		require.NoError(t, err)

		var cfg proxyConfig
		require.NoError(t, json.Unmarshal(raw, &cfg))

		apiRoute := findRouteByDomain(t, cfg.Routes, githubAPIDomain)
		assert.Equal(t, injectorBearer, apiRoute.Injector)
		assert.Equal(t, "CRED_GITHUB_API", apiRoute.EnvVar)

		gitRoute := findRouteByDomain(t, cfg.Routes, githubGitDomain)
		assert.Equal(t, injectorBasic, gitRoute.Injector)
		assert.Equal(t, "CRED_GITHUB_GIT", gitRoute.EnvVar)
		assert.Equal(t, githubBasicUsername, gitRoute.BasicUsername)
	})
}

func TestConfigureGatewayForRepoAccess(t *testing.T) {
	tests := []struct {
		name      string
		exposeEnv bool
		wantEnv   []string
	}{
		{name: "exposes GitHub tokens when requested", exposeEnv: true, wantEnv: []string{"GH_TOKEN", "GITHUB_TOKEN"}},
		{name: "does not expose env by default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := &clawv1alpha1.Claw{}
			instance.Name = testInstanceName
			instance.Spec.RepoAccess = &clawv1alpha1.RepoAccessSpec{
				GitHub: &clawv1alpha1.GitHubRepoAccessSpec{
					SecretRef: clawv1alpha1.SecretRefEntry{Name: "github-pat", Key: "token"},
					ExposeEnv: tt.exposeEnv,
				},
			}
			if len(tt.wantEnv) == 0 {
				assert.Empty(t, githubRepoAccessEnvFrom(instance))
				return
			}

			objects := repoAccessGatewayObjects()
			require.NoError(t, configureGatewayForRepoAccess(objects, instance))

			containers, _, _ := unstructured.NestedSlice(objects[0].Object, "spec", "template", "spec", "containers")
			envVars := containers[0].(map[string]any)["env"].([]any)
			require.Len(t, envVars, len(tt.wantEnv))
			for i, e := range envVars {
				assert.Equal(t, tt.wantEnv[i], e.(map[string]any)["name"])
				secretKeyRef := e.(map[string]any)["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)
				assert.Equal(t, "github-pat", secretKeyRef["name"])
				assert.Equal(t, "token", secretKeyRef["key"])
			}
		})
	}
}

func repoAccessGatewayObjects() []*unstructured.Unstructured {
	return []*unstructured.Unstructured{
		{
			Object: map[string]any{
				"kind": "Deployment",
				"metadata": map[string]any{
					"name": getClawDeploymentName(testInstanceName),
				},
				"spec": map[string]any{
					"template": map[string]any{
						"spec": map[string]any{
							"containers": []any{
								map[string]any{
									"name": ClawGatewayContainerName,
									"env":  []any{},
								},
							},
						},
					},
				},
			},
		},
	}
}
