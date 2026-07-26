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
	"strings"

	clawv1alpha1 "github.com/codeready-toolchain/claw-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// dreamingEnabled reports whether memory-core's dreaming consolidation was
// requested. Off by default: only an explicit spec.memory.dreaming.enabled:
// true turns it on.
func dreamingEnabled(instance *clawv1alpha1.Claw) bool {
	m := instance.Spec.Memory
	if m == nil || m.Dreaming == nil || m.Dreaming.Enabled == nil {
		return false
	}
	return *m.Dreaming.Enabled
}

// wikiEnabled reports whether the memory-wiki layer was requested. Off by
// default: only an explicit spec.memory.wiki.enabled: true turns it on.
func wikiEnabled(instance *clawv1alpha1.Claw) bool {
	m := instance.Spec.Memory
	if m == nil || m.Wiki == nil || m.Wiki.Enabled == nil {
		return false
	}
	return *m.Wiki.Enabled
}

// memoryStackEnabled reports whether any memory layer was requested. The
// layers are independent; this is only the shared gate for concerns that
// apply once any of them is on (context-engine back-off, the memorySearch
// repair, the MemoryStack condition).
func memoryStackEnabled(instance *clawv1alpha1.Claw) bool {
	return dreamingEnabled(instance) || wikiEnabled(instance)
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
// memory.search is intentionally NOT checked here: injectMemorySearch always
// sets memory.search.provider or memory.search.enabled, so checking it would
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
// memory.search in spec.config.raw. When they have, the operator
// backs off and their memory.search config governs vector recall, so neither the
// stack injection nor the status condition should manage or claim it.
func userConfiguredMemorySearch(instance *clawv1alpha1.Claw) bool {
	rawCfg, _ := parseUserRawConfig(instance)
	return userHasMemorySearchConfig(rawCfg, true)
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

// injectMemoryStack writes the requested memory layers into operator.json.
// Each layer seeds independently; the memory.search repair applies once any
// layer is on, because vector recall is layer-independent. Skipped entirely
// when no layer is requested or the user owns memory config.
// userOwnsMemorySearch reports whether the user set memory.search in
// spec.config.raw (computed once by the caller); when true the operator does
// not enable vector recall, leaving that config to the user.
func injectMemoryStack(config map[string]any, instance *clawv1alpha1.Claw, userOwnsMemorySearch bool) {
	if !memoryStackEnabled(instance) || userHasMemoryStackConfig(config) {
		return
	}

	// injectMemorySearch already set memory.search.provider for this credential,
	// which leaves search enabled by default, so this looks redundant. It is not:
	// an instance that reconciled without an embedding-capable credential had
	// memory.search.enabled: false written to its PVC config, and deep-merge never
	// removes it. Writing true here repairs that stale value when a credential is
	// added later.
	if _, ok := firstEmbeddingProvider(instance); ok && !userOwnsMemorySearch {
		setNestedValue(config, true, "memory", "search", "enabled")
	}

	entries := ensureNestedMap(ensureNestedMap(config, "plugins"), "entries")
	if dreamingEnabled(instance) {
		injectDreaming(entries, instance.Spec.Memory.Dreaming)
	}
	if wikiEnabled(instance) {
		injectWiki(entries, instance.Spec.Memory.Wiki)
	}
}

// injectDreaming seeds the memory-core dreaming block. Seeded keys are
// defaults the user can override in spec.config.raw; explicit CRD fields
// (frequency, model) are operator-managed and overwrite a raw value, because
// a field set on the API is a stronger statement of intent than a merged
// config key.
func injectDreaming(entries map[string]any, spec *clawv1alpha1.DreamingSpec) {
	dreaming := ensureNestedMap(ensureNestedMap(ensureNestedMap(entries, "memory-core"), "config"), "dreaming")
	setDefault(dreaming, "enabled", true)
	if spec.Frequency != "" {
		dreaming["frequency"] = spec.Frequency
	}
	if spec.Model != "" {
		dreaming["model"] = spec.Model
	}
}

// injectWiki seeds the memory-wiki entry. An explicit CRD mode is
// operator-managed and overwrites a raw vaultMode; when unset, bridge is
// seeded as a default the user may override. The bridge indexing block only
// seeds when the effective mode is bridge, so an isolated vault does not
// carry dormant bridge config.
func injectWiki(entries map[string]any, spec *clawv1alpha1.WikiSpec) {
	wiki := ensureNestedMap(entries, "memory-wiki")
	setDefault(wiki, "enabled", true)
	wcfg := ensureNestedMap(wiki, "config")
	if spec.Mode != "" {
		wcfg["vaultMode"] = string(spec.Mode)
	} else {
		setDefault(wcfg, "vaultMode", string(clawv1alpha1.WikiModeBridge))
	}
	setDefault(ensureNestedMap(wcfg, "vault"), "path", "~/.openclaw/workspace/wiki/main")
	if wcfg["vaultMode"] == string(clawv1alpha1.WikiModeBridge) {
		bridge := ensureNestedMap(wcfg, "bridge")
		setDefault(bridge, "enabled", true)
		setDefault(bridge, "readMemoryArtifacts", true)
		setDefault(bridge, "indexDreamReports", true)
		setDefault(bridge, "indexDailyNotes", true)
		setDefault(bridge, "indexMemoryRoot", true)
		setDefault(bridge, "followMemoryEvents", true)
	}
	search := ensureNestedMap(wcfg, "search")
	setDefault(search, "backend", "shared")
	setDefault(search, "corpus", "all")
	render := ensureNestedMap(wcfg, "render")
	setDefault(render, "preserveHumanBlocks", true)
	setDefault(render, "createBacklinks", true)
	setDefault(render, "createDashboards", true)
}

// enabledMemoryLayers names the requested layers for condition messages, so
// the status says exactly which layers the operator is managing.
func enabledMemoryLayers(instance *clawv1alpha1.Claw) string {
	var layers []string
	if dreamingEnabled(instance) {
		layers = append(layers, "dreaming")
	}
	if wikiEnabled(instance) {
		layers = append(layers, "wiki")
	}
	return strings.Join(layers, ", ")
}

// setMemoryStackCondition records the MemoryStack status condition. The
// condition is only reported for instances that opted into at least one
// layer: like the McpServersConfigured condition, it is removed rather than
// set to False when the feature is not requested, so instances that never
// enabled memory do not grow a permanent condition. When layers are on, they
// function in every case the operator manages, so the condition is True and
// the reason reflects vector recall state.
func setMemoryStackCondition(instance *clawv1alpha1.Claw) {
	if !memoryStackEnabled(instance) {
		meta.RemoveStatusCondition(&instance.Status.Conditions, clawv1alpha1.ConditionTypeMemoryStack)
		return
	}
	layers := enabledMemoryLayers(instance)

	// injectMemoryStack backs off entirely when the user selects their own
	// context engine, so the operator seeds nothing and cannot claim the layers
	// are applied. Report that explicitly instead of asserting layers the
	// operator did not configure. Tuning knobs on the memory-* entries do not
	// reach here: those still seed, so the layers really are applied.
	if userConfiguredMemoryStack(instance) {
		setCondition(instance, clawv1alpha1.ConditionTypeMemoryStack, metav1.ConditionFalse,
			clawv1alpha1.ConditionReasonMemoryStackUserManaged,
			"spec.memory requests memory layers but plugins.slots.contextEngine in spec.config.raw "+
				"takes precedence; the operator is not managing the memory layers")
		return
	}

	// When the user owns memory.search via spec.config.raw the operator does not
	// manage vector recall, so the condition must not assert it is active — the
	// effective state is whatever the user configured.
	if userConfiguredMemorySearch(instance) {
		setCondition(instance, clawv1alpha1.ConditionTypeMemoryStack, metav1.ConditionTrue,
			clawv1alpha1.ConditionReasonMemoryStackEnabled,
			"Memory layers enabled ("+layers+"); vector recall follows your spec.config.raw memory.search setting")
		return
	}

	if _, ok := firstEmbeddingProvider(instance); ok {
		setCondition(instance, clawv1alpha1.ConditionTypeMemoryStack, metav1.ConditionTrue,
			clawv1alpha1.ConditionReasonMemoryStackEnabled,
			"Memory layers enabled ("+layers+") with vector recall")
		return
	}
	setCondition(instance, clawv1alpha1.ConditionTypeMemoryStack, metav1.ConditionTrue,
		clawv1alpha1.ConditionReasonMemoryStackNoVectors,
		"Memory layers enabled ("+layers+") without vector recall, no embedding-capable credential")
}
