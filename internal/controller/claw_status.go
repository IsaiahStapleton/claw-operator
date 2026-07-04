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
	"fmt"
	"net/url"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/codeready-toolchain/claw-operator/api/v1alpha1"
)

// getDeploymentAvailableStatus fetches a Deployment and returns whether its Available condition is True
func (r *ClawResourceReconciler) getDeploymentAvailableStatus(ctx context.Context, namespace, name string) (bool, error) {
	logger := log.FromContext(ctx)
	deployment := &appsv1.Deployment{}
	err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, deployment)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Deployment not found", "name", name)
			return false, nil
		}
		return false, err
	}

	// check for Available condition
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentAvailable {
			return condition.Status == corev1.ConditionTrue, nil
		}
	}

	// No Available condition found
	return false, nil
}

// checkDeploymentsReady checks if all managed Deployments are ready.
func (r *ClawResourceReconciler) checkDeploymentsReady(ctx context.Context, namespace, instanceName string) (bool, []string, error) {
	deployNames := []string{
		getClawDeploymentName(instanceName),
		getProxyDeploymentName(instanceName),
	}

	var pending []string
	for _, name := range deployNames {
		ready, err := r.getDeploymentAvailableStatus(ctx, namespace, name)
		if err != nil {
			return false, nil, err
		}
		if !ready {
			pending = append(pending, name)
		}
	}

	return len(pending) == 0, pending, nil
}

// getRouteURL fetches the Route and returns the HTTPS URL, or empty string if not found
func (r *ClawResourceReconciler) getRouteURL(ctx context.Context, instance *clawv1alpha1.Claw) (string, error) {
	logger := log.FromContext(ctx)
	routeName := getRouteName(instance.Name)

	// Create an unstructured object to fetch the Route (OpenShift-specific resource)
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "route.openshift.io",
		Version: "v1",
		Kind:    RouteKind,
	})

	if err := r.Get(ctx, client.ObjectKey{
		Namespace: instance.Namespace,
		Name:      routeName,
	}, route); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			// Route not found (or CRD not registered on non-OpenShift clusters)
			logger.Info("Route not found or CRD not registered", "name", routeName)
			return "", nil
		}
		return "", fmt.Errorf("failed to get Route: %w", err)
	}

	// Extract host from Route.Status.Ingress[0].Host (authoritative source)
	ingress, found, err := unstructured.NestedSlice(route.Object, "status", "ingress")
	if err != nil {
		return "", fmt.Errorf("failed to extract ingress from Route status: %w", err)
	}
	if !found || len(ingress) == 0 {
		// Route exists but status not yet populated by OpenShift router
		return "", nil
	}

	// Get first ingress entry (primary router)
	firstIngress, ok := ingress[0].(map[string]any)
	if !ok {
		return "", fmt.Errorf("failed to parse ingress entry")
	}

	host, found, err := unstructured.NestedString(firstIngress, "host")
	if err != nil {
		return "", fmt.Errorf("failed to extract host from ingress: %w", err)
	}
	if !found || host == "" {
		// Ingress entry exists but host not yet populated
		return "", nil
	}

	return "https://" + host, nil
}

// setCondition is a generic helper to set a condition on the Claw instance.
func setCondition(instance *clawv1alpha1.Claw, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: instance.Generation,
	})
}

// setReadyCondition sets the Ready condition on the Claw instance based on deployment readiness
func setReadyCondition(instance *clawv1alpha1.Claw, ready bool, pendingDeployments []string) {
	var status metav1.ConditionStatus
	var reason, message string

	if ready {
		status = metav1.ConditionTrue
		reason = clawv1alpha1.ConditionReasonReady
		message = "Claw instance is ready"
	} else {
		status = metav1.ConditionFalse
		reason = clawv1alpha1.ConditionReasonProvisioning
		if len(pendingDeployments) > 0 {
			message = "Waiting for deployments to become ready"
		} else {
			message = "Provisioning in progress"
		}
	}

	meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
		Type:               clawv1alpha1.ConditionTypeReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: instance.Generation,
	})
}

// setReadyConditionWithDetail sets the Ready condition, enriching the message
// with init container failure details when available.
func setReadyConditionWithDetail(
	instance *clawv1alpha1.Claw,
	ready bool,
	pendingDeployments []string,
	initFailureDetail string,
) {
	if ready || initFailureDetail == "" {
		setReadyCondition(instance, ready, pendingDeployments)
		return
	}

	meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
		Type:               clawv1alpha1.ConditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             clawv1alpha1.ConditionReasonInitContainerFailure,
		Message:            initFailureDetail,
		ObservedGeneration: instance.Generation,
	})
}

// checkPodInitFailures inspects pods owned by the named Deployment for
// init container failures (non-zero exit or CrashLoopBackOff). Returns
// a human-readable description or "" if no failures are found.
func (r *ClawResourceReconciler) checkPodInitFailures(
	ctx context.Context,
	namespace, deploymentName string,
) (string, error) {
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: namespace, Name: deploymentName,
	}, deployment); err != nil {
		return "", fmt.Errorf("get deployment %s: %w", deploymentName, err)
	}

	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return "", fmt.Errorf("build selector for %s: %w", deploymentName, err)
	}

	pods := &corev1.PodList{}
	if err := r.List(ctx, pods,
		client.InNamespace(namespace),
		client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		return "", fmt.Errorf("list pods for %s: %w", deploymentName, err)
	}

	var failures []string
	for i := range pods.Items {
		for _, cs := range pods.Items[i].Status.InitContainerStatuses {
			if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
				msg := t.Reason
				if t.Message != "" {
					msg = t.Message
				}
				failures = append(failures, fmt.Sprintf(
					"init container %q failed (exit %d): %s",
					cs.Name, t.ExitCode, msg))
			} else if w := cs.State.Waiting; w != nil &&
				w.Reason == "CrashLoopBackOff" {
				failures = append(failures, fmt.Sprintf(
					"init container %q in CrashLoopBackOff: %s",
					cs.Name, w.Message))
			}
		}
	}

	if len(failures) == 0 {
		return "", nil
	}
	return strings.Join(failures, "; "), nil
}

