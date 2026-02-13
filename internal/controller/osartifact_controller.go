/*
Copyright 2022.

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

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	FinalizerName                   = "build.kairos.io/osbuilder-finalizer"
	CompatibleAurorabootVersion     = "v0.19.3"
	artifactLabel                   = "build.kairos.io/artifact"
	artifactExporterIndexAnnotation = "build.kairos.io/export-index"
)

// OSArtifactReconciler reconciles a OSArtifact object
type OSArtifactReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	ServingImage string
	ToolImage    string
	CopierImage  string
}

// +kubebuilder:rbac:groups=build.kairos.io,resources=osartifacts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=build.kairos.io,resources=osartifacts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=build.kairos.io,resources=osartifacts/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;create;delete;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete

func (r *OSArtifactReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var artifact buildv1alpha2.OSArtifact
	if err := r.Get(ctx, req.NamespacedName, &artifact); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{Requeue: true}, err
	}

	if artifact.DeletionTimestamp != nil {
		controllerutil.RemoveFinalizer(&artifact, FinalizerName)
		return ctrl.Result{}, r.Update(ctx, &artifact)
	}

	if !controllerutil.ContainsFinalizer(&artifact, FinalizerName) {
		controllerutil.AddFinalizer(&artifact, FinalizerName)
		if err := r.Update(ctx, &artifact); err != nil {
			return ctrl.Result{Requeue: true}, err
		}
	}

	logger.Info(fmt.Sprintf("Reconciling %s/%s", artifact.Namespace, artifact.Name))

	switch artifact.Status.Phase {
	case buildv1alpha2.Exporting:
		return r.checkExport(ctx, &artifact)
	case buildv1alpha2.Ready, buildv1alpha2.Error:
		return ctrl.Result{}, nil
	default:
		return r.checkBuild(ctx, &artifact)
	}
}

// CreateConfigMap generates a configmap required for building a custom image
func (r *OSArtifactReconciler) CreateConfigMap(ctx context.Context, artifact *buildv1alpha2.OSArtifact) error {
	cm := r.genConfigMap(artifact)
	if cm.Labels == nil {
		cm.Labels = map[string]string{}
	}
	cm.Labels[artifactLabel] = artifact.Name
	if err := controllerutil.SetOwnerReference(artifact, cm, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, cm); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	return nil
}

func (r *OSArtifactReconciler) createPVC(ctx context.Context,
	artifact *buildv1alpha2.OSArtifact) (*corev1.PersistentVolumeClaim, error) {
	pvc := r.newArtifactPVC(artifact)
	if pvc.Labels == nil {
		pvc.Labels = map[string]string{}
	}
	pvc.Labels[artifactLabel] = artifact.Name
	if err := controllerutil.SetOwnerReference(artifact, pvc, r.Scheme); err != nil {
		return pvc, err
	}
	if err := r.Create(ctx, pvc); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// PVC already exists, fetch and return it
			existingPVC := &corev1.PersistentVolumeClaim{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(pvc), existingPVC); err != nil {
				return pvc, err
			}
			return existingPVC, nil
		}
		return pvc, err
	}

	return pvc, nil
}

func (r *OSArtifactReconciler) createBuilderPod(ctx context.Context, artifact *buildv1alpha2.OSArtifact,
	pvc *corev1.PersistentVolumeClaim) (*corev1.Pod, error) {
	pod := r.newBuilderPod(ctx, pvc.Name, artifact)
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[artifactLabel] = artifact.Name
	if err := controllerutil.SetOwnerReference(artifact, pod, r.Scheme); err != nil {
		return pod, err
	}

	if err := r.Create(ctx, pod); err != nil {
		return pod, err
	}

	return pod, nil
}

// checkSecretOwnership verifies that a Secret with the given name either does not exist
// or is owned by the specified OSArtifact. This prevents name collision attacks where
// a malicious user could create an OSArtifact to overwrite unrelated Secrets.
// Returns an error if the Secret exists but is not owned by the artifact.
func (r *OSArtifactReconciler) checkSecretOwnership(ctx context.Context, secretName, namespace string, owner *buildv1alpha2.OSArtifact) error {
	existingSecret := &corev1.Secret{}
	err := r.Get(ctx, client.ObjectKey{Name: secretName, Namespace: namespace}, existingSecret)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to check for existing Secret: %w", err)
	}

	// If Secret exists but is not owned by this OSArtifact, refuse to proceed
	if err == nil && !metav1.IsControlledBy(existingSecret, owner) {
		return fmt.Errorf("secret %q already exists and is not owned by this OSArtifact (uid=%s); refusing to update to prevent collision", secretName, owner.UID)
	}

	return nil
}

// renderDockerfile fetches the Dockerfile from the referenced Secret, renders
// it through the Go template engine, and creates a new Secret with the rendered
// content. The in-memory artifact is updated to point to the rendered Secret so
// that downstream pod creation uses the rendered version.
// If BaseImageDockerfile is not set, this is a no-op.
func (r *OSArtifactReconciler) renderDockerfile(ctx context.Context, artifact *buildv1alpha2.OSArtifact) error {
	if artifact.Spec.BaseImageDockerfile == nil {
		return nil
	}

	logger := log.FromContext(ctx)

	// Fetch the original Secret containing the Dockerfile
	secret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      artifact.Spec.BaseImageDockerfile.Name,
		Namespace: artifact.Namespace,
	}, secret); err != nil {
		return fmt.Errorf("failed to fetch Dockerfile secret %q: %w", artifact.Spec.BaseImageDockerfile.Name, err)
	}

	// Determine which key holds the Dockerfile content
	key := artifact.Spec.BaseImageDockerfile.Key
	if key == "" {
		key = "Dockerfile"
	}

	dockerfileContent, ok := secret.Data[key]
	if !ok {
		return fmt.Errorf("key %q not found in secret %q", key, secret.Name)
	}

	// Collect template values. Secret values are loaded first, then inline
	// values are merged on top so that inline takes precedence on conflicts.
	values := map[string]string{}

	if artifact.Spec.DockerfileTemplateValuesFrom != nil {
		valuesSecret := &corev1.Secret{}
		if err := r.Get(ctx, client.ObjectKey{
			Name:      artifact.Spec.DockerfileTemplateValuesFrom.Name,
			Namespace: artifact.Namespace,
		}, valuesSecret); err != nil {
			return fmt.Errorf("failed to fetch template values Secret %q: %w",
				artifact.Spec.DockerfileTemplateValuesFrom.Name, err)
		}
		for k, v := range valuesSecret.Data {
			values[k] = string(v)
		}
	}

	for k, v := range artifact.Spec.DockerfileTemplateValues {
		values[k] = v
	}

	rendered, err := renderDockerfileTemplate(string(dockerfileContent), values)
	if err != nil {
		return fmt.Errorf("failed to render Dockerfile template: %w", err)
	}

	logger.Info("Rendered Dockerfile template", "artifact", artifact.Name)

	// Create or update the Secret with the rendered Dockerfile.
	// Using CreateOrUpdate ensures that if the user changes their Dockerfile
	// or template values, the rendered Secret is refreshed on re-reconciliation.
	renderedSecretName := artifact.Name + "-rendered-dockerfile"
	renderedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      renderedSecretName,
			Namespace: artifact.Namespace,
		},
	}

	// Verify ownership to prevent name collision attacks
	if err := r.checkSecretOwnership(ctx, renderedSecretName, artifact.Namespace, artifact); err != nil {
		return err
	}

	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, renderedSecret, func() error {
		// Merge labels instead of overwriting to preserve labels set by other tooling
		if renderedSecret.Labels == nil {
			renderedSecret.Labels = make(map[string]string)
		}
		renderedSecret.Labels[artifactLabel] = artifact.Name
		// The rendered Dockerfile is always stored under the "Dockerfile" key
		// because kaniko expects the file to be named "Dockerfile" in the
		// mounted volume (--dockerfile dockerfile/Dockerfile).
		renderedSecret.StringData = map[string]string{
			"Dockerfile": rendered,
		}
		return controllerutil.SetControllerReference(artifact, renderedSecret, r.Scheme)
	}); err != nil {
		return fmt.Errorf("failed to create or update rendered Dockerfile secret: %w", err)
	}

	// Update the in-memory reference so the pod mounts the rendered Secret.
	// This does NOT persist to the API server — it only affects the current
	// reconciliation pass. Key is set to "Dockerfile" because that's what
	// kaniko expects in the mounted volume.
	artifact.Spec.BaseImageDockerfile.Name = renderedSecretName
	artifact.Spec.BaseImageDockerfile.Key = "Dockerfile"

	return nil
}

func (r *OSArtifactReconciler) startBuild(ctx context.Context,
	artifact *buildv1alpha2.OSArtifact) (ctrl.Result, error) {
	// Render Dockerfile template if applicable
	if err := r.renderDockerfile(ctx, artifact); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	err := r.CreateConfigMap(ctx, artifact)
	if err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	pvc, err := r.createPVC(ctx, artifact)
	if err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	_, err = r.createBuilderPod(ctx, artifact, pvc)
	if err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	artifact.Status.Phase = buildv1alpha2.Building
	if err := r.Status().Update(ctx, artifact); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	return ctrl.Result{}, nil
}

func (r *OSArtifactReconciler) checkBuild(ctx context.Context,
	artifact *buildv1alpha2.OSArtifact) (ctrl.Result, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			artifactLabel: artifact.Name,
		}),
	}); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	for _, pod := range pods.Items {
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			artifact.Status.Phase = buildv1alpha2.Exporting
			return ctrl.Result{Requeue: true}, r.Status().Update(ctx, artifact)
		case corev1.PodFailed:
			artifact.Status.Phase = buildv1alpha2.Error
			return ctrl.Result{Requeue: true}, r.Status().Update(ctx, artifact)
		case corev1.PodPending, corev1.PodRunning:
			return ctrl.Result{}, nil
		}
	}

	return r.startBuild(ctx, artifact)
}

func (r *OSArtifactReconciler) checkExport(ctx context.Context,
	artifact *buildv1alpha2.OSArtifact) (ctrl.Result, error) {
	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			artifactLabel: artifact.Name,
		}),
	}); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	indexedJobs := make(map[string]*batchv1.Job, len(artifact.Spec.Exporters))
	for _, job := range jobs.Items {
		if job.GetAnnotations() != nil {
			if idx, ok := job.GetAnnotations()[artifactExporterIndexAnnotation]; ok {
				indexedJobs[idx] = &job
			}
		}
	}

	var pvcs corev1.PersistentVolumeClaimList
	var pvc *corev1.PersistentVolumeClaim
	if err := r.List(ctx, &pvcs, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{artifactLabel: artifact.Name}),
	}); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	for _, item := range pvcs.Items {
		pvc = &item
		break
	}

	if pvc == nil {
		log.FromContext(ctx).Error(nil, "failed to locate pvc for artifact, this should not happen")
		return ctrl.Result{}, fmt.Errorf("failed to locate artifact pvc")
	}

	var succeeded int
	for i := range artifact.Spec.Exporters {
		idx := fmt.Sprintf("%d", i)

		job := indexedJobs[idx]
		if job == nil {
			job = &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-export-%s", artifact.Name, idx),
					Namespace: artifact.Namespace,
					Annotations: map[string]string{
						artifactExporterIndexAnnotation: idx,
					},
					Labels: map[string]string{
						artifactLabel: artifact.Name,
					},
				},
				Spec: artifact.Spec.Exporters[i],
			}

			// Clean up template metadata to remove server-managed fields that shouldn't be in JobSpec.Template
			job.Spec.Template.ObjectMeta = metav1.ObjectMeta{
				Labels:      job.Spec.Template.Labels,
				Annotations: job.Spec.Template.Annotations,
			}

			job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: "artifacts",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvc.Name,
						ReadOnly:  true,
					},
				},
			})

			if err := controllerutil.SetOwnerReference(artifact, job, r.Scheme); err != nil {
				return ctrl.Result{Requeue: true}, err
			}

			if err := r.Create(ctx, job); err != nil {
				return ctrl.Result{Requeue: true}, err
			}

		} else if job.Spec.Completions == nil || *job.Spec.Completions == 1 {
			if job.Status.Succeeded > 0 {
				succeeded++
			}
		} else if *job.Spec.BackoffLimit <= job.Status.Failed {
			artifact.Status.Phase = buildv1alpha2.Error
			if err := r.Status().Update(ctx, artifact); err != nil {
				return ctrl.Result{Requeue: true}, err
			}
			break
		}
	}

	if succeeded == len(artifact.Spec.Exporters) {
		artifact.Status.Phase = buildv1alpha2.Ready
		if err := r.Status().Update(ctx, artifact); err != nil {
			return ctrl.Result{Requeue: true}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *OSArtifactReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&buildv1alpha2.OSArtifact{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.findOwningArtifact),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&batchv1.Job{},
			handler.EnqueueRequestsFromMapFunc(r.findOwningArtifact),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}

func (r *OSArtifactReconciler) findOwningArtifact(_ context.Context, obj client.Object) []reconcile.Request {
	if obj.GetLabels() == nil {
		return nil
	}

	if artifactName, ok := obj.GetLabels()[artifactLabel]; ok {
		return []reconcile.Request{
			{
				NamespacedName: types.NamespacedName{
					Name:      artifactName,
					Namespace: obj.GetNamespace(),
				},
			},
		}
	}

	return nil
}
