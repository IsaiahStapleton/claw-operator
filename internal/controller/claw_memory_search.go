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
	clawv1alpha1 "github.com/codeready-toolchain/claw-operator/api/v1alpha1"
)

// userHasMemorySearchConfig returns true when the merged config has a
// schema-appropriate user override, so the operator must not replace it.
func userHasMemorySearchConfig(config map[string]any, use72Config bool) bool {
	if !use72Config {
		agents, ok := config["agents"].(map[string]any)
		if !ok {
			return false
		}
		defaults, ok := agents["defaults"].(map[string]any)
		if !ok {
			return false
		}
		_, ok = defaults["memorySearch"]
		return ok
	}
	memory, ok := config["memory"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = memory["search"]
	return ok
}

// firstEmbeddingProvider returns the OpenClaw memory-search adapter for the
// first embedding-capable credential. GCP credentials (Vertex AI) are skipped.
// ok is false when none is eligible.
func firstEmbeddingProvider(instance *clawv1alpha1.Claw) (adapter string, ok bool) {
	for _, cred := range instance.Spec.Credentials {
		if cred.Type == clawv1alpha1.CredentialTypeGCP {
			continue
		}
		if defaults, found := knownProviders[cred.Provider]; found && defaults.EmbeddingAdapter != "" {
			return defaults.EmbeddingAdapter, true
		}
	}
	return "", false
}

// injectMemorySearch auto-configures memory search based on
// the first embedding-capable credential. GCP credentials (Vertex AI) are
// skipped because the gemini adapter expects API key auth, not OAuth2 tokens.
// If no eligible provider is found, memory search is explicitly disabled to
// suppress noisy runtime errors. User-provided memorySearch config in
// spec.config.raw takes full precedence; the operator never overrides it.
func injectMemorySearch(config map[string]any, instance *clawv1alpha1.Claw) {
	injectMemorySearchForGeneration(config, instance, openClaw72ConfigGeneration)
}

func injectMemorySearchForGeneration(config map[string]any, instance *clawv1alpha1.Claw, generation configGeneration) {
	use72Config := generation == openClaw72ConfigGeneration
	if userHasMemorySearchConfig(config, use72Config) {
		return
	}

	if adapter, ok := firstEmbeddingProvider(instance); ok {
		if use72Config {
			setNestedValue(config, adapter, "memory", "search", "provider")
		} else {
			setNestedValue(config, adapter, "agents", "defaults", "memorySearch", "provider")
		}
		return
	}

	if use72Config {
		setNestedValue(config, false, "memory", "search", "enabled")
	} else {
		setNestedValue(config, false, "agents", "defaults", "memorySearch", "enabled")
	}
}
