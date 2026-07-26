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
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	clawv1alpha1 "github.com/codeready-toolchain/claw-operator/api/v1alpha1"
)

const (
	doctorFixImageAnnotation = "claw.sandbox.redhat.com/doctor-fix-image"
	doctorFixMergeEnv        = "OPENCLAW_DOCTOR_MIGRATION"
)

func doctorFixRequested(instance *clawv1alpha1.Claw) bool {
	return instance.Spec.Migration != nil && instance.Spec.Migration.DoctorFix
}

func doctorFixComplete(instance *clawv1alpha1.Claw) bool {
	if doctorFixRequested(instance) {
		return instance.Status.Migration != nil &&
			instance.Status.Migration.DoctorFixImage == effectiveOpenClawImage(instance)
	}
	if instance.GetAnnotations()[configGenerationAnnotation] == string(openClaw72ConfigGeneration) &&
		instance.Spec.Image == "" &&
		instance.Status.LastDeployedVersion == instance.Spec.Version {
		return true
	}
	return false
}

func doctorFixJobActive(job *batchv1.Job) bool {
	for _, condition := range job.Status.Conditions {
		if (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) && condition.Status == corev1.ConditionTrue {
			return false
		}
	}
	return true
}

func (r *ClawResourceReconciler) hasActiveDoctorFix(ctx context.Context, instance *clawv1alpha1.Claw) (bool, error) {
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(instance.Namespace), client.MatchingLabels{InstanceLabelKey: sanitizeLabelValue(instance.Name)}); err != nil {
		return false, fmt.Errorf("list doctor Jobs: %w", err)
	}
	for i := range jobs.Items {
		if jobs.Items[i].Annotations[doctorFixImageAnnotation] != "" && doctorFixJobActive(&jobs.Items[i]) {
			return true, nil
		}
	}
	return false, nil
}

func doctorFixPending(instance *clawv1alpha1.Claw) bool {
	return doctorFixRequested(instance) && !doctorFixComplete(instance)
}

func doctorFixJobName(instanceName, image string) string {
	suffix := fmt.Sprintf("%x", sha256.Sum256([]byte(image)))[:8]
	prefix := strings.TrimSuffix(instanceName, "-")
	if max := 63 - len("-doctor-") - len(suffix); len(prefix) > max {
		prefix = prefix[:max]
	}
	return prefix + "-doctor-" + suffix
}

func pauseGatewayForDoctorFix(objects []*unstructured.Unstructured, instance *clawv1alpha1.Claw) error {
	for _, obj := range objects {
		if obj.GetKind() != DeploymentKind || obj.GetName() != getClawDeploymentName(instance.Name) {
			continue
		}
		if err := unstructured.SetNestedField(obj.Object, int64(0), "spec", "replicas"); err != nil {
			return fmt.Errorf("set gateway replicas to zero for doctor migration: %w", err)
		}
		return nil
	}
	return fmt.Errorf("gateway deployment %q not found", getClawDeploymentName(instance.Name))
}

