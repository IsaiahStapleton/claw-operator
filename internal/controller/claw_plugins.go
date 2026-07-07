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
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	clawv1alpha1 "github.com/codeready-toolchain/claw-operator/api/v1alpha1"
)

const (
	PluginsInitContainerName = "init-plugins"
)

func pluginsEnabled(instance *clawv1alpha1.Claw) bool {
	return len(instance.Spec.Plugins) > 0
}

// pluginPackageName strips a trailing @version suffix from a plugin spec,
// returning just the package name for deduplication purposes.
func pluginPackageName(spec string) string {
	// Scoped packages start with @, so find the LAST @ for the version separator.
	// "@openclaw/foo@1.2.3" → "@openclaw/foo"
	// "@openclaw/foo" → "@openclaw/foo"
	if idx := strings.LastIndex(spec, "@"); idx > 0 {
		return spec[:idx]
	}
	return spec
}

// effectivePlugins returns the complete list of plugins to install: explicit
// spec.plugins plus any implicitly required by the configured credentials
// (e.g., Vertex AI SDK providers that need an external plugin).
// Duplicates are removed by package name (spec declarations take precedence
// over implicit ones, allowing users to override the pinned version).
func effectivePlugins(instance *clawv1alpha1.Claw) []string {
	implicit := operatorRequiredPlugins(instance)
	if len(implicit) == 0 {
		return instance.Spec.Plugins
	}
	seen := make(map[string]bool, len(instance.Spec.Plugins))
	for _, p := range instance.Spec.Plugins {
		seen[pluginPackageName(p)] = true
	}
	merged := append([]string{}, instance.Spec.Plugins...)
	for _, p := range implicit {
		if !seen[pluginPackageName(p)] {
			merged = append(merged, p)
			seen[pluginPackageName(p)] = true
		}
	}
	return merged
}

// operatorRequiredPlugins returns plugins that are infrastructure for CR-backed
// features. These remain operator-managed even when OpenClaw runtime config is
// otherwise user-managed.
func operatorRequiredPlugins(instance *clawv1alpha1.Claw) []string {
	plugins := requiredProviderPlugins(instance)
	plugins = append(plugins, requiredDiagnosticsPlugins(instance)...)
	return plugins
}

// requiredProviderPlugins inspects credentials and returns plugin package specs
// that must be installed for the configured providers to work. When spec.version
// is set, the plugin is pinned to that version so the plugin and gateway stay in
// lockstep (e.g. "@openclaw/anthropic-vertex-provider@2026.6.8").
func requiredProviderPlugins(instance *clawv1alpha1.Claw) []string {
	var plugins []string
	seen := make(map[string]bool)
	for _, cred := range instance.Spec.Credentials {
		if !usesVertexSDK(cred) {
			continue
		}
		defaults, ok := knownProviders[cred.Provider]
		if !ok || defaults.VertexPlugin == "" {
			continue
		}
		if !seen[defaults.VertexPlugin] {
			spec := defaults.VertexPlugin
			if instance.Spec.Version != "" {
				spec += "@" + instance.Spec.Version
			}
			plugins = append(plugins, spec)
			seen[defaults.VertexPlugin] = true
		}
	}
	return plugins
}

// injectProviderPlugins adds plugins.entries declarations for any provider
// plugins that need to be loaded by the gateway at runtime. Installing a
// plugin to disk (via init-plugins) is not enough; the gateway only loads
// extension plugins that are declared in plugins.entries.
func injectProviderPlugins(config map[string]any, instance *clawv1alpha1.Claw) {
	var ids []string
	seen := make(map[string]bool)
	for _, cred := range instance.Spec.Credentials {
		if !usesVertexSDK(cred) {
			continue
		}
		defaults, ok := knownProviders[cred.Provider]
		if !ok || defaults.VertexPluginID == "" {
			continue
		}
		if !seen[defaults.VertexPluginID] {
			ids = append(ids, defaults.VertexPluginID)
			seen[defaults.VertexPluginID] = true
		}
	}
	if len(ids) == 0 {
		return
	}
	existingEntries := ensureNestedMap(ensureNestedMap(config, "plugins"), "entries")
	for _, id := range ids {
		if _, exists := existingEntries[id]; !exists {
			existingEntries[id] = map[string]any{"enabled": true}
		}
	}
}

