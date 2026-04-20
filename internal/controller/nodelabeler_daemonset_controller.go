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
// loop mode on every Kairos node, keeping labels in sync after upgrades.
type NodeLabelerDaemonSetReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete

func (r *NodeLabelerDaemonSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	namespace := getOperatorNamespace()
	if req.Name != kairosNodeLabelerDaemonSetName || req.Namespace != namespace {
		return ctrl.Result{}, nil
	}

	existing := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{Name: kairosNodeLabelerDaemonSetName, Namespace: namespace}, existing)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		ds := r.buildDaemonSet(namespace)
		if err := r.Create(ctx, ds); err != nil {
			log.Error(err, "Failed to create node-labeler DaemonSet")
			return ctrl.Result{}, err
		}
		log.Info("Created node-labeler DaemonSet")
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
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
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.DaemonSet{}).
		Complete(r)
}
