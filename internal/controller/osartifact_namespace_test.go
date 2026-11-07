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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
)

// Two OSArtifacts with the same name in different namespaces must not see
// each other's builder Pods or export Jobs.
var _ = Describe("OSArtifact children are scoped to the artifact namespace", func() {
	var (
		ctx          context.Context
		r            *OSArtifactReconciler
		name         string
		nsA, nsB     string
		otherPodSpec corev1.PodSpec
	)

	newNamespace := func() string {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: randStringRunes(10)}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		return ns.Name
	}

	newArtifact := func(namespace string, exporters []batchv1.JobSpec) *buildv1alpha2.OSArtifact {
		a := &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: buildv1alpha2.OSArtifactSpec{
				Image:     buildv1alpha2.ImageSpec{Ref: "quay.io/kairos/hadron:latest"},
				Artifacts: &buildv1alpha2.ArtifactSpec{ISO: true},
				Exporters: exporters,
			},
		}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		return a
	}

	// setStatus re-reads obj and applies mutate before every status write, so
	// a resourceVersion bump between Create and the write (409 Conflict) is
	// retried instead of failing the test.
	setStatus := func(obj client.Object, mutate func()) {
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
				return err
			}
			mutate()
			return k8sClient.Status().Update(ctx, obj)
		})).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()
		r = &OSArtifactReconciler{
			Client:       k8sClient,
			Scheme:       k8sClient.Scheme(),
			ToolImage:    fmt.Sprintf("quay.io/kairos/auroraboot:%s", CompatibleAurorabootVersion),
			BuildahImage: fmt.Sprintf("quay.io/buildah/stable:%s", CompatibleBuildahVersion),
		}
		name = randStringRunes(10)
		nsA = newNamespace()
		nsB = newNamespace()
		otherPodSpec = corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{{Name: "c", Image: "busybox"}},
		}
	})

	It("checkBuild ignores a running builder Pod of a same-named artifact in another namespace", func() {
		podA := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name + "-pod",
				Namespace: nsA,
				Labels:    map[string]string{artifactLabel: name},
			},
			Spec: otherPodSpec,
		}
		// The controller tests run on a real cluster: gate the Pod so the
		// scheduler never binds it and no kubelet overwrites its phase.
		podA.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "kairos.io/test-hold"}}
		Expect(k8sClient.Create(ctx, podA)).To(Succeed())
		setStatus(podA, func() { podA.Status.Phase = corev1.PodSucceeded })

		artifactB := newArtifact(nsB, nil)
		setStatus(artifactB, func() { artifactB.Status.Phase = buildv1alpha2.Building })

		_, err := r.checkBuild(ctx, artifactB)
		Expect(err).ToNot(HaveOccurred())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(artifactB), artifactB)).To(Succeed())
		Expect(artifactB.Status.Phase).To(BeEquivalentTo(buildv1alpha2.Building),
			"team-b must not move to Exporting on team-a's Pod")

		var podsB corev1.PodList
		Expect(k8sClient.List(ctx, &podsB, client.InNamespace(nsB),
			client.MatchingLabels{artifactLabel: name})).To(Succeed())
		Expect(podsB.Items).To(HaveLen(1), "team-b must start its own builder Pod")
	})

	It("checkExport ignores a succeeded export Job of a same-named artifact in another namespace", func() {
		exporter := batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{Spec: otherPodSpec},
		}

		jobA := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name + "-export-0",
				Namespace:   nsA,
				Labels:      map[string]string{artifactLabel: name},
				Annotations: map[string]string{artifactExporterIndexAnnotation: "0"},
			},
			Spec: exporter,
		}
		Expect(k8sClient.Create(ctx, jobA)).To(Succeed())
		Expect(markJobAsCompleted(ctx, k8sClient, jobA)).To(Succeed())

		artifactB := newArtifact(nsB, []batchv1.JobSpec{exporter})
		_, err := r.createPVC(ctx, artifactB)
		Expect(err).ToNot(HaveOccurred())
		setStatus(artifactB, func() { artifactB.Status.Phase = buildv1alpha2.Exporting })

		_, err = r.checkExport(ctx, artifactB)
		Expect(err).ToNot(HaveOccurred())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(artifactB), artifactB)).To(Succeed())
		Expect(artifactB.Status.Phase).To(BeEquivalentTo(buildv1alpha2.Exporting),
			"team-b must not become Ready on team-a's export Job")

		var jobsB batchv1.JobList
		Expect(k8sClient.List(ctx, &jobsB, client.InNamespace(nsB),
			client.MatchingLabels{artifactLabel: name})).To(Succeed())
		Expect(jobsB.Items).To(HaveLen(1), "team-b must create its own export Job")
	})
})
