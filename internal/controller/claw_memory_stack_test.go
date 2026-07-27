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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clawv1alpha1 "github.com/codeready-toolchain/claw-operator/api/v1alpha1"
)

func TestMemoryStackEnabled(t *testing.T) {
	t.Run("nil memory spec defaults to disabled", func(t *testing.T) {
		assert.False(t, memoryStackEnabled(&clawv1alpha1.Claw{}))
	})
	t.Run("nil enabled pointer defaults to disabled", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{Memory: &clawv1alpha1.MemorySpec{}}}
		assert.False(t, memoryStackEnabled(instance))
	})
	t.Run("explicit true is enabled", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)}}}
		assert.True(t, memoryStackEnabled(instance))
	})
	t.Run("explicit false opts out", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(false)}}}
		assert.False(t, memoryStackEnabled(instance))
	})
}

func memSearch(config map[string]any) map[string]any {
	return config["agents"].(map[string]any)["defaults"].(map[string]any)["memorySearch"].(map[string]any)
}
func memEntries(config map[string]any) map[string]any {
	return config["plugins"].(map[string]any)["entries"].(map[string]any)
}

func TestInjectMemoryStack(t *testing.T) {
	t.Run("openai credential enables vectors and seeds native layers", func(t *testing.T) {
		config := map[string]any{}
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{
			Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)},
			Credentials: []clawv1alpha1.CredentialSpec{
				{Name: "openai", Type: clawv1alpha1.CredentialTypeAPIKey, Provider: "openai"},
			},
		}}
		injectMemorySearch(config, instance)
		injectMemoryStack(config, instance, false)

		ms := memSearch(config)
		assert.Equal(t, true, ms["enabled"])
		assert.Equal(t, "openai", ms["provider"])

		entries := memEntries(config)
		dreaming := entries["memory-core"].(map[string]any)["config"].(map[string]any)["dreaming"].(map[string]any)
		assert.Equal(t, true, dreaming["enabled"])

		wiki := entries["memory-wiki"].(map[string]any)
		assert.Equal(t, true, wiki["enabled"])
		wcfg := wiki["config"].(map[string]any)
		assert.Equal(t, "bridge", wcfg["vaultMode"])
		assert.Equal(t, "~/.openclaw/workspace/wiki/main", wcfg["vault"].(map[string]any)["path"])

		_, hasSlots := config["plugins"].(map[string]any)["slots"]
		assert.False(t, hasSlots, "the native stack must not select a context engine")
	})

	t.Run("no embedding credential leaves vectors off but seeds the rest", func(t *testing.T) {
		config := map[string]any{}
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{
			Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)},
			Credentials: []clawv1alpha1.CredentialSpec{
				{Name: "claude", Type: clawv1alpha1.CredentialTypeAPIKey, Provider: "anthropic"},
			},
		}}
		injectMemorySearch(config, instance)
		injectMemoryStack(config, instance, false)

		assert.Equal(t, false, memSearch(config)["enabled"])
		entries := memEntries(config)
		assert.Equal(t, true, entries["memory-wiki"].(map[string]any)["enabled"])
	})

	t.Run("skips entirely when disabled", func(t *testing.T) {
		config := map[string]any{}
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(false)}}}
		injectMemoryStack(config, instance, false)
		_, hasPlugins := config["plugins"]
		assert.False(t, hasPlugins)
	})

	t.Run("seeds native layers even when plugin installation disabled", func(t *testing.T) {
		config := map[string]any{}
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{
			Memory:       &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)},
			Restrictions: &clawv1alpha1.RestrictionsSpec{PluginInstallation: ptr.To(false)},
		}}
		injectMemoryStack(config, instance, false)
		entries := memEntries(config)
		assert.Equal(t, true, entries["memory-wiki"].(map[string]any)["enabled"], "memory-wiki seeds with plugin install disabled")
		assert.Equal(t, true, entries["memory-core"].(map[string]any)["config"].(map[string]any)["dreaming"].(map[string]any)["enabled"], "dreaming seeds with plugin install disabled")
	})

	t.Run("preserves a user-set native layer entry but still seeds the rest", func(t *testing.T) {
		config := map[string]any{
			"plugins": map[string]any{"entries": map[string]any{"memory-wiki": map[string]any{"enabled": false}}},
		}
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{
			Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)},
		}}
		injectMemoryStack(config, instance, false)
		entries := config["plugins"].(map[string]any)["entries"].(map[string]any)
		assert.Equal(t, false, entries["memory-wiki"].(map[string]any)["enabled"], "user value preserved")
		_, hasCore := entries["memory-core"]
		assert.True(t, hasCore, "other layers still seed; one tuned key does not disable the stack")
	})

	t.Run("seeds the stack while honoring a user-tuned dreaming threshold", func(t *testing.T) {
		config := map[string]any{
			"plugins": map[string]any{"entries": map[string]any{
				"memory-core": map[string]any{"config": map[string]any{"dreaming": map[string]any{
					"phases": map[string]any{"deep": map[string]any{"minScore": 0.45}},
				}}},
			}},
		}
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{
			Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)},
		}}
		injectMemoryStack(config, instance, false)
		entries := config["plugins"].(map[string]any)["entries"].(map[string]any)
		dreaming := entries["memory-core"].(map[string]any)["config"].(map[string]any)["dreaming"].(map[string]any)
		assert.Equal(t, true, dreaming["enabled"], "operator still enables dreaming")
		deep := dreaming["phases"].(map[string]any)["deep"].(map[string]any)
		assert.Equal(t, 0.45, deep["minScore"], "user threshold survives injection")
		wiki := entries["memory-wiki"].(map[string]any)
		assert.Equal(t, true, wiki["enabled"], "memory-wiki still seeds")
		assert.Equal(t, "bridge", wiki["config"].(map[string]any)["vaultMode"])
	})

	t.Run("skips when user already configured a context engine", func(t *testing.T) {
		config := map[string]any{
			"plugins": map[string]any{"slots": map[string]any{"contextEngine": "custom-engine"}},
		}
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{
			Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)},
			Credentials: []clawv1alpha1.CredentialSpec{
				{Name: "openai", Type: clawv1alpha1.CredentialTypeAPIKey, Provider: "openai"},
			},
		}}
		injectMemoryStack(config, instance, false)
		slots := config["plugins"].(map[string]any)["slots"].(map[string]any)
		assert.Equal(t, "custom-engine", slots["contextEngine"], "user value preserved")
		_, hasEntries := config["plugins"].(map[string]any)["entries"]
		assert.False(t, hasEntries, "no operator entries injected over a user override")
	})

	t.Run("user memorySearch override is not re-enabled by stack injection", func(t *testing.T) {
		config := map[string]any{
			"agents": map[string]any{
				"defaults": map[string]any{
					"memorySearch": map[string]any{"enabled": false},
				},
			},
		}
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{
			Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)},
			Credentials: []clawv1alpha1.CredentialSpec{
				{Name: "openai", Type: clawv1alpha1.CredentialTypeAPIKey, Provider: "openai"},
			},
			Config: &clawv1alpha1.ConfigSpec{
				Raw: &clawv1alpha1.RawConfig{
					RawExtension: runtime.RawExtension{Raw: []byte(`{"agents":{"defaults":{"memorySearch":{"enabled":false}}}}`)},
				},
			},
		}}
		// injectMemorySearch would skip (user set memorySearch); injectMemoryStack must not re-enable.
		injectMemoryStack(config, instance, true)
		assert.Equal(t, false, memSearch(config)["enabled"], "user's enabled:false must survive stack injection")
		// the native layers still seed:
		assert.Equal(t, true, memEntries(config)["memory-wiki"].(map[string]any)["enabled"])
	})
}

