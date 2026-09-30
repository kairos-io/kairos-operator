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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kairos-io/kairos-operator/internal/utils"
)

const (
	nodeLabelerServiceAccount = "kairos-node-labeler"
	// Labels used on node-labeler Jobs/DaemonSet Pods
	labelKeyApp              = "app"
	labelKeyJobNode          = "node"
	labelValueNodeLabelerApp = "kairos-node-labeler"
	// Env/fieldRef wiring for the node-labeler container
	envNodeName       = "NODE_NAME"
	fieldPathNodeName = "spec.nodeName"
	// Host /etc bind mount used by the node-labeler to read kairos-release
	hostEtcVolumeName = "host-etc"
	hostEtcMountPath  = "/host/etc"
	// Common label keys
	labelKeyKairosManaged = "kairos.io/managed"
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

func getOperatorNamespace() string {
	namespace := os.Getenv("CONTROLLER_POD_NAMESPACE")
	if namespace == "" {
		return "system"
	}
	return namespace
}

// jobsForNode returns the node-labeler Jobs that currently exist for nodeName.
func (r *NodeLabelerReconciler) jobsForNode(ctx context.Context, namespace string, nodeName string) ([]batchv1.Job, error) {
	jobList := &batchv1.JobList{}
	if err := r.List(ctx, jobList,
		client.InNamespace(namespace),
		client.MatchingLabels(map[string]string{
			labelKeyJobNode: nodeName,
			labelKeyApp:     labelValueNodeLabelerApp,
		}),
	); err != nil {
		return nil, err
	}

	return jobList.Items, nil
}

// jobFailed reports whether the Job gave up. A Job that exhausted its backoff
// limit never runs another Pod, so the node it was meant to label stays
// unlabeled until the Job is replaced.
func jobFailed(job *batchv1.Job) bool {
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}

// findNodeForLabelerJob maps a node-labeler Job back to the node it labels, so
// a Job that fails wakes the reconciler instead of waiting for a Node event.
func (r *NodeLabelerReconciler) findNodeForLabelerJob(_ context.Context, obj client.Object) []reconcile.Request {
	nodeName := obj.GetLabels()[labelKeyJobNode]
	if nodeName == "" {
		return nil
	}

	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nodeName}}}
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
				labelKeyApp:     labelValueNodeLabelerApp,
				labelKeyJobNode: node.Name,
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						labelKeyApp: labelValueNodeLabelerApp,
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
								RunAsNonRoot: asBool(true),
								RunAsUser:    &[]int64{1000}[0],
							},
							Env: []corev1.EnvVar{
								{
									Name: envNodeName,
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: fieldPathNodeName,
										},
									},
								},
								{
									Name:  "HOST_ETC_PATH",
									Value: hostEtcMountPath,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      hostEtcVolumeName,
									MountPath: hostEtcMountPath,
									ReadOnly:  true,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: hostEtcVolumeName,
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/etc",
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
	namespace := getOperatorNamespace()

	// Check if a labeler job already exists for this node
	existing, err := r.jobsForNode(ctx, namespace, node.Name)
	if err != nil {
		log.Error(err, "Failed to check for existing jobs")
		return ctrl.Result{}, err
	}

	for i := range existing {
		if !jobFailed(&existing[i]) {
			// A Job for this node is still pending, running or already done
			return ctrl.Result{}, nil
		}
	}

	// Every Job we found gave up. Drop them, so the deterministic name is free
	// and the node gets another chance to be labeled.
	for i := range existing {
		failed := &existing[i]
		propagation := metav1.DeletePropagationBackground
		// The UID precondition keeps a stale cache read from deleting the
		// replacement Job, which carries the same name.
		err := r.Delete(ctx, failed,
			client.PropagationPolicy(propagation),
			client.Preconditions{UID: &failed.UID},
		)
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			log.Error(err, "Failed to delete failed node-labeler job", "job", failed.Name)
			return ctrl.Result{}, err
		}
		log.Info("Deleted failed node-labeler job", "node", node.Name, "job", failed.Name)
	}

	// Create the node-labeler job
	job := r.createNodeLabelerJob(node, namespace)

	if err := r.Create(ctx, job); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// The name is derived from the node, so another reconcile won the race.
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to create node-labeler job")
		return ctrl.Result{}, err
	}

	log.Info("Created node-labeler job for node", "node", node.Name)
	return ctrl.Result{}, nil
}

func (r *NodeLabelerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	setupLog := logf.Log.WithName("setup")

	// Ensure RBAC resources are created when the controller starts
	namespace := getOperatorNamespace()
	if err := r.ensureServiceAccount(context.Background(), namespace); err != nil {
		setupLog.Error(err, "Failed to ensure service account and RBAC")
		os.Exit(1)
	}

	// Define selector for nodes that should be ignored
	nodeSelector, err := labels.Parse("kairos.io/managed notin (false)")
	if err != nil {
		setupLog.Error(err, "Failed to parse label selector for nodes")
		os.Exit(1)
	}

	log := logf.FromContext(context.TODO())
	return ctrl.NewControllerManagedBy(mgr).
		For(
			&corev1.Node{},
			// Filter out ignored nodes
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				node := obj.(*corev1.Node)

				matches := nodeSelector.Matches(labels.Set(node.ObjectMeta.Labels))
				if !matches {
					log.V(1).Info("Ignoring explicitly labeled node", "node", node.ObjectMeta.Name)
				}

				return matches
			})),
		).
		Watches(
			&batchv1.Job{},
			handler.EnqueueRequestsFromMapFunc(r.findNodeForLabelerJob),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetLabels()[labelKeyApp] == labelValueNodeLabelerApp
			})),
		).
		Complete(r)
}