func requiredDiagnosticsPlugins(instance *clawv1alpha1.Claw) []string {
	var plugins []string
	if tracesEnabled(instance) || logsEnabled(instance) {
		plugins = append(plugins, "@openclaw/diagnostics-otel")
	}
	if metricsEnabled(instance) {
		plugins = append(plugins, "@openclaw/diagnostics-prometheus")
	}
	return plugins
}

func generatePluginInstallScript(plugins []string, preserveUnmanagedOpt ...bool) string {
	preserveUnmanaged := len(preserveUnmanagedOpt) > 0 && preserveUnmanagedOpt[0]
	var b strings.Builder
	b.WriteString(`set -e
EXT="/home/node/.openclaw/extensions"
MANIFEST="$EXT/.operator-managed"
if [ -f "$MANIFEST" ]; then
  while IFS= read -r dir; do
    case "$dir" in
      ""|.|..|*/*|*..*) continue ;;
    esac
    target="$EXT/$dir"
    [ -e "$target" ] || continue
    rm -rf -- "$target"
  done < "$MANIFEST"
  rm -f "$MANIFEST"
`)
	if !preserveUnmanaged {
		b.WriteString(`else
  # No manifest from a previous successful install — clean all extension
  # dirs to avoid "plugin already exists" errors from orphaned directories
  # left by pods killed mid-install or pre-manifest operator versions.
  find "$EXT" -mindepth 1 -maxdepth 1 -type d -exec rm -rf {} + 2>/dev/null || true
`)
	}
	b.WriteString(`fi
mkdir -p "$EXT"
ls "$EXT" 2>/dev/null | sort > /tmp/before-plugins.txt
`)
	for _, pkg := range plugins {
		escaped := "'" + strings.ReplaceAll(pkg, "'", "'\\''") + "'"
		fmt.Fprintf(&b, "openclaw plugins install clawhub:%s\n", escaped)
	}
	b.WriteString(`ls "$EXT" | sort | comm -13 /tmp/before-plugins.txt - > "$MANIFEST"
`)
	return b.String()
}

