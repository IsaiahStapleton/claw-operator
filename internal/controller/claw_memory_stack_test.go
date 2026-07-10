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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

	t.Run("skips when user configured a native layer entry", func(t *testing.T) {
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
		assert.False(t, hasCore, "no operator entries injected over a user override")
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

func makeConfigMapObjects() []*unstructured.Unstructured {
	cm := &unstructured.Unstructured{}
	cm.SetKind(ConfigMapKind)
	cm.SetName(getConfigMapName(testInstanceName))
	cm.Object["data"] = map[string]any{}
	return []*unstructured.Unstructured{cm}
}

func cmData(objects []*unstructured.Unstructured) map[string]any {
	data, _, _ := unstructured.NestedMap(objects[0].Object, "data")
	return data
}

func TestSetMemoryStackCondition(t *testing.T) {
	cond := func(instance *clawv1alpha1.Claw) *metav1.Condition {
		return meta.FindStatusCondition(instance.Status.Conditions, clawv1alpha1.ConditionTypeMemoryStack)
	}
	openaiCreds := []clawv1alpha1.CredentialSpec{{Name: "openai", Type: clawv1alpha1.CredentialTypeAPIKey, Provider: "openai"}}

	t.Run("disabled", func(t *testing.T) {
		instance := &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(false)}}}
		setMemoryStackCondition(instance)
		c := cond(instance)
		require.NotNil(t, c)
		assert.Equal(t, metav1.ConditionFalse, c.Status)
		assert.Equal(t, clawv1alpha1.ConditionReasonMemoryStackDisabled, c.Reason)
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

func TestInjectMemoryWorkspaceFiles(t *testing.T) {
	t.Run("seeds HEARTBEAT.md when enabled", func(t *testing.T) {
		objects := makeConfigMapObjects()
		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: namespace},
			Spec:       clawv1alpha1.ClawSpec{Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(true)}},
		}
		require.NoError(t, injectMemoryWorkspaceFiles(objects, instance))

		data := cmData(objects)
		_, ok := data["_ws_HEARTBEAT.md"].(string)
		assert.True(t, ok)
	})

	t.Run("no-op when disabled", func(t *testing.T) {
		objects := makeConfigMapObjects()
		instance := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: namespace},
			Spec:       clawv1alpha1.ClawSpec{Memory: &clawv1alpha1.MemorySpec{Enabled: ptr.To(false)}},
		}
		require.NoError(t, injectMemoryWorkspaceFiles(objects, instance))
		assert.Empty(t, cmData(objects))
	})
}

// --- Integration test ---

func TestMemoryStackIntegration(t *testing.T) {
	t.Run("fresh memory-on instance wires config, workspace files, condition", func(t *testing.T) {
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

		// ConfigMap carries the _ws_ workspace files.
		_, hasHeartbeat := cm.Data["_ws_HEARTBEAT.md"]
		assert.True(t, hasHeartbeat, "HEARTBEAT.md workspace file seeded")

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
