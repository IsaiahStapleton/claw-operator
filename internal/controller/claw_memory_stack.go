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
	"fmt"

	clawv1alpha1 "github.com/codeready-toolchain/claw-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// memoryStackEnabled reports whether the default memory/context stack should be
// applied. The stack is OFF by default: a nil spec.memory or a nil
// spec.memory.enabled both resolve to disabled. Only an explicit
// spec.memory.enabled: true turns the stack on.
func memoryStackEnabled(instance *clawv1alpha1.Claw) bool {
	if instance.Spec.Memory == nil || instance.Spec.Memory.Enabled == nil {
		return false
	}
	return *instance.Spec.Memory.Enabled
}

// userHasMemoryStackConfig reports whether the merged config already carries
// user-set plugin-level memory configuration. If so, the operator does not
// inject the default stack (the user owns that config). It treats a user-set
// plugins.slots.contextEngine, or any of the plugins.entries the stack writes
// (memory-core, memory-wiki), as an override.
// memorySearch is intentionally NOT checked here: injectMemorySearch always
// sets memorySearch.provider or memorySearch.enabled, so checking it would
// cause the stack to skip on every normal operator-managed reconcile.
// Note: a user who sets only memorySearch in spec.config.raw does NOT suppress
// plugin-stack seeding (memory-core, memory-wiki still seed); that is by
// design, since those layers are independent of the vector provider and
// deep-merge lets users override individual plugin entries.
func userHasMemoryStackConfig(config map[string]any) bool {
	plugins, ok := config["plugins"].(map[string]any)
	if !ok {
		return false
	}
	if slots, ok := plugins["slots"].(map[string]any); ok {
		if _, ok := slots["contextEngine"]; ok {
			return true
		}
	}
	if entries, ok := plugins["entries"].(map[string]any); ok {
		for _, name := range []string{"memory-core", "memory-wiki"} {
			if _, ok := entries[name]; ok {
				return true
			}
		}
	}
	return false
}

// userConfiguredMemorySearch reports whether the user set
// agents.defaults.memorySearch in spec.config.raw. When they have, the operator
// backs off and their memorySearch config governs vector recall, so neither the
// stack injection nor the status condition should manage or claim it.
func userConfiguredMemorySearch(instance *clawv1alpha1.Claw) bool {
	rawCfg, _ := parseUserRawConfig(instance)
	return userHasMemorySearchConfig(rawCfg)
}

// injectMemoryStack writes the default memory/context stack into operator.json:
// native layers (memory-core dreaming, memory-wiki, and vector recall when an
// embedding credential exists) are seeded whenever memory is enabled. Skipped
// entirely when the stack is off or the user owns memory config.
// userOwnsMemorySearch reports whether the user set memorySearch in
// spec.config.raw (computed once by the caller); when true the operator does
// not enable vector recall, leaving that config to the user.
func injectMemoryStack(config map[string]any, instance *clawv1alpha1.Claw, userOwnsMemorySearch bool) {
	if !memoryStackEnabled(instance) || userHasMemoryStackConfig(config) {
		return
	}

	if _, ok := firstEmbeddingProvider(instance); ok && !userOwnsMemorySearch {
		setNestedValue(config, true, "agents", "defaults", "memorySearch", "enabled")
	}

	entries := ensureNestedMap(ensureNestedMap(config, "plugins"), "entries")

	dreaming := ensureNestedMap(ensureNestedMap(ensureNestedMap(entries, "memory-core"), "config"), "dreaming")
	dreaming["enabled"] = true

	wiki := ensureNestedMap(entries, "memory-wiki")
	wiki["enabled"] = true
	wcfg := ensureNestedMap(wiki, "config")
	wcfg["vaultMode"] = "bridge"
	ensureNestedMap(wcfg, "vault")["path"] = "~/.openclaw/workspace/wiki/main"
	bridge := ensureNestedMap(wcfg, "bridge")
	bridge["enabled"] = true
	bridge["readMemoryArtifacts"] = true
	bridge["indexDreamReports"] = true
	bridge["indexDailyNotes"] = true
	bridge["indexMemoryRoot"] = true
	bridge["followMemoryEvents"] = true
	search := ensureNestedMap(wcfg, "search")
	search["backend"] = "shared"
	search["corpus"] = "all"
	render := ensureNestedMap(wcfg, "render")
	render["preserveHumanBlocks"] = true
	render["createBacklinks"] = true
	render["createDashboards"] = true
}

const memoryHeartbeat = `# Heartbeat checklist

Keep this short to limit token burn.

- Review ` + "`memory/YYYY-MM-DD.md`" + ` for anything still open today.
- Fold anything durable into ` + "`MEMORY.md`" + `.
- Surface anything urgent; otherwise reply ` + "`HEARTBEAT_OK`" + `.

The memory wiki is auto-compiled from your memory; you do not edit it by hand.
`

// setMemoryStackCondition records the MemoryStack status condition. The native
// memory layers function whenever the stack is enabled, so the condition is True
// for every enabled case; the reason reflects vector recall state.
func setMemoryStackCondition(instance *clawv1alpha1.Claw) {
	if !memoryStackEnabled(instance) {
		setCondition(instance, clawv1alpha1.ConditionTypeMemoryStack, metav1.ConditionFalse,
			clawv1alpha1.ConditionReasonMemoryStackDisabled, "Memory stack disabled via spec.memory.enabled")
		return
	}

	// When the user owns memorySearch via spec.config.raw the operator does not
	// manage vector recall, so the condition must not assert it is active — the
	// effective state is whatever the user configured.
	if userConfiguredMemorySearch(instance) {
		setCondition(instance, clawv1alpha1.ConditionTypeMemoryStack, metav1.ConditionTrue,
			clawv1alpha1.ConditionReasonMemoryStackEnabled,
			"Memory stack enabled; vector recall follows your spec.config.raw memorySearch setting")
		return
	}

	if _, ok := firstEmbeddingProvider(instance); ok {
		setCondition(instance, clawv1alpha1.ConditionTypeMemoryStack, metav1.ConditionTrue,
			clawv1alpha1.ConditionReasonMemoryStackEnabled,
			"Memory stack enabled with vector recall")
		return
	}
	setCondition(instance, clawv1alpha1.ConditionTypeMemoryStack, metav1.ConditionTrue,
		clawv1alpha1.ConditionReasonMemoryStackNoVectors,
		"Memory stack enabled without vector recall, no embedding-capable credential")
}

// injectMemoryWorkspaceFiles seeds the memory stack's workspace files as _ws_
// ConfigMap keys (HEARTBEAT.md). The merge.js _ws_ loop seeds them once on the
// PVC in any management mode, so user edits survive. No-op when the stack is
// disabled.
func injectMemoryWorkspaceFiles(objects []*unstructured.Unstructured, instance *clawv1alpha1.Claw) error {
	if !memoryStackEnabled(instance) {
		return nil
	}
	cmObj, err := findObject(objects, ConfigMapKind, getConfigMapName(instance.Name))
	if err != nil {
		return fmt.Errorf("ConfigMap not found in manifests: %w", err)
	}
	files := map[string]string{
		"HEARTBEAT.md": memoryHeartbeat,
	}
	for p, content := range files {
		key := workspaceKeyPrefix + encodeWorkspacePath(p)
		if err := unstructured.SetNestedField(cmObj.Object, content, "data", key); err != nil {
			return fmt.Errorf("failed to set memory workspace file %q: %w", p, err)
		}
	}
	return nil
}
