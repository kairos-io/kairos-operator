package controller

import (
	"context"
	"os"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	kairosNodeLabelerDaemonSetName = "kairos-node-labeler"
	defaultSyncIntervalSeconds     = 60
)

// NodeLabelerDaemonSetReconciler ensures a DaemonSet runs the node-labeler in
// loop mode on every already-managed Kairos node, keeping labels and annotations
// in sync across upgrades.
//
// Labeling lifecycle:
//  1. NodeLabelerReconciler watches Node objects and creates a one-shot Job for
//     each new node. That Job runs the node-labeler binary without --every,
//     which sets kairos.io/managed=true (plus all other metadata labels).
//  2. This DaemonSet — which selects on kairos.io/managed=true — then schedules
//     a pod on that node and re-syncs labels on the configured interval.
//
// There is intentionally no chicken-and-egg problem: the DaemonSet only needs
// to reach nodes that are already labeled, so it is correct and by design that
// it won't run on a unlabeled node. The purpose of the Job is to identify Kairos
// nodes once. The purpose of the DaemonSet is to keep a Pod running on each Kairos
// node (and only Kairos nodes) to keep the labels and annotations in sync.
// This way, we avoid running a "permanent" Pod on non-Kairos Nodes.
type NodeLabelerDaemonSetReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete

func (r *NodeLabelerDaemonSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	namespace := getOperatorNamespace()
	if req.Name != kairosNodeLabelerDaemonSetName || req.Namespace != namespace {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.ensureDaemonSet(ctx, namespace)
}

// ensureDaemonSetOnStartup is called from SetupWithManager before the cache is
// started, so it must not read through the cache. On first install it creates
// the DaemonSet; on subsequent operator startups (e.g. upgrades) it patches the
// spec so that a new NODE_LABELER_IMAGE is rolled out without manual intervention.
func (r *NodeLabelerDaemonSetReconciler) ensureDaemonSetOnStartup(ctx context.Context, namespace string) error {
	desired := r.buildDaemonSet(namespace)
	if err := r.Create(ctx, desired); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		existing := &appsv1.DaemonSet{}
		if err := r.Get(ctx, types.NamespacedName{Name: kairosNodeLabelerDaemonSetName, Namespace: namespace}, existing); err != nil {
			return err
		}
		patch := client.MergeFrom(existing.DeepCopy())
		existing.Spec = desired.Spec
		return r.Patch(ctx, existing, patch)
	}
	return nil
}

// ensureDaemonSet is safe to call from Reconcile (cache is running). It checks
// first and only creates when the DaemonSet is genuinely absent.
func (r *NodeLabelerDaemonSetReconciler) ensureDaemonSet(ctx context.Context, namespace string) error {
	log := logf.FromContext(ctx)
	existing := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{Name: kairosNodeLabelerDaemonSetName, Namespace: namespace}, existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	if err := r.Create(ctx, r.buildDaemonSet(namespace)); err != nil {
		log.Error(err, "Failed to create node-labeler DaemonSet")
		return err
	}
	log.Info("Created node-labeler DaemonSet")
	return nil
}

func (r *NodeLabelerDaemonSetReconciler) buildDaemonSet(namespace string) *appsv1.DaemonSet {
	nodeLabelerImage := os.Getenv("NODE_LABELER_IMAGE")
	if nodeLabelerImage == "" {
		nodeLabelerImage = "quay.io/kairos/operator-node-labeler:v0.0.1"
	}

	podLabels := map[string]string{
		"app":  "kairos-node-labeler",
		"mode": "daemon",
	}

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kairosNodeLabelerDaemonSetName,
			Namespace: namespace,
			Labels: map[string]string{
				"app": "kairos-node-labeler",
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: podLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: nodeLabelerServiceAccount,
					NodeSelector: map[string]string{
						"kairos.io/managed": "true",
					},
					Tolerations: []corev1.Toleration{
						{Operator: corev1.TolerationOpExists},
					},
					Containers: []corev1.Container{
						{
							Name:            "node-labeler",
							Image:           nodeLabelerImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Args:            []string{"--every", strconv.Itoa(defaultSyncIntervalSeconds)},
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot: asBool(true),
								RunAsUser:    &[]int64{1000}[0],
							},
							Env: []corev1.EnvVar{
								{
									Name: "NODE_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											APIVersion: "v1",
											FieldPath:  "spec.nodeName",
										},
									},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "host-etc",
									MountPath: "/host/etc",
									ReadOnly:  true,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "host-etc",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/etc",
								},
							},
						},
					},
				},
			},
		},
	}
}

func (r *NodeLabelerDaemonSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	namespace := getOperatorNamespace()
	if err := r.ensureDaemonSetOnStartup(context.Background(), namespace); err != nil {
		log := logf.Log.WithName("setup")
		log.Error(err, "Failed to ensure node-labeler DaemonSet")
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.DaemonSet{}).
		Complete(r)
}
