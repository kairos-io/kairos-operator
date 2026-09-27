package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

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
	// Records which Node object incarnation a Job was created for
	labelKeyNodeUID = "kairos.io/node-uid"
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
// The Job name and the node label are both derived from the host name, which a
// re-provisioned machine keeps, so the caller must still decide which Node
// object each Job belongs to.
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

// jobLabelsNode reports whether job was created for this Node object, and not
// for an earlier one that happened to carry the same host name. A Job with no
// recorded UID predates this check, so it cannot be shown to belong to node.
func jobLabelsNode(job *batchv1.Job, node *corev1.Node) bool {
	return job.Labels[labelKeyNodeUID] == string(node.UID)
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
				labelKeyNodeUID: string(node.UID),
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
		if jobLabelsNode(&existing[i], node) {
			// Job already exists for this node
			return ctrl.Result{}, nil
		}
	}

	// Every Job under this host name was created for a different Node object,
	// so the machine was re-provisioned and comes back unlabeled. Drop them, so
	// the deterministic Job name is free for the replacement.
	for i := range existing {
		outdated := &existing[i]
		propagation := metav1.DeletePropagationBackground
		// The UID precondition keeps a stale cache read from deleting the
		// replacement Job, which carries the same name.
		err := r.Delete(ctx, outdated,
			client.PropagationPolicy(propagation),
			client.Preconditions{UID: &outdated.UID},
		)
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			log.Error(err, "Failed to delete outdated node-labeler job", "job", outdated.Name)
			return ctrl.Result{}, err
		}
		log.Info("Deleted node-labeler job of a previous node",
			"node", node.Name, "job", outdated.Name, "nodeUID", outdated.Labels[labelKeyNodeUID])
	}

	// Create the node-labeler job
	job := r.createNodeLabelerJob(node, namespace)

	if err := r.Create(ctx, job); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// The name is still held by the Job we just deleted, or another
			// reconcile won the race. Come back for it.
			return ctrl.Result{RequeueAfter: time.Second}, nil
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
		Complete(r)
}
