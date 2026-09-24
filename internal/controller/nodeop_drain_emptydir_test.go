package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

// These specs use the fake client rather than the envtest suite because they
// assert that nothing was evicted, which is a statement about the requests
// drainNode issued and needs no API server.

const drainTestNode = "node-a"

// drainTestPod builds a pod on drainTestNode owned by a ReplicaSet, so the
// default DrainOptions.Force does not skip it.
func drainTestPod(name string, opts ...func(*corev1.Pod)) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "ReplicaSet",
				Name:       name + "-rs",
				UID:        types.UID("22222222-2222-2222-2222-222222222222"),
				Controller: asBool(true),
			}},
		},
		Spec: corev1.PodSpec{NodeName: drainTestNode},
	}
	for _, o := range opts {
		o(pod)
	}
	return pod
}

func withEmptyDir(pod *corev1.Pod) {
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name:         "scratch",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
}

func ownedByDaemonSet(pod *corev1.Pod) {
	pod.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "apps/v1",
		Kind:       "DaemonSet",
		Name:       pod.Name + "-ds",
		UID:        types.UID("33333333-3333-3333-3333-333333333333"),
		Controller: asBool(true),
	}}
}

func onAnotherNode(pod *corev1.Pod) { pod.Spec.NodeName = "node-b" }

// runDrain drains drainTestNode over a fake client seeded with pods, and
// returns the drain error plus the names of the pods that survived.
func runDrain(t *testing.T, opts *kairosiov1alpha1.DrainOptions, pods ...*corev1.Pod) (error, []string) {
	t.Helper()

	objs := []client.Object{&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: drainTestNode}}}
	for _, p := range pods {
		objs = append(objs, p)
	}
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()

	node := &corev1.Node{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: drainTestNode}, node); err != nil {
		t.Fatalf("seeding the node: %v", err)
	}

	r := &NodeOpReconciler{Client: c, Scheme: scheme.Scheme}
	drainErr := r.drainNode(context.Background(), node, opts)

	var alive []string
	for _, p := range pods {
		err := c.Get(context.Background(), client.ObjectKeyFromObject(p), &corev1.Pod{})
		switch {
		case err == nil:
			alive = append(alive, p.Name)
		case apierrors.IsNotFound(err):
		default:
			t.Fatalf("reading pod %s back: %v", p.Name, err)
		}
	}
	return drainErr, alive
}

func TestDrainRefusesToDestroyEmptyDirData(t *testing.T) {
	// deleteEmptyDirData defaults to false, which the CRD and the shipped
	// sample present as "the data in emptyDir volumes is not deleted". The
	// drain must therefore refuse, and refuse before evicting anything: a
	// partial drain would have destroyed the very data the refusal is about.
	withData := drainTestPod("cache", withEmptyDir)
	plain := drainTestPod("stateless")

	err, alive := runDrain(t, &kairosiov1alpha1.DrainOptions{}, withData, plain)
	if err == nil {
		t.Fatal("expected the drain to be refused, got nil")
	}
	if !strings.Contains(err.Error(), "default/cache") {
		t.Errorf("the error should name the pod holding the data, got %q", err)
	}
	if len(alive) != 2 {
		t.Errorf("a refused drain must evict nothing, but only %v survived", alive)
	}
}

func TestDrainDeletesEmptyDirDataWhenAllowed(t *testing.T) {
	withData := drainTestPod("cache", withEmptyDir)

	err, alive := runDrain(t, &kairosiov1alpha1.DrainOptions{DeleteEmptyDirData: asBool(true)}, withData)
	if err != nil {
		t.Fatalf("an opted-in drain should succeed, got %v", err)
	}
	if len(alive) != 0 {
		t.Errorf("expected the pod to be evicted, %v survived", alive)
	}
}

func TestDrainIgnoresEmptyDirOnPodsItWouldNotEvict(t *testing.T) {
	// The guard sits after the skip rules, so a pod the drain was never going
	// to touch must not block it. A DaemonSet pod with a scratch volume is the
	// common case: log shippers and CNI agents nearly all have one, and
	// ignoreDaemonSets defaults to true.
	daemon := drainTestPod("log-shipper", withEmptyDir, ownedByDaemonSet)
	elsewhere := drainTestPod("other-node-cache", withEmptyDir, onAnotherNode)
	plain := drainTestPod("stateless")

	err, alive := runDrain(t, &kairosiov1alpha1.DrainOptions{}, daemon, elsewhere, plain)
	if err != nil {
		t.Fatalf("expected the drain to proceed, got %v", err)
	}
	if len(alive) != 2 || alive[0] != "log-shipper" || alive[1] != "other-node-cache" {
		t.Errorf("expected only the stateless pod to be evicted, survivors were %v", alive)
	}
}

func TestDrainProceedsWithoutEmptyDirVolumes(t *testing.T) {
	// A hostPath or projected volume is not emptyDir and must not be read as
	// one, or the guard would refuse every drain on a node running anything
	// with a mounted secret.
	hostPath := drainTestPod("with-hostpath")
	hostPath.Spec.Volumes = []corev1.Volume{{
		Name:         "host",
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/log"}},
	}, {
		Name:         "creds",
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "creds"}},
	}}

	err, alive := runDrain(t, &kairosiov1alpha1.DrainOptions{}, hostPath)
	if err != nil {
		t.Fatalf("expected the drain to proceed, got %v", err)
	}
	if len(alive) != 0 {
		t.Errorf("expected the pod to be evicted, %v survived", alive)
	}
}
