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
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clawv1alpha1 "github.com/codeready-toolchain/claw-operator/api/v1alpha1"
)

func makeTestConfigMap() *unstructured.Unstructured {
	cm := &unstructured.Unstructured{}
	cm.SetKind(ConfigMapKind)
	cm.SetName(getConfigMapName(testInstanceName))
	cm.Object["data"] = map[string]any{
		"operator.json": "{}",
		"openclaw.json": "{}",
	}
	return cm
}

// --- encodeWorkspacePath tests ---

func TestEncodeWorkspacePath(t *testing.T) {
	t.Run("should return simple filename unchanged", func(t *testing.T) {
		assert.Equal(t, "IDENTITY.md", encodeWorkspacePath("IDENTITY.md"))
	})

	t.Run("should encode slashes as --", func(t *testing.T) {
		assert.Equal(t, "docs--README.md", encodeWorkspacePath("docs/README.md"))
	})

	t.Run("should encode multiple path segments", func(t *testing.T) {
		assert.Equal(t, "a--b--c.md", encodeWorkspacePath("a/b/c.md"))
	})
}

// --- validateWorkspaceFiles tests ---

func TestValidateWorkspaceFiles(t *testing.T) {
	t.Run("should accept valid simple path", func(t *testing.T) {
		err := validateWorkspaceFiles(map[string]string{"IDENTITY.md": "content"})
		assert.NoError(t, err)
	})

	t.Run("should accept valid nested path", func(t *testing.T) {
		err := validateWorkspaceFiles(map[string]string{"docs/README.md": "content"})
		assert.NoError(t, err)
	})

	t.Run("should accept AGENTS.md override", func(t *testing.T) {
		err := validateWorkspaceFiles(map[string]string{"AGENTS.md": "custom agents"})
		assert.NoError(t, err)
	})

	t.Run("should reject empty path", func(t *testing.T) {
		err := validateWorkspaceFiles(map[string]string{"": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")
	})

	t.Run("should reject absolute path", func(t *testing.T) {
		err := validateWorkspaceFiles(map[string]string{"/etc/passwd": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be absolute")
	})

	t.Run("should reject directory traversal", func(t *testing.T) {
		err := validateWorkspaceFiles(map[string]string{"../../etc/passwd": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `must not contain ".."`)
	})

	t.Run("should reject directory traversal embedded in middle of path", func(t *testing.T) {
		err := validateWorkspaceFiles(map[string]string{"foo/../bar": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `must not contain ".."`)
	})

	t.Run("should reject path with -- delimiter", func(t *testing.T) {
		err := validateWorkspaceFiles(map[string]string{"file--name.md": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reserved as path encoding delimiter")
	})

	t.Run("should reject path conflicting with platform skill", func(t *testing.T) {
		err := validateWorkspaceFiles(map[string]string{"skills/platform/SKILL.md": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with operator-managed platform skill")
	})

	t.Run("should reject path conflicting with kubernetes skill", func(t *testing.T) {
		err := validateWorkspaceFiles(map[string]string{"skills/kubernetes/SKILL.md": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with operator-managed kubernetes skill")
	})

	t.Run("should accept non-conflicting skill path", func(t *testing.T) {
		err := validateWorkspaceFiles(map[string]string{"skills/custom/SKILL.md": "content"})
		assert.NoError(t, err)
	})

	t.Run("should accept nil map", func(t *testing.T) {
		err := validateWorkspaceFiles(nil)
		assert.NoError(t, err)
	})
}

// --- validateSkillNames tests ---

func TestValidateSkillNames(t *testing.T) {
	t.Run("should accept valid name", func(t *testing.T) {
		err := validateSkillNames(map[string]string{"quote-builder": "content"})
		assert.NoError(t, err)
	})

	t.Run("should reject empty name", func(t *testing.T) {
		err := validateSkillNames(map[string]string{"": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")
	})

	t.Run("should reject dot name", func(t *testing.T) {
		err := validateSkillNames(map[string]string{".": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `must not be "."`)
	})

	t.Run("should reject dot-dot name", func(t *testing.T) {
		err := validateSkillNames(map[string]string{"..": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `must not be ".."`)
	})

	t.Run("should reject name with slash", func(t *testing.T) {
		err := validateSkillNames(map[string]string{"my/skill": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `must not contain "/"`)
	})

	t.Run("should reject name with -- delimiter", func(t *testing.T) {
		err := validateSkillNames(map[string]string{"my--skill": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reserved as path encoding delimiter")
	})

	t.Run("should reject builtin platform name", func(t *testing.T) {
		err := validateSkillNames(map[string]string{"platform": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with builtin operator skill")
	})

	t.Run("should reject builtin kubernetes name", func(t *testing.T) {
		err := validateSkillNames(map[string]string{"kubernetes": "content"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with builtin operator skill")
	})

	t.Run("should accept nil map", func(t *testing.T) {
		err := validateSkillNames(nil)
		assert.NoError(t, err)
	})
}

// --- validateSkillImages tests ---

func TestValidateSkillImages(t *testing.T) {
	t.Run("should accept valid skill images", func(t *testing.T) {
		images := []clawv1alpha1.SkillImageSpec{
			{Name: "openshift-review", Image: "quay.io/corp/openshift-review:1.0.0"},
			{Name: "sales-playbook", Image: "quay.io/corp/sales-playbook:2.0.0"},
		}
		err := validateSkillImages(images, nil)
		assert.NoError(t, err)
	})

	t.Run("should accept nil slice", func(t *testing.T) {
		err := validateSkillImages(nil, nil)
		assert.NoError(t, err)
	})

	t.Run("should accept empty slice", func(t *testing.T) {
		err := validateSkillImages([]clawv1alpha1.SkillImageSpec{}, nil)
		assert.NoError(t, err)
	})

	t.Run("should reject empty name", func(t *testing.T) {
		images := []clawv1alpha1.SkillImageSpec{
			{Name: "", Image: "quay.io/corp/skill:1.0.0"},
		}
		err := validateSkillImages(images, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")
	})

	t.Run("should reject dot name", func(t *testing.T) {
		images := []clawv1alpha1.SkillImageSpec{
			{Name: ".", Image: "quay.io/corp/skill:1.0.0"},
		}
		err := validateSkillImages(images, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `must not be "."`)
	})

	t.Run("should reject dot-dot name", func(t *testing.T) {
		images := []clawv1alpha1.SkillImageSpec{
			{Name: "..", Image: "quay.io/corp/skill:1.0.0"},
		}
		err := validateSkillImages(images, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `must not be ".."`)
	})

	t.Run("should reject name with slash", func(t *testing.T) {
		images := []clawv1alpha1.SkillImageSpec{
			{Name: "my/skill", Image: "quay.io/corp/skill:1.0.0"},
		}
		err := validateSkillImages(images, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `must not contain "/"`)
	})

	t.Run("should reject name with -- delimiter", func(t *testing.T) {
		images := []clawv1alpha1.SkillImageSpec{
			{Name: "my--skill", Image: "quay.io/corp/skill:1.0.0"},
		}
		err := validateSkillImages(images, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reserved as path encoding delimiter")
	})

	t.Run("should reject builtin platform name", func(t *testing.T) {
		images := []clawv1alpha1.SkillImageSpec{
			{Name: "platform", Image: "quay.io/corp/skill:1.0.0"},
		}
		err := validateSkillImages(images, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with builtin operator skill")
	})

	t.Run("should reject builtin kubernetes name", func(t *testing.T) {
		images := []clawv1alpha1.SkillImageSpec{
			{Name: "kubernetes", Image: "quay.io/corp/skill:1.0.0"},
		}
		err := validateSkillImages(images, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with builtin operator skill")
	})

	t.Run("should reject duplicate names within images", func(t *testing.T) {
		images := []clawv1alpha1.SkillImageSpec{
			{Name: "my-skill", Image: "quay.io/corp/skill:1.0.0"},
			{Name: "my-skill", Image: "quay.io/corp/skill:2.0.0"},
		}
		err := validateSkillImages(images, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate skill image name")
	})

	t.Run("should reject name collision with content skills", func(t *testing.T) {
		images := []clawv1alpha1.SkillImageSpec{
			{Name: "my-skill", Image: "quay.io/corp/skill:1.0.0"},
		}
		allNames := map[string]string{"my-skill": "spec.skills.content"}
		err := validateSkillImages(images, allNames)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with spec.skills.content entry")
	})

	t.Run("should reject empty image reference", func(t *testing.T) {
		images := []clawv1alpha1.SkillImageSpec{
			{Name: "my-skill", Image: ""},
		}
		err := validateSkillImages(images, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "has empty image reference")
	})

	t.Run("should reject empty imagePullSecret name", func(t *testing.T) {
		images := []clawv1alpha1.SkillImageSpec{
			{
				Name:  "my-skill",
				Image: "quay.io/corp/skill:1.0.0",
				ImagePullSecrets: []corev1.LocalObjectReference{
					{Name: ""},
				},
			},
		}
		err := validateSkillImages(images, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "imagePullSecret with empty name")
	})
}

// --- validateSkills tests ---

func TestValidateSkills(t *testing.T) {
	t.Run("should accept nil skills", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{}}
		assert.NoError(t, validateSkills(instance))
	})

	t.Run("should accept all three sources without collision", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{
			Spec: clawv1alpha1.ClawSpec{
				Skills: &clawv1alpha1.SkillsSpec{
					Content: map[string]string{"inline-skill": "content"},
					Images: []clawv1alpha1.SkillImageSpec{
						{Name: "image-skill", Image: "quay.io/test:1.0"},
					},
					ConfigMaps: []clawv1alpha1.SkillConfigMapRef{
						{Name: "my-configmap"},
					},
				},
			},
		}
		assert.NoError(t, validateSkills(instance))
	})

	t.Run("should reject content-image collision", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{
			Spec: clawv1alpha1.ClawSpec{
				Skills: &clawv1alpha1.SkillsSpec{
					Content: map[string]string{"collision": "content"},
					Images: []clawv1alpha1.SkillImageSpec{
						{Name: "collision", Image: "quay.io/test:1.0"},
					},
				},
			},
		}
		err := validateSkills(instance)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with spec.skills.content entry")
	})

	t.Run("should reject duplicate configMap refs", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{
			Spec: clawv1alpha1.ClawSpec{
				Skills: &clawv1alpha1.SkillsSpec{
					ConfigMaps: []clawv1alpha1.SkillConfigMapRef{
						{Name: "cm1"},
						{Name: "cm1"},
					},
				},
			},
		}
		err := validateSkills(instance)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate skill configMap ref")
	})

	t.Run("should reject empty configMap ref name", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{
			Spec: clawv1alpha1.ClawSpec{
				Skills: &clawv1alpha1.SkillsSpec{
					ConfigMaps: []clawv1alpha1.SkillConfigMapRef{
						{Name: ""},
					},
				},
			},
		}
		err := validateSkills(instance)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")
	})
}

// --- validateSkillConfigMaps tests ---

func TestValidateSkillConfigMaps(t *testing.T) {
	t.Run("should accept nil refs", func(t *testing.T) {
		assert.NoError(t, validateSkillConfigMaps(nil))
	})

	t.Run("should accept valid refs", func(t *testing.T) {
		refs := []clawv1alpha1.SkillConfigMapRef{{Name: "cm1"}, {Name: "cm2"}}
		assert.NoError(t, validateSkillConfigMaps(refs))
	})

	t.Run("should reject empty name", func(t *testing.T) {
		refs := []clawv1alpha1.SkillConfigMapRef{{Name: ""}}
		err := validateSkillConfigMaps(refs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")
	})

	t.Run("should reject duplicate refs", func(t *testing.T) {
		refs := []clawv1alpha1.SkillConfigMapRef{{Name: "cm1"}, {Name: "cm1"}}
		err := validateSkillConfigMaps(refs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate skill configMap ref")
	})
}

// --- injectWorkspaceFiles tests ---

func TestInjectWorkspaceFiles(t *testing.T) {
	t.Run("should be no-op when workspace is nil", func(t *testing.T) {
		cm := makeTestConfigMap()
		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName},
			Spec:       clawv1alpha1.ClawSpec{},
		}
		err := injectWorkspaceFiles([]*unstructured.Unstructured{cm}, instance)
		require.NoError(t, err)

		data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
		assert.NotContains(t, data, "_ws_IDENTITY.md")
	})

	t.Run("should be no-op when workspace has skipBootstrap but no files", func(t *testing.T) {
		cm := makeTestConfigMap()
		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName},
			Spec: clawv1alpha1.ClawSpec{
				Workspace: &clawv1alpha1.WorkspaceSpec{
					SkipBootstrap: true,
				},
			},
		}
		err := injectWorkspaceFiles([]*unstructured.Unstructured{cm}, instance)
		require.NoError(t, err)

		data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
		for k := range data {
			assert.False(t, len(k) > 4 && k[:4] == "_ws_", "no _ws_ keys should be added when files map is empty")
		}
	})

	t.Run("should inject _ws_ prefixed keys for workspace files", func(t *testing.T) {
		cm := makeTestConfigMap()
		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName},
			Spec: clawv1alpha1.ClawSpec{
				Workspace: &clawv1alpha1.WorkspaceSpec{
					Files: map[string]string{
						"IDENTITY.md": "# Identity\nName: Test",
						"AGENTS.md":   "# Custom Agents",
					},
				},
			},
		}
		err := injectWorkspaceFiles([]*unstructured.Unstructured{cm}, instance)
		require.NoError(t, err)

		val, found, _ := unstructured.NestedString(cm.Object, "data", "_ws_IDENTITY.md")
		assert.True(t, found)
		assert.Equal(t, "# Identity\nName: Test", val)

		val, found, _ = unstructured.NestedString(cm.Object, "data", "_ws_AGENTS.md")
		assert.True(t, found)
		assert.Equal(t, "# Custom Agents", val)
	})

	t.Run("should encode slashes in path as --", func(t *testing.T) {
		cm := makeTestConfigMap()
		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName},
			Spec: clawv1alpha1.ClawSpec{
				Workspace: &clawv1alpha1.WorkspaceSpec{
					Files: map[string]string{
						"docs/README.md": "readme content",
					},
				},
			},
		}
		err := injectWorkspaceFiles([]*unstructured.Unstructured{cm}, instance)
		require.NoError(t, err)

		val, found, _ := unstructured.NestedString(cm.Object, "data", "_ws_docs--README.md")
		assert.True(t, found)
		assert.Equal(t, "readme content", val)
	})

	t.Run("should return error for invalid path", func(t *testing.T) {
		cm := makeTestConfigMap()
		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName},
			Spec: clawv1alpha1.ClawSpec{
				Workspace: &clawv1alpha1.WorkspaceSpec{
					Files: map[string]string{
						"../../etc/passwd": "bad content",
					},
				},
			},
		}
		err := injectWorkspaceFiles([]*unstructured.Unstructured{cm}, instance)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `must not contain ".."`)
	})
}

// --- injectSkillFiles tests ---

func TestInjectSkillFiles(t *testing.T) {
	t.Run("should be no-op when skills is nil", func(t *testing.T) {
		cm := makeTestConfigMap()
		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName},
			Spec:       clawv1alpha1.ClawSpec{},
		}
		err := injectSkillFiles([]*unstructured.Unstructured{cm}, instance)
		require.NoError(t, err)
	})

	t.Run("should inject _skill_ prefixed keys", func(t *testing.T) {
		cm := makeTestConfigMap()
		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName},
			Spec: clawv1alpha1.ClawSpec{
				Skills: &clawv1alpha1.SkillsSpec{
					Content: map[string]string{
						"quote-builder": "# Quote Builder\nUse pricing API...",
						"compliance":    "# Compliance\nFollow policy...",
					},
				},
			},
		}
		err := injectSkillFiles([]*unstructured.Unstructured{cm}, instance)
		require.NoError(t, err)

		val, found, _ := unstructured.NestedString(cm.Object, "data", "_skill_quote-builder")
		assert.True(t, found)
		assert.Equal(t, "# Quote Builder\nUse pricing API...", val)

		val, found, _ = unstructured.NestedString(cm.Object, "data", "_skill_compliance")
		assert.True(t, found)
		assert.Equal(t, "# Compliance\nFollow policy...", val)
	})

	t.Run("should return error for builtin skill name", func(t *testing.T) {
		cm := makeTestConfigMap()
		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName},
			Spec: clawv1alpha1.ClawSpec{
				Skills: &clawv1alpha1.SkillsSpec{
					Content: map[string]string{
						"platform": "should fail",
					},
				},
			},
		}
		err := injectSkillFiles([]*unstructured.Unstructured{cm}, instance)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with builtin operator skill")
	})
}

// --- injectSkipBootstrap tests ---

func TestInjectSkipBootstrap(t *testing.T) {
	t.Run("should set skipBootstrap when enabled", func(t *testing.T) {
		config := map[string]any{}
		instance := &clawv1alpha1.Claw{
			Spec: clawv1alpha1.ClawSpec{
				Workspace: &clawv1alpha1.WorkspaceSpec{
					SkipBootstrap: true,
				},
			},
		}
		injectSkipBootstrap(config, instance)

		agents, ok := config["agents"].(map[string]any)
		require.True(t, ok)
		defaults, ok := agents["defaults"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, true, defaults["skipBootstrap"])
	})

	t.Run("should not set skipBootstrap when disabled", func(t *testing.T) {
		config := map[string]any{}
		instance := &clawv1alpha1.Claw{
			Spec: clawv1alpha1.ClawSpec{
				Workspace: &clawv1alpha1.WorkspaceSpec{
					SkipBootstrap: false,
				},
			},
		}
		injectSkipBootstrap(config, instance)

		_, ok := config["agents"]
		assert.False(t, ok, "agents key should not be created when skipBootstrap is false")
	})

	t.Run("should not set skipBootstrap when workspace is nil", func(t *testing.T) {
		config := map[string]any{}
		instance := &clawv1alpha1.Claw{
			Spec: clawv1alpha1.ClawSpec{},
		}
		injectSkipBootstrap(config, instance)

		_, ok := config["agents"]
		assert.False(t, ok, "agents key should not be created when workspace is nil")
	})

	t.Run("should preserve existing config when setting skipBootstrap", func(t *testing.T) {
		config := map[string]any{
			"agents": map[string]any{
				"defaults": map[string]any{
					"workspace": "~/.openclaw/workspace",
				},
			},
		}
		instance := &clawv1alpha1.Claw{
			Spec: clawv1alpha1.ClawSpec{
				Workspace: &clawv1alpha1.WorkspaceSpec{
					SkipBootstrap: true,
				},
			},
		}
		injectSkipBootstrap(config, instance)

		agents := config["agents"].(map[string]any)
		defaults := agents["defaults"].(map[string]any)
		assert.Equal(t, true, defaults["skipBootstrap"])
		assert.Equal(t, "~/.openclaw/workspace", defaults["workspace"])
	})
}

// --- Integration tests ---

func TestWorkspaceIntegration(t *testing.T) {
	t.Run("should inject workspace and skill keys into ConfigMap after reconcile", func(t *testing.T) {
		ctx := context.Background()

		secret := createTestAPIKeySecret(aiModelSecret, namespace, aiModelSecretKey, aiModelSecretValue)
		require.NoError(t, k8sClient.Create(ctx, secret))

		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: namespace},
			Spec: clawv1alpha1.ClawSpec{
				Credentials: testCredentials(),
				Workspace: &clawv1alpha1.WorkspaceSpec{
					SkipBootstrap: true,
					Files: map[string]string{
						"IDENTITY.md": "# Identity\nName: Test User",
					},
				},
				Skills: &clawv1alpha1.SkillsSpec{
					Content: map[string]string{
						"quote-builder": "# Quote Builder\nBuild quotes...",
					},
				},
			},
		}
		require.NoError(t, k8sClient.Create(ctx, instance))

		t.Cleanup(func() {
			deleteAndWaitAllResources(t, namespace)
		})

		reconciler := createClawReconciler()
		reconcileClaw(t, ctx, reconciler, testInstanceName, namespace)

		var cm corev1.ConfigMap
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{
			Name:      getConfigMapName(testInstanceName),
			Namespace: namespace,
		}, &cm))

		assert.Equal(t, "# Identity\nName: Test User", cm.Data["_ws_IDENTITY.md"],
			"workspace file should be present in ConfigMap")
		assert.Equal(t, "# Quote Builder\nBuild quotes...", cm.Data["_skill_quote-builder"],
			"skill file should be present in ConfigMap")

		// Verify skipBootstrap is in operator.json
		assert.Contains(t, cm.Data["operator.json"], "skipBootstrap")
	})

	t.Run("should inject skipBootstrap without workspace files or skills", func(t *testing.T) {
		ctx := context.Background()

		secret := createTestAPIKeySecret(aiModelSecret, namespace, aiModelSecretKey, aiModelSecretValue)
		require.NoError(t, k8sClient.Create(ctx, secret))

		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: namespace},
			Spec: clawv1alpha1.ClawSpec{
				Credentials: testCredentials(),
				Workspace: &clawv1alpha1.WorkspaceSpec{
					SkipBootstrap: true,
				},
			},
		}
		require.NoError(t, k8sClient.Create(ctx, instance))

		t.Cleanup(func() {
			deleteAndWaitAllResources(t, namespace)
		})

		reconciler := createClawReconciler()
		reconcileClaw(t, ctx, reconciler, testInstanceName, namespace)

		var cm corev1.ConfigMap
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{
			Name:      getConfigMapName(testInstanceName),
			Namespace: namespace,
		}, &cm))

		var config map[string]any
		require.NoError(t, json.Unmarshal([]byte(cm.Data["operator.json"]), &config))

		agents, ok := config["agents"].(map[string]any)
		require.True(t, ok, "operator.json should have agents key")
		defaults, ok := agents["defaults"].(map[string]any)
		require.True(t, ok, "agents should have defaults key")
		assert.Equal(t, true, defaults["skipBootstrap"],
			"skipBootstrap should be true in operator.json")

		for k := range cm.Data {
			assert.NotContains(t, k, "_ws_", "no workspace keys should be present")
			assert.NotContains(t, k, "_skill_", "no skill keys should be present")
		}
	})

	t.Run("should fail reconcile with invalid workspace path", func(t *testing.T) {
		ctx := context.Background()

		secret := createTestAPIKeySecret(aiModelSecret, namespace, aiModelSecretKey, aiModelSecretValue)
		require.NoError(t, k8sClient.Create(ctx, secret))

		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: namespace},
			Spec: clawv1alpha1.ClawSpec{
				Credentials: testCredentials(),
				Workspace: &clawv1alpha1.WorkspaceSpec{
					Files: map[string]string{
						"../../etc/passwd": "bad content",
					},
				},
			},
		}
		require.NoError(t, k8sClient.Create(ctx, instance))

		t.Cleanup(func() {
			deleteAndWaitAllResources(t, namespace)
		})

		reconciler := createClawReconciler()
		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      testInstanceName,
				Namespace: namespace,
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `must not contain ".."`)
	})

	t.Run("should fail reconcile with invalid skill name", func(t *testing.T) {
		ctx := context.Background()

		secret := createTestAPIKeySecret(aiModelSecret, namespace, aiModelSecretKey, aiModelSecretValue)
		require.NoError(t, k8sClient.Create(ctx, secret))

		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: namespace},
			Spec: clawv1alpha1.ClawSpec{
				Credentials: testCredentials(),
				Skills: &clawv1alpha1.SkillsSpec{
					Content: map[string]string{
						"platform": "should not be allowed",
					},
				},
			},
		}
		require.NoError(t, k8sClient.Create(ctx, instance))

		t.Cleanup(func() {
			deleteAndWaitAllResources(t, namespace)
		})

		reconciler := createClawReconciler()
		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      testInstanceName,
				Namespace: namespace,
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with builtin operator skill")
	})
}

