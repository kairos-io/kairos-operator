package controller

import (
	"context"
	"fmt"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get

// operatorOwnerReference returns an owner reference to the Deployment that
// runs this operator, found by walking the controller Pod (CONTROLLER_POD_NAME
// in the operator namespace) to its ReplicaSet and on to the Deployment.
//
// Objects the operator creates at runtime in its own namespace (the
// node-labeler DaemonSet, ServiceAccount and Jobs) carry this reference, so
// the garbage collector removes them when the operator is uninstalled even if
// the namespace survives.
//
// It returns nil and no error when the operator does not run under a
// Deployment (CONTROLLER_POD_NAME unset, e.g. `make run`, or a Pod that is not
// owned by a ReplicaSet of a Deployment). The reference is not a controller
// reference and does not block owner deletion, so it needs no extra RBAC on
// the Deployment.
func operatorOwnerReference(ctx context.Context, reader client.Reader) (*metav1.OwnerReference, error) {
	podName := os.Getenv(controllerPodNameEnv)
	if podName == "" {
		return nil, nil
	}
	namespace := getOperatorNamespace()

	pod := &corev1.Pod{}
	if err := reader.Get(ctx, types.NamespacedName{Name: podName, Namespace: namespace}, pod); err != nil {
		return nil, fmt.Errorf("failed to get operator pod %s/%s: %w", namespace, podName, err)
	}
	rsRef := metav1.GetControllerOf(pod)
	if rsRef == nil || rsRef.Kind != "ReplicaSet" {
		return nil, nil
	}

	rs := &appsv1.ReplicaSet{}
	if err := reader.Get(ctx, types.NamespacedName{Name: rsRef.Name, Namespace: namespace}, rs); err != nil {
		return nil, fmt.Errorf("failed to get operator replicaset %s/%s: %w", namespace, rsRef.Name, err)
	}
	deployRef := metav1.GetControllerOf(rs)
	if deployRef == nil || deployRef.Kind != "Deployment" {
		return nil, nil
	}

	return &metav1.OwnerReference{
		APIVersion: deployRef.APIVersion,
		Kind:       deployRef.Kind,
		Name:       deployRef.Name,
		UID:        deployRef.UID,
	}, nil
}

// addOwnerReference appends ref to obj's owner references unless ref is nil
// or obj already has an owner with the same UID. It reports whether obj
// changed.
func addOwnerReference(obj metav1.Object, ref *metav1.OwnerReference) bool {
	if ref == nil {
		return false
	}
	refs := obj.GetOwnerReferences()
	for _, existing := range refs {
		if existing.UID == ref.UID {
			return false
		}
	}
	obj.SetOwnerReferences(append(refs, *ref))
	return true
}
