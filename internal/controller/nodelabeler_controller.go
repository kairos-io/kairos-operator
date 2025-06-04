package controller

import (
	"context"
	"fmt"
	"os"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kairos-io/kairos-operator/internal/utils"
)

const (
	nodeLabelerServiceAccount = "kairos-node-labeler"
)

// NodeLabelerReconciler reconciles nodes to ensure they are labeled
type NodeLabelerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete

func (r *NodeLabelerReconciler) getOperatorNamespace() string {
	// Get namespace from environment variable
	namespace := os.Getenv("CONTROLLER_POD_NAMESPACE")
	if namespace == "" {
		// Fallback to "system" if not set
		return "system"
	}
	return namespace
}

func (r *NodeLabelerReconciler) jobExists(ctx context.Context, namespace string, nodeName string) (bool, error) {
	jobList := &batchv1.JobList{}
	if err := r.List(ctx, jobList,
		client.InNamespace(namespace),
		client.MatchingLabels(map[string]string{
			"node": nodeName,
			"app":  "kairos-node-labeler",
		}),
	); err != nil {
		return false, err
	}

	return len(jobList.Items) > 0, nil
}

func (r *NodeLabelerReconciler) createNodeLabelerJob(node *corev1.Node, namespace string) *batchv1.Job {
	// Get the node labeler image from environment variable
	nodeLabelerImage := os.Getenv("NODE_LABELER_IMAGE")
	if nodeLabelerImage == "" {
		// Fallback to a default value if not set
		nodeLabelerImage = "quay.io/kairos/operator-node-labeler:v0.0.1"
	}

	fullName := fmt.Sprintf("kairos-node-labeler-%s", node.Name)
	jobName := utils.TruncateNameWithHash(fullName, utils.KubernetesNameLengthLimit)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			Labels: map[string]string{
				"app":  "kairos-node-labeler",
				"node": node.Name,
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "kairos-node-labeler",
					},
				},
				Spec: corev1.PodSpec{
					NodeName:           node.Name,
					ServiceAccountName: nodeLabelerServiceAccount,
					Containers: []corev1.Container{
						{
							Name:            "node-labeler",
							Image:           nodeLabelerImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot: &[]bool{true}[0],
								RunAsUser:    &[]int64{1000}[0],
							},
							Env: []corev1.EnvVar{
								{
									Name: "NODE_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "spec.nodeName",
										},
									},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "kairos-release",
									MountPath: "/etc/kairos-release",
									ReadOnly:  true,
								},
								{
									Name:      "os-release",
									MountPath: "/etc/os-release",
									ReadOnly:  true,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "kairos-release",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/etc/kairos-release",
								},
							},
						},
						{
							Name: "os-release",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/etc/os-release",
								},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			},
		},
	}
}

func (r *NodeLabelerReconciler) ensureServiceAccount(ctx context.Context, namespace string) error {
	// Create ServiceAccount
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeLabelerServiceAccount,
			Namespace: namespace,
		},
	}
	if err := r.Create(ctx, sa); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create service account: %w", err)
		}
	}

	// Create ClusterRole
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeLabelerServiceAccount,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get", "list", "watch", "update", "patch"},
			},
		},
	}
	if err := r.Create(ctx, clusterRole); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create cluster role: %w", err)
		}
	}

	// Create ClusterRoleBinding
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeLabelerServiceAccount,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      nodeLabelerServiceAccount,
				Namespace: namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     nodeLabelerServiceAccount,
		},
	}
	if err := r.Create(ctx, clusterRoleBinding); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create cluster role binding: %w", err)
		}
	}

	return nil
}

func (r *NodeLabelerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Get the node
	node := &corev1.Node{}
	if err := r.Get(ctx, req.NamespacedName, node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Get the operator namespace
	namespace := r.getOperatorNamespace()

	// Check if a labeler job already exists for this node
	exists, err := r.jobExists(ctx, namespace, node.Name)
	if err != nil {
		log.Error(err, "Failed to check for existing jobs")
		return ctrl.Result{}, err
	}

	if exists {
		// Job already exists for this node
		return ctrl.Result{}, nil
	}

	// Create the node-labeler job
	job := r.createNodeLabelerJob(node, namespace)

	if err := r.Create(ctx, job); err != nil {
		log.Error(err, "Failed to create node-labeler job")
		return ctrl.Result{}, err
	}

	log.Info("Created node-labeler job for node", "node", node.Name)
	return ctrl.Result{}, nil
}

func (r *NodeLabelerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Ensure RBAC resources are created when the controller starts
	namespace := r.getOperatorNamespace()
	if err := r.ensureServiceAccount(context.Background(), namespace); err != nil {
		log := logf.Log.WithName("setup")
		log.Error(err, "Failed to ensure service account and RBAC")
		os.Exit(1)

	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Complete(r)
}