// --- SkillImages integration tests ---

func TestSkillImagesIntegration(t *testing.T) {
	t.Run("should reconcile successfully with skillImages set", func(t *testing.T) {
		ctx := context.Background()

		secret := createTestAPIKeySecret(aiModelSecret, namespace, aiModelSecretKey, aiModelSecretValue)
		require.NoError(t, k8sClient.Create(ctx, secret))

		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: namespace},
			Spec: clawv1alpha1.ClawSpec{
				Credentials: testCredentials(),
				Skills: &clawv1alpha1.SkillsSpec{
					Images: []clawv1alpha1.SkillImageSpec{
						{Name: "openshift-review", Image: "quay.io/corp/openshift-review:1.0.0"},
					},
				},
			},
		}
		require.NoError(t, k8sClient.Create(ctx, instance))

		t.Cleanup(func() {
			deleteAndWaitAllResources(t, namespace)
		})

		reconciler := createClawReconciler()
		reconcileClaw(t, ctx, reconciler, testInstanceName, namespace)

		// Verify the Deployment was created (reconciliation did not error).
		// ImageVolume content assertions are in unit tests (TestConfigureSkillImages)
		// because envtest strips image volume sources that require feature gates.
		var dep appsv1.Deployment
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{
			Name:      getClawDeploymentName(testInstanceName),
			Namespace: namespace,
		}, &dep))
	})

	t.Run("should fail reconcile when skill image name collides with inline skill", func(t *testing.T) {
		ctx := context.Background()

		secret := createTestAPIKeySecret(aiModelSecret, namespace, aiModelSecretKey, aiModelSecretValue)
		require.NoError(t, k8sClient.Create(ctx, secret))

		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: namespace},
			Spec: clawv1alpha1.ClawSpec{
				Credentials: testCredentials(),
				Skills: &clawv1alpha1.SkillsSpec{
					Content: map[string]string{"my-skill": "content"},
					Images: []clawv1alpha1.SkillImageSpec{
						{Name: "my-skill", Image: "quay.io/corp/my-skill:1.0.0"},
					},
				},
			},
		}
		require.NoError(t, k8sClient.Create(ctx, instance))

		t.Cleanup(func() {
			deleteAndWaitAllResources(t, namespace)
		})

		reconciler := createClawReconciler()
		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      testInstanceName,
				Namespace: namespace,
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with spec.skills.content entry")
	})
}