func TestSetMemoryStackCondition(t *testing.T) {
	cond := func(instance *clawv1alpha1.Claw) *metav1.Condition {
		return meta.FindStatusCondition(instance.Status.Conditions, clawv1alpha1.ConditionTypeMemoryStack)
	}
	openaiCreds := []clawv1alpha1.CredentialSpec{{Name: "openai", Type: clawv1alpha1.CredentialTypeAPIKey, Provider: "openai"}}

	t.Run("disabled reports no condition", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(false)}}}
		setMemoryStackCondition(instance)
		assert.Nil(t, cond(instance), "opted-out instances must not carry a MemoryStack condition")
	})
	t.Run("disabling removes a previously set condition", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)}, Credentials: openaiCreds}}
		setMemoryStackCondition(instance)
		require.NotNil(t, cond(instance))

		instance.Spec.Memory.Enabled = ptr.To(false)
		setMemoryStackCondition(instance)
		assert.Nil(t, cond(instance))
	})
	t.Run("enabled with vectors", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)}, Credentials: openaiCreds}}
		setMemoryStackCondition(instance)
		c := cond(instance)
		require.NotNil(t, c)
		assert.Equal(t, metav1.ConditionTrue, c.Status)
		assert.Equal(t, clawv1alpha1.ConditionReasonMemoryStackEnabled, c.Reason)
		assert.Contains(t, c.Message, "vector recall")
	})
	t.Run("enabled without embedding credential", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)}, Credentials: []clawv1alpha1.CredentialSpec{{Name: "c", Type: clawv1alpha1.CredentialTypeAPIKey, Provider: "anthropic"}}}}
		setMemoryStackCondition(instance)
		c := cond(instance)
		require.NotNil(t, c)
		assert.Equal(t, metav1.ConditionTrue, c.Status)
		assert.Equal(t, clawv1alpha1.ConditionReasonMemoryStackNoVectors, c.Reason)
	})
	t.Run("user-owned context engine does not claim an applied stack", func(t *testing.T) {
		// injectMemoryStack backs off entirely for a user-set contextEngine, so
		// the condition must not report a stack the operator never seeded.
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{
			Credentials: openaiCreds,
			Memory:      &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)},
			Config: &clawv1alpha1.ConfigSpec{
				Raw: &clawv1alpha1.RawConfig{
					RawExtension: runtime.RawExtension{Raw: []byte(`{"plugins":{"slots":{"contextEngine":"custom"}}}`)},
				},
			},
		}}
		setMemoryStackCondition(instance)
		c := cond(instance)
		require.NotNil(t, c)
		assert.Equal(t, metav1.ConditionFalse, c.Status)
		assert.Equal(t, clawv1alpha1.ConditionReasonMemoryStackUserManaged, c.Reason)
		assert.Contains(t, c.Message, "plugins.slots.contextEngine")
	})
	t.Run("user-tuned memory entry still reports an applied stack", func(t *testing.T) {
		// A tuning knob on memory-core does not suppress seeding (the operator
		// still owns the layers), so the condition must not claim UserManaged.
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{
			Credentials: openaiCreds,
			Memory:      &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)},
			Config: &clawv1alpha1.ConfigSpec{
				Raw: &clawv1alpha1.RawConfig{
					RawExtension: runtime.RawExtension{
						Raw: []byte(`{"plugins":{"entries":{"memory-core":{"config":{"dreaming":{"phases":{"deep":{"minScore":0.45}}}}}}}}`),
					},
				},
			},
		}}
		setMemoryStackCondition(instance)
		c := cond(instance)
		require.NotNil(t, c)
		assert.Equal(t, metav1.ConditionTrue, c.Status)
		assert.Equal(t, clawv1alpha1.ConditionReasonMemoryStackEnabled, c.Reason)
	})
	t.Run("user-owned memorySearch does not claim vector recall", func(t *testing.T) {
		// An embedding-capable credential is present, but the user disabled
		// memorySearch in spec.config.raw, so the operator backs off. The
		// condition must not assert "with vector recall".
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{
			Credentials: openaiCreds,
			Memory:      &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)},
			Config: &clawv1alpha1.ConfigSpec{
				Raw: &clawv1alpha1.RawConfig{
					RawExtension: runtime.RawExtension{Raw: []byte(`{"agents":{"defaults":{"memorySearch":{"enabled":false}}}}`)},
				},
			},
		}}
		setMemoryStackCondition(instance)
		c := cond(instance)
		require.NotNil(t, c)
		assert.Equal(t, metav1.ConditionTrue, c.Status)
		assert.Equal(t, clawv1alpha1.ConditionReasonMemoryStackEnabled, c.Reason)
		assert.NotContains(t, c.Message, "with vector recall")
		assert.Contains(t, c.Message, "spec.config.raw")
	})
}