// configurePluginsInitContainer adds an init-plugins init container to the
// gateway Deployment when plugins need to be installed. The container runs the
// openclaw CLI to install each declared plugin on the shared PVC. It goes
// through the MITM proxy (appended after wait-for-proxy).
func configurePluginsInitContainer(
	objects []*unstructured.Unstructured,
	instance *clawv1alpha1.Claw,
	plugins []string,
	preserveUnmanagedOpt ...bool,
) error {
	if len(plugins) == 0 {
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

		var gatewayImage string
		for _, c := range containers {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if name, _, _ := unstructured.NestedString(cm, "name"); name == ClawGatewayContainerName {
				gatewayImage, _, _ = unstructured.NestedString(cm, "image")
				break
			}
		}
		if gatewayImage == "" {
			return fmt.Errorf("gateway container image not found in claw deployment")
		}

		initContainers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "initContainers")

		proxyHost := fmt.Sprintf("http://%s-proxy:8080", instance.Name)
		preserveUnmanaged := len(preserveUnmanagedOpt) > 0 && preserveUnmanagedOpt[0]
		script := generatePluginInstallScript(plugins, preserveUnmanaged)

		initContainers = append(initContainers, map[string]any{
			"name":            PluginsInitContainerName,
			"image":           gatewayImage,
			"imagePullPolicy": "IfNotPresent",
			"command":         []any{"sh", "-c", script},
			"env": []any{
				map[string]any{"name": "HOME", "value": "/home/node"},
				map[string]any{"name": "NPM_CONFIG_CACHE", "value": "/home/node/.cache/npm"},
				map[string]any{"name": "HTTP_PROXY", "value": proxyHost},
				map[string]any{"name": "HTTPS_PROXY", "value": proxyHost},
				map[string]any{"name": "NO_PROXY", "value": pluginsNoProxy(instance)},
				map[string]any{"name": "NODE_EXTRA_CA_CERTS", "value": "/etc/proxy-ca/ca.crt"},
			},
			"resources": map[string]any{
				"requests": map[string]any{"memory": "128Mi", "cpu": "100m"},
				"limits":   map[string]any{"memory": "512Mi", "cpu": "500m"},
			},
			"securityContext": map[string]any{
				"allowPrivilegeEscalation": false,
				"capabilities":             map[string]any{"drop": []any{"ALL"}},
			},
			"volumeMounts": []any{
				map[string]any{
					"name":      "claw-home",
					"mountPath": "/home/node",
					"subPath":   "home",
				},
				map[string]any{
					"name":      "proxy-ca",
					"mountPath": "/etc/proxy-ca",
					"readOnly":  true,
				},
				map[string]any{
					"name":      "tmp-volume",
					"mountPath": "/tmp",
				},
			},
		})

		if err := unstructured.SetNestedSlice(obj.Object, initContainers, "spec", "template", "spec", "initContainers"); err != nil {
			return fmt.Errorf("failed to set init containers on claw deployment: %w", err)
		}
		return nil
	}
	return fmt.Errorf("claw deployment not found in manifests")
}

// pluginsNoProxy returns the NO_PROXY value for the plugins init container.
func pluginsNoProxy(instance *clawv1alpha1.Claw) string {
	base := "localhost,127.0.0.1"
	if inClusterBypassEnabled(instance) {
		return base + noProxySuffix
	}
	return base
}

// compareCalver compares two calver version strings (e.g. "2026.6.5").
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
// The bool is false when either string is malformed (non-numeric segments).
func compareCalver(a, b string) (int, bool) {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	maxLen := len(aParts)
	if len(bParts) > maxLen {
		maxLen = len(bParts)
	}

	for i := range maxLen {
		var aVal, bVal int
		var err error
		if i < len(aParts) {
			aVal, err = strconv.Atoi(aParts[i])
			if err != nil {
				return 0, false
			}
		}
		if i < len(bParts) {
			bVal, err = strconv.Atoi(bParts[i])
			if err != nil {
				return 0, false
			}
		}
		if aVal < bVal {
			return -1, true
		}
		if aVal > bVal {
			return 1, true
		}
	}
	return 0, true
}

// checkPluginCompatibility checks whether any implicitly required plugin
// has a minimum version that exceeds spec.version. Returns a warning
// message or "" if all plugins are compatible.
func checkPluginCompatibility(instance *clawv1alpha1.Claw) string {
	if instance.Spec.Version == "" {
		return ""
	}
	for _, cred := range instance.Spec.Credentials {
		if !usesVertexSDK(cred) {
			continue
		}
		defaults, ok := knownProviders[cred.Provider]
		if !ok || defaults.VertexPlugin == "" || defaults.PluginMinVersion == "" {
			continue
		}
		cmp, ok := compareCalver(instance.Spec.Version, defaults.PluginMinVersion)
		if !ok {
			return fmt.Sprintf(
				"cannot check plugin compatibility: spec.version %q is not a valid CalVer string",
				instance.Spec.Version,
			)
		}
		if cmp < 0 {
			return fmt.Sprintf(
				"plugin %s requires OpenClaw >= %s, but spec.version is %s",
				defaults.VertexPlugin, defaults.PluginMinVersion, instance.Spec.Version,
			)
		}
	}
	return ""
}
