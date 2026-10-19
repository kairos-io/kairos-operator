package controller

import (
	"context"
	"fmt"
	"os"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	// Common label keys
	labelKeyKairosManaged = "kairos.io/managed"
	// clusterRoleKind is the RoleRef kind of a cluster-scoped role. rbacv1
	// exports the Subject kinds but no RoleRef one, so the string has to be
	// written somewhere; writing it once keeps the binding this operator
	// creates and the ones its tests assert on from drifting apart.
	clusterRoleKind = "ClusterRole"
)

// NodeLabelerReconciler reconciles nodes to ensure they are labeled
type NodeLabelerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIReader reads straight from the API server, bypassing the manager's
	// cache. The ensure* helpers below read a ClusterRole and a
	// ClusterRoleBinding by name; a cached Get would make the operator watch
	// every ClusterRole and ClusterRoleBinding in the cluster to read two.
	// SetupWithManager fills it in from the manager when the caller left it nil.
	APIReader client.Reader
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

func (r *NodeLabelerReconciler) jobExists(ctx context.Context, namespace string, nodeName string) (bool, error) {
	jobList := &batchv1.JobList{}
	if err := r.List(ctx, jobList,
		client.InNamespace(namespace),
		client.MatchingLabels(map[string]string{
			labelKeyJobNode: nodeName,
			labelKeyApp:     labelValueNodeLabelerApp,
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

// nodeLabelerClusterRoleRules is the grant the node-labeler needs: it reads
// its own Node and writes the kairos.io/* labels onto it. This is the single
// declaration of those rules, so ensureClusterRole can compare what the
// cluster holds against what this operator version wants.
func nodeLabelerClusterRoleRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{
			APIGroups: []string{""},
			Resources: []string{"nodes"},
			Verbs:     []string{verbGet, verbList, "watch", "update", "patch"},
		},
	}
}

// nodeLabelerSubject is the ServiceAccount the node-labeler Job and DaemonSet
// both run as. It lives in the operator's own namespace, which is why the
// cluster-scoped binding below cannot be written once and left alone.
func nodeLabelerSubject(namespace string) rbacv1.Subject {
	return rbacv1.Subject{
		Kind:      rbacv1.ServiceAccountKind,
		Name:      nodeLabelerServiceAccount,
		Namespace: namespace,
	}
}

// rbacReader is the reader the ensure* helpers look the cluster-scoped RBAC up
// with. See the APIReader field for why the cached client will not do.
func (r *NodeLabelerReconciler) rbacReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *NodeLabelerReconciler) ensureServiceAccount(ctx context.Context, namespace string) error {
	// Create ServiceAccount. This one is namespaced, so a fresh install in a
	// new namespace gets a fresh object and there is nothing to converge.
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

	if err := r.ensureClusterRole(ctx); err != nil {
		return err
	}

	return r.ensureClusterRoleBinding(ctx, namespace)
}

// ensureClusterRole converges the node-labeler ClusterRole on the rules this
// operator version declares.
//
// It cannot be a bare Create that swallows AlreadyExists. The ClusterRole is
// cluster-scoped and outlives the operator Pod, so the first version ever
// installed in a cluster would own its rules forever: an upgrade that widens
// them rolls a new Pod, calls this again, gets AlreadyExists, and keeps the old
// grant. The same applies to a ClusterRole an administrator or a policy
// controller has since narrowed.
func (r *NodeLabelerReconciler) ensureClusterRole(ctx context.Context) error {
	log := logf.FromContext(ctx)
	want := nodeLabelerClusterRoleRules()

	existing := &rbacv1.ClusterRole{}
	err := r.rbacReader().Get(ctx, types.NamespacedName{Name: nodeLabelerServiceAccount}, existing)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get cluster role: %w", err)
		}

		clusterRole := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: nodeLabelerServiceAccount},
			Rules:      want,
		}
		if err := r.Create(ctx, clusterRole); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create cluster role: %w", err)
		}
		return nil
	}

	if equality.Semantic.DeepEqual(existing.Rules, want) {
		return nil
	}

	log.Info("Updating node-labeler ClusterRole rules", "clusterRole", nodeLabelerServiceAccount)
	existing.Rules = want
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("failed to update cluster role: %w", err)
	}
	return nil
}

// ensureClusterRoleBinding makes sure the node-labeler ClusterRoleBinding
// grants the ServiceAccount in this operator's namespace.
//
// The binding is cluster-scoped and has a fixed name, but the grant it carries
// names a namespace, and nothing gives the object an owner reference or a Helm
// release label. So it survives a `helm uninstall` and a plain Create would
// then get AlreadyExists on a reinstall into a different namespace, leaving the
// binding pointing at the old namespace's ServiceAccount. Both node-labeler
// workloads run as that ServiceAccount, so every one of their Pods is denied
// the node update, no node ever gets kairos.io/managed, and every NodeOp
// matches zero nodes while reporting success.
//
// The subject is appended rather than replacing the list: a second operator
// instance in another namespace is still granted by its own subject, and
// dropping it would revoke a running installation's only grant.
func (r *NodeLabelerReconciler) ensureClusterRoleBinding(ctx context.Context, namespace string) error {
	log := logf.FromContext(ctx)
	want := nodeLabelerSubject(namespace)

	existing := &rbacv1.ClusterRoleBinding{}
	err := r.rbacReader().Get(ctx, types.NamespacedName{Name: nodeLabelerServiceAccount}, existing)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get cluster role binding: %w", err)
		}

		clusterRoleBinding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: nodeLabelerServiceAccount},
			Subjects:   []rbacv1.Subject{want},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     clusterRoleKind,
				Name:     nodeLabelerServiceAccount,
			},
		}
		if err := r.Create(ctx, clusterRoleBinding); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create cluster role binding: %w", err)
		}
		return nil
	}

	for _, subject := range existing.Subjects {
		if subject == want {
			return nil
		}
	}

	log.Info("Adding this namespace's ServiceAccount to the node-labeler ClusterRoleBinding",
		"clusterRoleBinding", nodeLabelerServiceAccount, "namespace", namespace)
	existing.Subjects = append(existing.Subjects, want)
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("failed to update cluster role binding: %w", err)
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
	setupLog := logf.Log.WithName("setup")

	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}

	namespace := getOperatorNamespace()
	if err := addStartupTask(mgr, "node-labeler ServiceAccount and RBAC", func(ctx context.Context) error {
		return r.ensureServiceAccount(ctx, namespace)
	}); err != nil {
		return err
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
