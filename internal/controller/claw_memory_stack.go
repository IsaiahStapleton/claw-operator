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
	"k8s.io/apimachinery/pkg/api/meta"
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
// inject the default stack (the user owns that config). Only a user-set
// plugins.slots.contextEngine counts: that replaces the native context engine
// wholesale, so seeding the native layers alongside it would be incoherent.
// A user-set plugins.entries.memory-core / memory-wiki does NOT suppress
// seeding. Those are tuning knobs on layers the stack still owns (e.g.
// dreaming.phases.deep.minScore), and injectMemoryStack seeds its defaults
// without overwriting keys the user set, so an override survives the merge.
// Treating a single tuned key as "user owns the whole stack" silently disabled
// every layer, which is the opposite of what the user asked for.
// memorySearch is intentionally NOT checked here: injectMemorySearch always
// sets memorySearch.provider or memorySearch.enabled, so checking it would
// cause the stack to skip on every normal operator-managed reconcile.
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
	return false
}

// setDefault sets key only when the user has not already set it. injectMemoryStack
// runs against a config that spec.config.raw has already been deep-merged into, so
// an unconditional write would clobber the user's value.
func setDefault(m map[string]any, key string, val any) {
	if _, ok := m[key]; !ok {
		m[key] = val
	}
}

// userConfiguredMemorySearch reports whether the user set
// agents.defaults.memorySearch in spec.config.raw. When they have, the operator
// backs off and their memorySearch config governs vector recall, so neither the
// stack injection nor the status condition should manage or claim it.
func userConfiguredMemorySearch(instance *clawv1alpha1.Claw) bool {
	rawCfg, _ := parseUserRawConfig(instance)
	return userHasMemorySearchConfig(rawCfg)
}

// userConfiguredMemoryStack reports whether the user set a context engine in
// spec.config.raw. It is the status-side counterpart of the guard
// injectMemoryStack applies to the merged config: spec.config.raw is the only
// source of plugins.slots.contextEngine (neither the operator.json template nor
// any other injector writes it), so the two agree on when the operator backs
// off. Tuning knobs under the memory-* entries do not count, matching
// userHasMemoryStackConfig.
func userConfiguredMemoryStack(instance *clawv1alpha1.Claw) bool {
	rawCfg, _ := parseUserRawConfig(instance)
	return userHasMemoryStackConfig(rawCfg)
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
	setDefault(dreaming, "enabled", true)

	wiki := ensureNestedMap(entries, "memory-wiki")
	setDefault(wiki, "enabled", true)
	wcfg := ensureNestedMap(wiki, "config")
	setDefault(wcfg, "vaultMode", "bridge")
	setDefault(ensureNestedMap(wcfg, "vault"), "path", "~/.openclaw/workspace/wiki/main")
	bridge := ensureNestedMap(wcfg, "bridge")
	setDefault(bridge, "enabled", true)
	setDefault(bridge, "readMemoryArtifacts", true)
	setDefault(bridge, "indexDreamReports", true)
	setDefault(bridge, "indexDailyNotes", true)
	setDefault(bridge, "indexMemoryRoot", true)
	setDefault(bridge, "followMemoryEvents", true)
	search := ensureNestedMap(wcfg, "search")
	setDefault(search, "backend", "shared")
	setDefault(search, "corpus", "all")
	render := ensureNestedMap(wcfg, "render")
	setDefault(render, "preserveHumanBlocks", true)
	setDefault(render, "createBacklinks", true)
	setDefault(render, "createDashboards", true)
}

const memoryHeartbeat = `# Heartbeat checklist

Keep this short to limit token burn.

- Review ` + "`memory/YYYY-MM-DD.md`" + ` for anything still open today.
- Fold anything durable into ` + "`MEMORY.md`" + `.
- Surface anything urgent; otherwise reply ` + "`HEARTBEAT_OK`" + `.

The memory wiki is auto-compiled from your memory; you do not edit it by hand.
`

// setMemoryStackCondition records the MemoryStack status condition. The
// condition is only reported for instances that opted in: like the
// McpServersConfigured condition, it is removed rather than set to False when
// the feature is not requested, so instances that never enabled memory do not
// grow a permanent condition. When the stack is on, the native memory layers
// function in every case the operator manages, so the condition is True and the
// reason reflects vector recall state.
func setMemoryStackCondition(instance *clawv1alpha1.Claw) {
	if !memoryStackEnabled(instance) {
		meta.RemoveStatusCondition(&instance.Status.Conditions, clawv1alpha1.ConditionTypeMemoryStack)
		return
	}

	// injectMemoryStack backs off entirely when the user selects their own
	// context engine, so the operator seeds nothing and cannot claim the stack is
	// applied. Report that explicitly instead of asserting an enabled stack the
	// operator did not configure. Tuning knobs on the memory-* entries do not
	// reach here: those still seed, so the stack really is applied.
	if userConfiguredMemoryStack(instance) {
		setCondition(instance, clawv1alpha1.ConditionTypeMemoryStack, metav1.ConditionFalse,
			clawv1alpha1.ConditionReasonMemoryStackUserManaged,
			"spec.memory.enabled is true but plugins.slots.contextEngine in spec.config.raw takes "+
				"precedence; the operator is not managing the memory layers")
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
