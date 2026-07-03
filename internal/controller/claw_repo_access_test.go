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

func TestGitHubRepoAccessCredentials(t *testing.T) {
	instance := &clawv1alpha1.Claw{}
	instance.Spec.RepoAccess = &clawv1alpha1.RepoAccessSpec{
		GitHub: &clawv1alpha1.GitHubRepoAccessSpec{
			SecretRef:           clawv1alpha1.SecretRefEntry{Name: "github-pat", Key: "token"},
			AllowedRepositories: []string{"sallyom/clawboard"},
		},
	}

	creds := githubRepoAccessCredentials(instance)
	require.Len(t, creds, 2)

	assert.Equal(t, githubAPIRepoAccessCredentialName, creds[0].Name)
	assert.Equal(t, clawv1alpha1.CredentialTypeBearer, creds[0].Type)
	assert.Equal(t, githubAPIDomain, creds[0].Domain)
	assert.Equal(t, []clawv1alpha1.SecretRefEntry{{Name: "github-pat", Key: "token"}}, creds[0].SecretRef)

	assert.Equal(t, githubGitRepoAccessCredentialName, creds[1].Name)
	assert.Equal(t, credentialTypeBasic, creds[1].Type)
	assert.Equal(t, githubGitDomain, creds[1].Domain)
	assert.Equal(t, []string{"/sallyom/clawboard/", "/sallyom/clawboard.git/"}, creds[1].AllowedPaths)
}

func TestGitHubRepoAccessSkipsExplicitGitHubCredentials(t *testing.T) {
	instance := &clawv1alpha1.Claw{}
	instance.Spec.Credentials = []clawv1alpha1.CredentialSpec{
		{
			Name:   "github",
			Type:   clawv1alpha1.CredentialTypeBearer,
			Domain: githubAPIDomain,
		},
	}
	instance.Spec.RepoAccess = &clawv1alpha1.RepoAccessSpec{
		GitHub: &clawv1alpha1.GitHubRepoAccessSpec{
			SecretRef: clawv1alpha1.SecretRefEntry{Name: "github-pat", Key: "token"},
		},
	}

	creds := githubRepoAccessCredentials(instance)
	require.Len(t, creds, 1)
	assert.Equal(t, githubGitRepoAccessCredentialName, creds[0].Name)
}

func TestGitHubRepoAccessProxyRoutes(t *testing.T) {
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

	var apiRoute, gitRoute *proxyRoute
	for i := range cfg.Routes {
		switch cfg.Routes[i].Domain {
		case githubAPIDomain:
			apiRoute = &cfg.Routes[i]
		case githubGitDomain:
			gitRoute = &cfg.Routes[i]
		}
	}
	require.NotNil(t, apiRoute)
	assert.Equal(t, injectorBearer, apiRoute.Injector)
	assert.Equal(t, "CRED_GITHUB_API", apiRoute.EnvVar)

	require.NotNil(t, gitRoute)
	assert.Equal(t, injectorBasic, gitRoute.Injector)
	assert.Equal(t, "CRED_GITHUB_GIT", gitRoute.EnvVar)
	assert.Equal(t, githubBasicUsername, gitRoute.BasicUsername)
}

func TestConfigureGatewayForRepoAccess(t *testing.T) {
	objects := []*unstructured.Unstructured{
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
	instance := &clawv1alpha1.Claw{}
	instance.Name = testInstanceName
	instance.Spec.RepoAccess = &clawv1alpha1.RepoAccessSpec{
		GitHub: &clawv1alpha1.GitHubRepoAccessSpec{
			SecretRef: clawv1alpha1.SecretRefEntry{Name: "github-pat", Key: "token"},
			ExposeEnv: true,
		},
	}

	require.NoError(t, configureGatewayForRepoAccess(objects, instance))

	containers, _, _ := unstructured.NestedSlice(objects[0].Object, "spec", "template", "spec", "containers")
	envVars := containers[0].(map[string]any)["env"].([]any)
	require.Len(t, envVars, 2)
	assert.Equal(t, "GH_TOKEN", envVars[0].(map[string]any)["name"])
	assert.Equal(t, "GITHUB_TOKEN", envVars[1].(map[string]any)["name"])
	for _, e := range envVars {
		secretKeyRef := e.(map[string]any)["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)
		assert.Equal(t, "github-pat", secretKeyRef["name"])
		assert.Equal(t, "token", secretKeyRef["key"])
	}
}

func TestConfigureGatewayForRepoAccessNoEnvByDefault(t *testing.T) {
	instance := &clawv1alpha1.Claw{}
	instance.Spec.RepoAccess = &clawv1alpha1.RepoAccessSpec{
		GitHub: &clawv1alpha1.GitHubRepoAccessSpec{
			SecretRef: clawv1alpha1.SecretRefEntry{Name: "github-pat", Key: "token"},
		},
	}

	assert.Empty(t, githubRepoAccessEnvFrom(instance))
}
