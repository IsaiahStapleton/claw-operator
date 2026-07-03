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
	"crypto/sha256"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	clawv1alpha1 "github.com/codeready-toolchain/claw-operator/api/v1alpha1"
)

const (
	githubAPIRepoAccessCredentialName = "github-api"
	githubGitRepoAccessCredentialName = "github-git"
	githubAPIDomain                   = "api.github.com"
	githubGitDomain                   = "github.com"
	githubBasicUsername               = "x-access-token"
)

func repoAccessEnabled(flag *bool) bool {
	return flag == nil || *flag
}

func githubRepoAccess(instance *clawv1alpha1.Claw) *clawv1alpha1.GitHubRepoAccessSpec {
	if instance.Spec.RepoAccess == nil {
		return nil
	}
	return instance.Spec.RepoAccess.GitHub
}

func hasCredentialName(credentials []clawv1alpha1.CredentialSpec, name string) bool {
	for _, cred := range credentials {
		if cred.Name == name {
			return true
		}
	}
	return false
}

func hasCredentialDomain(credentials []clawv1alpha1.CredentialSpec, domain string) bool {
	for _, cred := range credentials {
		if strings.EqualFold(cred.Domain, domain) {
			return true
		}
	}
	return false
}

func githubAllowedPaths(repositories []string) []string {
	if len(repositories) == 0 {
		return nil
	}
	paths := make([]string, 0, len(repositories)*2)
	seen := make(map[string]bool, len(repositories)*2)
	for _, repo := range repositories {
		repo = strings.Trim(strings.TrimSpace(repo), "/")
		if repo == "" {
			continue
		}
		for _, path := range []string{"/" + repo + "/", "/" + repo + ".git/"} {
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	return paths
}

func githubRepoAccessCredentials(instance *clawv1alpha1.Claw) []clawv1alpha1.CredentialSpec {
	gh := githubRepoAccess(instance)
	if gh == nil {
		return nil
	}

	var credentials []clawv1alpha1.CredentialSpec
	if repoAccessEnabled(gh.EnableAPIProxy) &&
		!hasCredentialName(instance.Spec.Credentials, githubAPIRepoAccessCredentialName) &&
		!hasCredentialDomain(instance.Spec.Credentials, githubAPIDomain) {
		credentials = append(credentials, clawv1alpha1.CredentialSpec{
			Name:      githubAPIRepoAccessCredentialName,
			Type:      clawv1alpha1.CredentialTypeBearer,
			SecretRef: []clawv1alpha1.SecretRefEntry{gh.SecretRef},
			Domain:    githubAPIDomain,
		})
	}
	if repoAccessEnabled(gh.EnableGitHTTPS) &&
		!hasCredentialName(instance.Spec.Credentials, githubGitRepoAccessCredentialName) &&
		!hasCredentialDomain(instance.Spec.Credentials, githubGitDomain) {
		credentials = append(credentials, clawv1alpha1.CredentialSpec{
			Name:         githubGitRepoAccessCredentialName,
			Type:         clawv1alpha1.CredentialTypeBasic,
			SecretRef:    []clawv1alpha1.SecretRefEntry{gh.SecretRef},
			Domain:       githubGitDomain,
			Basic:        &clawv1alpha1.BasicAuthConfig{Username: githubBasicUsername},
			AllowedPaths: githubAllowedPaths(gh.AllowedRepositories),
		})
	}
	return credentials
}

func githubRepoAccessEnvFrom(instance *clawv1alpha1.Claw) []clawv1alpha1.McpEnvFromSecret {
	gh := githubRepoAccess(instance)
	if gh == nil || !gh.ExposeEnv {
		return nil
	}
	envNames := gh.EnvNames
	if len(envNames) == 0 {
		envNames = []string{"GH_TOKEN", "GITHUB_TOKEN"}
	}
	envFrom := make([]clawv1alpha1.McpEnvFromSecret, 0, len(envNames))
	seen := make(map[string]bool, len(envNames))
	for _, name := range envNames {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		envFrom = append(envFrom, clawv1alpha1.McpEnvFromSecret{
			Name:      name,
			SecretRef: gh.SecretRef,
		})
	}
	return envFrom
}

func (r *ClawResourceReconciler) validateRepoAccessSecrets(instance *clawv1alpha1.Claw, secrets *userSecretCache) error {
	gh := githubRepoAccess(instance)
	if gh == nil {
		return nil
	}
	secret, err := secrets.get(gh.SecretRef.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("repoAccess.github: Secret %q not found", gh.SecretRef.Name)
		}
		return fmt.Errorf("repoAccess.github: failed to get Secret %q: %w", gh.SecretRef.Name, err)
	}
	if _, ok := secret.Data[gh.SecretRef.Key]; !ok {
		return fmt.Errorf("repoAccess.github: key %q not found in Secret %q", gh.SecretRef.Key, gh.SecretRef.Name)
	}
	return nil
}

func configureGatewayForRepoAccess(objects []*unstructured.Unstructured, instance *clawv1alpha1.Claw) error {
	desired := githubRepoAccessEnvFrom(instance)
	if len(desired) == 0 {
		return nil
	}

	gatewayName := getClawDeploymentName(instance.Name)
	for _, obj := range objects {
		if obj.GetKind() != DeploymentKind || obj.GetName() != gatewayName {
			continue
		}

		containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
		if err != nil {
			return fmt.Errorf("failed to get containers from claw deployment: %w", err)
		}
		if !found {
			return fmt.Errorf("containers field not found in claw deployment")
		}

		containerIdx := -1
		var container map[string]any
		for i, c := range containers {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if name, _, _ := unstructured.NestedString(cm, "name"); name == ClawGatewayContainerName {
				containerIdx = i
				container = cm
				break
			}
		}
		if containerIdx < 0 {
			return fmt.Errorf("container %q not found in claw deployment", ClawGatewayContainerName)
		}

		envVars, _, _ := unstructured.NestedSlice(container, "env")
		envVars = mergeEnvFromIntoSlice(envVars, desired)
		if err := unstructured.SetNestedSlice(container, envVars, "env"); err != nil {
			return fmt.Errorf("failed to set repo access env vars on claw deployment: %w", err)
		}
		containers[containerIdx] = container
		if err := unstructured.SetNestedSlice(obj.Object, containers, "spec", "template", "spec", "containers"); err != nil {
			return fmt.Errorf("failed to set containers on claw deployment: %w", err)
		}
		return nil
	}
	return fmt.Errorf("claw deployment not found in manifests")
}

func (r *ClawResourceReconciler) stampRepoAccessSecretVersionAnnotation(
	objects []*unstructured.Unstructured,
	instance *clawv1alpha1.Claw,
	secrets *userSecretCache,
) error {
	gh := githubRepoAccess(instance)
	if gh == nil || !gh.ExposeEnv {
		return nil
	}
	secret, err := secrets.get(gh.SecretRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get Secret %q for repo access env: %w", gh.SecretRef.Name, err)
	}

	gatewayName := getClawDeploymentName(instance.Name)
	for _, obj := range objects {
		if obj.GetKind() != DeploymentKind || obj.GetName() != gatewayName {
			continue
		}

		annotations, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[clawv1alpha1.AnnotationPrefixMcpSecretVersion+repoAccessAnnotationKey("github")+
			clawv1alpha1.AnnotationSuffixMcpSecretVersion] = secret.ResourceVersion
		if err := unstructured.SetNestedStringMap(obj.Object, annotations, "spec", "template", "metadata", "annotations"); err != nil {
			return fmt.Errorf("failed to set repo access secret version annotations: %w", err)
		}
		return nil
	}
	return fmt.Errorf("gateway deployment not found for repo access secret version stamping")
}

func repoAccessAnnotationKey(name string) string {
	h := sha256.Sum256([]byte("repoAccess/" + name))
	return fmt.Sprintf("%x", h[:6])
}