// --- Integration test ---

func TestMemoryStackIntegration(t *testing.T) {
	t.Run("fresh memory-on instance wires config and condition", func(t *testing.T) {
		t.Cleanup(func() { deleteAndWaitAllResources(t, namespace) })

		secret := createTestAPIKeySecret(aiModelSecret, namespace, aiModelSecretKey, aiModelSecretValue)
		require.NoError(t, k8sClient.Create(ctx, secret))

		instance := &clawv1alpha1.Claw{}
		instance.Name = testInstanceName
		instance.Namespace = namespace
		instance.Spec.Credentials = testCredentials() // google provider has gemini embedding adapter (vectors ON)
		instance.Spec.Memory = &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)}
		require.NoError(t, k8sClient.Create(ctx, instance))

		reconciler := createClawReconciler()
		reconcileClaw(t, ctx, reconciler, testInstanceName, namespace)

		deployment := &appsv1.Deployment{}
		waitFor(t, timeout, interval, func() bool {
			return k8sClient.Get(ctx, client.ObjectKey{
				Name: getClawDeploymentName(testInstanceName), Namespace: namespace,
			}, deployment) == nil
		}, "Deployment should be created")

		// The native stack needs no plugin installation.
		for _, ic := range deployment.Spec.Template.Spec.InitContainers {
			assert.NotEqual(t, PluginsInitContainerName, ic.Name,
				"the native memory stack must not add an init-plugins container")
		}

		// operator.json carries native memory config but no contextEngine slot.
		cm := &corev1.ConfigMap{}
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{
			Name: getConfigMapName(testInstanceName), Namespace: namespace,
		}, cm))
		assert.Contains(t, cm.Data[operatorJSONKey], "memory-wiki", "operator.json must contain memory-wiki")
		assert.Contains(t, cm.Data[operatorJSONKey], "memorySearch", "operator.json must contain memorySearch")
		assert.NotContains(t, cm.Data[operatorJSONKey], "contextEngine",
			"operator.json must not select a context engine")

		// MemoryStack condition is set with vectors enabled (google has gemini embedding adapter).
		updated := &clawv1alpha1.Claw{}
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: testInstanceName, Namespace: namespace}, updated))
		c := meta.FindStatusCondition(updated.Status.Conditions, clawv1alpha1.ConditionTypeMemoryStack)
		require.NotNil(t, c)
		assert.Equal(t, clawv1alpha1.ConditionReasonMemoryStackEnabled, c.Reason)
		assert.Equal(t, metav1.ConditionTrue, c.Status)
		assert.Contains(t, c.Message, "vector recall")
	})
}
