package controller

import (
	"context"
	"fmt"
	"os"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// NodeLabelerReconciler reconciles nodes to ensure they are labeled
type NodeLabelerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch

func (r *NodeLabelerReconciler) getOperatorNamespace() string {
	// Get namespace from environment variable
	namespace := os.Getenv("OPERATOR_NAMESPACE")
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
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("kairos-node-labeler-%s", node.Name),
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
					ServiceAccountName: "kairos-node-labeler",
					Containers: []corev1.Container{
						{
							Name:            "node-labeler",
							Image:           "kairos/node-labeler:latest",
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
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Complete(r)
}