// --- SkillConfigMaps integration tests ---

func TestSkillConfigMapsIntegration(t *testing.T) {
	t.Run("should inject skills from referenced ConfigMap after reconcile", func(t *testing.T) {
		ctx := context.Background()

		secret := createTestAPIKeySecret(aiModelSecret, namespace, aiModelSecretKey, aiModelSecretValue)
		require.NoError(t, k8sClient.Create(ctx, secret))

		skillCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "corp-skills", Namespace: namespace},
			Data: map[string]string{
				"sales-playbook": "# Sales Playbook\nFollow the process...",
				"onboarding":     "# Onboarding\nWelcome new hires...",
			},
		}
		require.NoError(t, k8sClient.Create(ctx, skillCM))

		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: namespace},
			Spec: clawv1alpha1.ClawSpec{
				Credentials: testCredentials(),
				Skills: &clawv1alpha1.SkillsSpec{
					ConfigMaps: []clawv1alpha1.SkillConfigMapRef{
						{Name: "corp-skills"},
					},
				},
			},
		}
		require.NoError(t, k8sClient.Create(ctx, instance))

		t.Cleanup(func() {
			deleteAndWaitAllResources(t, namespace)
		})

		reconciler := createClawReconciler()
		reconcileClaw(t, ctx, reconciler, testInstanceName, namespace)

		var cm corev1.ConfigMap
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{
			Name:      getConfigMapName(testInstanceName),
			Namespace: namespace,
		}, &cm))

		assert.Equal(t, "# Sales Playbook\nFollow the process...", cm.Data["_skill_sales-playbook"],
			"sales-playbook skill should be present in gateway ConfigMap")
		assert.Equal(t, "# Onboarding\nWelcome new hires...", cm.Data["_skill_onboarding"],
			"onboarding skill should be present in gateway ConfigMap")
	})

	t.Run("should fail reconcile when referenced ConfigMap does not exist", func(t *testing.T) {
		ctx := context.Background()

		secret := createTestAPIKeySecret(aiModelSecret, namespace, aiModelSecretKey, aiModelSecretValue)
		require.NoError(t, k8sClient.Create(ctx, secret))

		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: namespace},
			Spec: clawv1alpha1.ClawSpec{
				Credentials: testCredentials(),
				Skills: &clawv1alpha1.SkillsSpec{
					ConfigMaps: []clawv1alpha1.SkillConfigMapRef{
						{Name: "nonexistent-cm"},
					},
				},
			},
		}
		require.NoError(t, k8sClient.Create(ctx, instance))

		t.Cleanup(func() {
			deleteAndWaitAllResources(t, namespace)
		})

		reconciler := createClawReconciler()
		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      testInstanceName,
				Namespace: namespace,
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get skill ConfigMap")
	})

	t.Run("should fail when ConfigMap key collides with inline content skill", func(t *testing.T) {
		ctx := context.Background()

		secret := createTestAPIKeySecret(aiModelSecret, namespace, aiModelSecretKey, aiModelSecretValue)
		require.NoError(t, k8sClient.Create(ctx, secret))

		skillCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "collision-skills", Namespace: namespace},
			Data: map[string]string{
				"my-skill": "# From ConfigMap",
			},
		}
		require.NoError(t, k8sClient.Create(ctx, skillCM))

		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: namespace},
			Spec: clawv1alpha1.ClawSpec{
				Credentials: testCredentials(),
				Skills: &clawv1alpha1.SkillsSpec{
					Content: map[string]string{
						"my-skill": "# From Content",
					},
					ConfigMaps: []clawv1alpha1.SkillConfigMapRef{
						{Name: "collision-skills"},
					},
				},
			},
		}
		require.NoError(t, k8sClient.Create(ctx, instance))

		t.Cleanup(func() {
			deleteAndWaitAllResources(t, namespace)
		})

		reconciler := createClawReconciler()
		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      testInstanceName,
				Namespace: namespace,
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with spec.skills.content entry")
	})
}
