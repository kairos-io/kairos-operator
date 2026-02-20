package e2e

import (
	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

var _ = Describe("OSArtifact ISO build test", func() {
	var artifactName string
	var artifactLabelSelector labels.Selector
	var tc *TestClients

	BeforeEach(func() {
		tc = SetupTestClients()

		artifact := &buildv1alpha2.OSArtifact{
			TypeMeta: metav1.TypeMeta{
				Kind:       "OSArtifact",
				APIVersion: buildv1alpha2.GroupVersion.String(),
			},
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "simple-",
			},
			Spec: buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{
					Ref: "quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0",
				},
				Artifacts: &buildv1alpha2.ArtifactSpec{
					ISO:      true,
					DiskSize: "",
				},
				Exporters: []batchv1.JobSpec{
					{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{},
							Spec: corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									{
										Name:    "test",
										Image:   "debian:latest",
										Command: []string{"bash"},
										Args:    []string{"-xec", "[ -f /artifacts/*.iso ]"},
										VolumeMounts: []corev1.VolumeMount{
											{
												Name:      "artifacts",
												ReadOnly:  true,
												MountPath: "/artifacts",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		artifactName, artifactLabelSelector = tc.CreateArtifact(artifact)
	})

	It("works", func() {
		tc.WaitForBuildCompletion(artifactName, artifactLabelSelector)
		tc.WaitForExportCompletion(artifactLabelSelector)
		tc.Cleanup(artifactName, artifactLabelSelector)
	})
})