// getGatewayToken fetches the gateway token from the gateway token Secret and Base64-decodes it.
// Returns the token string, or empty string if the Secret cannot be read.
func (r *ClawResourceReconciler) getGatewayToken(ctx context.Context, namespace, instanceName string) string {
	logger := log.FromContext(ctx)
	secretName := getGatewaySecretName(instanceName)

	secret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      secretName,
	}, secret); err != nil {
		logger.Error(err, "Failed to get gateway secret for status URL", "secret", secretName)
		return ""
	}

	tokenBytes, exists := secret.Data[GatewayTokenKeyName]
	if !exists || len(tokenBytes) == 0 {
		logger.Info("Gateway token not found in secret", "secret", secretName, "key", GatewayTokenKeyName)
		return ""
	}

	// Secret data is already raw bytes (not Base64-encoded in the Data field)
	// Kubernetes automatically handles Base64 decoding when accessing Secret.Data
	return string(tokenBytes)
}

// encodeFragmentValue percent-encodes a string for safe use in a URL fragment.
// This ensures special characters don't break URL parsing.
func encodeFragmentValue(v string) string {
	return url.QueryEscape(v)
}

// buildGatewayURL constructs the Claw status URL by appending the gateway token
// as a URL fragment if both routeURL and token are provided.
// Returns empty string if routeURL is empty.
func buildGatewayURL(routeURL, token string) string {
	if routeURL == "" {
		return ""
	}
	if token == "" {
		return routeURL
	}
	return routeURL + "#token=" + encodeFragmentValue(token)
}

// updateStatus updates the Claw status with current deployment conditions
func (r *ClawResourceReconciler) updateStatus(ctx context.Context, instance *clawv1alpha1.Claw) error {
	// check deployment readiness
	ready, pending, err := r.checkDeploymentsReady(ctx, instance.Namespace, instance.Name)
	if err != nil {
		return fmt.Errorf("failed to check deployment readiness: %w", err)
	}

	// When deployments are not ready, inspect pods for init container failures
	logger := log.FromContext(ctx)
	var initFailureDetail string
	if !ready {
		for _, depName := range pending {
			detail, err := r.checkPodInitFailures(ctx, instance.Namespace, depName)
			if err != nil {
				logger.Error(err, "failed to inspect init container failures", "deployment", depName)
				continue
			}
			if detail != "" {
				initFailureDetail = detail
				break
			}
		}
	}

	// Set Ready condition (with init failure detail if available)
	setReadyConditionWithDetail(instance, ready, pending, initFailureDetail)

	// Expose gateway secret name in status
	instance.Status.GatewayTokenSecretRef = getGatewaySecretName(instance.Name)
	instance.Status.Image = effectiveOpenClawImage(instance)

	// Populate URL fields only when all deployments are ready
	if ready {
		routeURL, err := r.getRouteURL(ctx, instance)
		if err != nil {
			return fmt.Errorf("failed to get Route URL: %w", err)
		}

		// Password mode: URL has no token fragment (password is entered in the UI)
		if instance.Spec.Auth != nil && instance.Spec.Auth.Mode == clawv1alpha1.AuthModePassword {
			instance.Status.URL = routeURL //nolint:staticcheck // deprecated but still populated
			instance.Status.GatewayURL = routeURL
		} else {
			token := r.getGatewayToken(ctx, instance.Namespace, instance.Name)
			gatewayURL := buildGatewayURL(routeURL, token)
			instance.Status.URL = gatewayURL //nolint:staticcheck // deprecated but still populated
			instance.Status.GatewayURL = gatewayURL
		}
	} else {
		// Clear URLs when deployments are not ready
		instance.Status.URL = "" //nolint:staticcheck // deprecated but still populated
		instance.Status.GatewayURL = ""
	}

	// Version downgrade detection
	cmp, cmpOK := compareCalver(instance.Spec.Version, instance.Status.LastDeployedVersion)
	if instance.Spec.Version != "" &&
		instance.Status.LastDeployedVersion != "" &&
		cmpOK && cmp < 0 {
		setCondition(instance,
			clawv1alpha1.ConditionTypeVersionDowngrade,
			metav1.ConditionTrue,
			clawv1alpha1.ConditionReasonVersionDowngrade,
			fmt.Sprintf(
				"spec.version %s is older than previously deployed version %s; "+
					"PVC data may be incompatible",
				instance.Spec.Version, instance.Status.LastDeployedVersion),
		)
	} else {
		meta.RemoveStatusCondition(&instance.Status.Conditions,
			clawv1alpha1.ConditionTypeVersionDowngrade)
	}

	// Track last deployed version when ready (high-water mark: only update upward
	// so that a downgrade preserves the previous version for comparison)
	if ready && instance.Spec.Version != "" {
		cmpTrack, cmpTrackOK := compareCalver(instance.Spec.Version, instance.Status.LastDeployedVersion)
		if instance.Status.LastDeployedVersion == "" || !cmpTrackOK || cmpTrack >= 0 {
			instance.Status.LastDeployedVersion = instance.Spec.Version
		}
	}

	recordClawMetrics(instance)

	// Update status subresource
	if err := r.Status().Update(ctx, instance); err != nil {
		return fmt.Errorf("failed to update Claw status: %w", err)
	}
	return nil
}
