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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clawv1alpha1 "github.com/codeready-toolchain/claw-operator/api/v1alpha1"
)

func TestDoctorFixJob(t *testing.T) {
	instance := &clawv1alpha1.Claw{}
	instance.Name = "example"
	instance.Namespace = "test"
	instance.Spec.Version = "2026.7.2-beta.1"
	instance.Spec.Migration = &clawv1alpha1.MigrationSpec{DoctorFix: true}

	job := doctorFixJob(instance)
	require.Equal(t, "test", job.Namespace)
	assert.Equal(t, effectiveOpenClawImage(instance), job.Annotations[doctorFixImageAnnotation])
	assert.Equal(t, int32(3), *job.Spec.BackoffLimit)
	assert.Equal(t, int64(600), *job.Spec.ActiveDeadlineSeconds)
	container := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, effectiveOpenClawImage(instance), container.Image)
	assert.Equal(t, []string{"sh", "-c", "node /config/merge.js && exec node /app/dist/index.js doctor --fix --non-interactive"}, container.Command)
	require.Len(t, job.Spec.Template.Spec.InitContainers, 1)
	initContainer := job.Spec.Template.Spec.InitContainers[0]
	assert.Equal(t, effectiveOpenClawImage(instance), initContainer.Image)
	assert.Equal(t, []string{"sh", "-c", "mkdir -p -m 0700 /mnt/pvc/home"}, initContainer.Command)
	require.Len(t, initContainer.VolumeMounts, 1)
	assert.Equal(t, "/mnt/pvc", initContainer.VolumeMounts[0].MountPath)
	require.Len(t, container.VolumeMounts, 2)
	assert.Equal(t, "/home/node", container.VolumeMounts[0].MountPath)
	assert.Equal(t, "home", container.VolumeMounts[0].SubPath)
	assert.Equal(t, "/config", container.VolumeMounts[1].MountPath)
	assert.True(t, container.VolumeMounts[1].ReadOnly)
	env := map[string]string{}
	for _, variable := range container.Env {
		env[variable.Name] = variable.Value
	}
	assert.Equal(t, "/home/node", env["HOME"])
	assert.Equal(t, "/home/node", env["OPENCLAW_HOME"])
	assert.Equal(t, "/home/node/.openclaw", env["OPENCLAW_STATE_DIR"])
	assert.Equal(t, "/home/node/.openclaw/openclaw.json", env["OPENCLAW_CONFIG_PATH"])
	assert.Equal(t, string(clawv1alpha1.ConfigModeMerge), env[ClawConfigModeEnvVar])
	assert.Equal(t, string(clawv1alpha1.ConfigManagementOperator), env[ClawConfigManagementEnvVar])
	assert.Equal(t, "1", env[doctorFixMergeEnv])
	require.Len(t, job.Spec.Template.Spec.Volumes, 2)
	assert.Equal(t, getPVCName(instance.Name), job.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName)
	assert.Equal(t, getConfigMapName(instance.Name), job.Spec.Template.Spec.Volumes[1].ConfigMap.Name)
}

func TestDoctorFixJobUsesUserConfigMergeSettings(t *testing.T) {
	instance := &clawv1alpha1.Claw{}
	instance.Spec.Config = &clawv1alpha1.ConfigSpec{
		Management: clawv1alpha1.ConfigManagementUser,
		MergeMode:  clawv1alpha1.ConfigModeOverwrite,
	}

	env := map[string]string{}
	for _, variable := range doctorFixJob(instance).Spec.Template.Spec.Containers[0].Env {
		env[variable.Name] = variable.Value
	}
	assert.Equal(t, string(clawv1alpha1.ConfigModeOverwrite), env[ClawConfigModeEnvVar])
	assert.Equal(t, string(clawv1alpha1.ConfigManagementUser), env[ClawConfigManagementEnvVar])
}

func TestPauseGatewayForDoctorFix(t *testing.T) {
	instance := &clawv1alpha1.Claw{}
	instance.Name = "example"
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"kind":     "Deployment",
		"metadata": map[string]any{"name": "example"},
		"spec":     map[string]any{"replicas": int64(1)},
	}}

	require.NoError(t, pauseGatewayForDoctorFix([]*unstructured.Unstructured{deployment}, instance))
	replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(0), replicas)
}

func TestDoctorFixCompletionIsScopedToTargetImage(t *testing.T) {
	instance := &clawv1alpha1.Claw{}
	instance.Spec.Version = "2026.7.2"
	instance.Spec.Migration = &clawv1alpha1.MigrationSpec{DoctorFix: true}
	instance.Status.Migration = &clawv1alpha1.MigrationStatus{DoctorFixImage: effectiveOpenClawImage(instance)}

	assert.True(t, doctorFixComplete(instance))
	instance.Spec.Version = "2026.8.0"
	assert.True(t, doctorFixPending(instance))
}

func TestDoctorFixRequestOverridesLegacyCompletionShortcut(t *testing.T) {
	instance := &clawv1alpha1.Claw{}
	instance.Annotations = map[string]string{configGenerationAnnotation: string(openClaw72ConfigGeneration)}
	instance.Spec.Version = "2026.7.2"
	instance.Spec.Migration = &clawv1alpha1.MigrationSpec{DoctorFix: true}
	instance.Status.LastDeployedVersion = instance.Spec.Version

	assert.False(t, doctorFixComplete(instance))
}

func TestReconcileDoctorFixRequeuesWhenDeploymentIsMissing(t *testing.T) {
	instance := &clawv1alpha1.Claw{}
	instance.Name = "missing-doctor-deployment"
	instance.Namespace = namespace
	require.NoError(t, k8sClient.Create(ctx, instance))
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, instance)
	})

	result, err := createClawReconciler().reconcileDoctorFix(ctx, instance)

	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, result.RequeueAfter)
	updated := &clawv1alpha1.Claw{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(instance), updated))
	ready := meta.FindStatusCondition(updated.Status.Conditions, clawv1alpha1.ConditionTypeReady)
	require.NotNil(t, ready)
	assert.Equal(t, "Migrating", ready.Reason)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
}