func doctorFixJob(instance *clawv1alpha1.Claw) *batchv1.Job {
	jobName := doctorFixJobName(instance.Name, effectiveOpenClawImage(instance))
	configMode, configManagement := clawConfigModeAndManagement(instance)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				InstanceLabelKey: sanitizeLabelValue(instance.Name),
			},
			Annotations: map[string]string{doctorFixImageAnnotation: effectiveOpenClawImage(instance)},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          ptr.To(int32(3)),
			ActiveDeadlineSeconds: ptr.To(int64(600)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{InstanceLabelKey: sanitizeLabelValue(instance.Name)}},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					InitContainers: []corev1.Container{{
						Name:         "prepare-home",
						Image:        effectiveOpenClawImage(instance),
						Command:      []string{"sh", "-c", "mkdir -p -m 0700 /mnt/pvc/home"},
						VolumeMounts: []corev1.VolumeMount{{Name: "claw-home", MountPath: "/mnt/pvc"}},
					}},
					Containers: []corev1.Container{{
						Name:  "doctor",
						Image: effectiveOpenClawImage(instance),
						// Doctor must use the gateway's state path: persisted config can
						// reference it directly, and migrations may inspect the agent DB.
						Command: []string{"sh", "-c", "node /config/merge.js && exec node /app/dist/index.js doctor --fix --non-interactive"},
						Env: []corev1.EnvVar{
							{Name: "HOME", Value: "/home/node"},
							{Name: "OPENCLAW_HOME", Value: "/home/node"},
							{Name: "OPENCLAW_STATE_DIR", Value: "/home/node/.openclaw"},
							{Name: "OPENCLAW_CONFIG_PATH", Value: "/home/node/.openclaw/openclaw.json"},
							{Name: ClawConfigModeEnvVar, Value: configMode},
							{Name: ClawConfigManagementEnvVar, Value: configManagement},
							{Name: doctorFixMergeEnv, Value: "1"},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "claw-home", MountPath: "/home/node", SubPath: "home"},
							{Name: "config", MountPath: "/config", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "claw-home",
						VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: getPVCName(instance.Name),
						}},
					}, {
						Name: "config",
						VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: getConfigMapName(instance.Name)},
						}},
					}},
				},
			},
		},
	}
}

func (r *ClawResourceReconciler) reconcileDoctorFix(ctx context.Context, instance *clawv1alpha1.Claw) (ctrl.Result, error) {
	setCondition(instance, clawv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
		"Migrating", "Gateway paused for a user-approved doctor migration")
	if err := r.Status().Update(ctx, instance); err != nil {
		return ctrl.Result{}, fmt.Errorf("record migrating status: %w", err)
	}

	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: instance.Namespace, Name: getClawDeploymentName(instance.Name)}, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get paused gateway deployment: %w", err)
	}
	if deployment.Status.Replicas != 0 || deployment.Status.ReadyReplicas != 0 {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	active, err := r.hasActiveDoctorFix(ctx, instance)
	if err != nil {
		return ctrl.Result{}, err
	}
	if active {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	wantImage := effectiveOpenClawImage(instance)
	wantJobName := doctorFixJobName(instance.Name, wantImage)
	job := &batchv1.Job{}
	err = r.Get(ctx, types.NamespacedName{Namespace: instance.Namespace, Name: wantJobName}, job)
	if apierrors.IsNotFound(err) {
		job = doctorFixJob(instance)
		if err := controllerutil.SetControllerReference(instance, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("set doctor Job owner reference: %w", err)
		}
		if err := r.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("create doctor Job: %w", err)
		}
		setCondition(instance, clawv1alpha1.ConditionTypeMigrationComplete, metav1.ConditionFalse,
			clawv1alpha1.ConditionReasonMigrationPending, "Waiting for user-approved doctor migration Job")
		if err := r.Status().Update(ctx, instance); err != nil {
			return ctrl.Result{}, fmt.Errorf("record doctor migration pending: %w", err)
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get doctor Job: %w", err)
	}
	if job.Status.Succeeded > 0 {
		instance.Status.Migration = &clawv1alpha1.MigrationStatus{DoctorFixImage: wantImage, DoctorFixJob: job.Name}
		setCondition(instance, clawv1alpha1.ConditionTypeMigrationComplete, metav1.ConditionTrue,
			clawv1alpha1.ConditionReasonMigrationComplete, "User-approved doctor migration completed")
		if err := r.Status().Update(ctx, instance); err != nil {
			return ctrl.Result{}, fmt.Errorf("record doctor migration success: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if !doctorFixJobActive(job) && job.Status.Failed > 0 {
		setCondition(instance, clawv1alpha1.ConditionTypeMigrationComplete, metav1.ConditionFalse,
			clawv1alpha1.ConditionReasonMigrationFailed, "Doctor migration Job failed; inspect its logs and update the target image before retrying")
		if err := r.Status().Update(ctx, instance); err != nil {
			return ctrl.Result{}, fmt.Errorf("record doctor migration failure: %w", err)
		}
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}
