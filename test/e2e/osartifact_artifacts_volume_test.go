package e2e

import (
	"context"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

var _ = Describe("OSArtifact with configurable artifacts volume (spec.artifacts.volume)", func() {
	var artifactName string
	var artifactLabelSelector labels.Selector
	var tc *TestClients
	const pvcName = "e2e-artifacts-volume-pvc"

	BeforeEach(func() {
		tc = SetupTestClients()

		// Create a user-owned PVC that will be used as the artifacts volume (instead of the operator creating one).
		pvc := &corev1.PersistentVolumeClaim{
			TypeMeta: metav1.TypeMeta{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "PersistentVolumeClaim",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcName,
				Namespace: "default",
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: map[corev1.ResourceName]resource.Quantity{
						corev1.ResourceStorage: resource.MustParse("5Gi"),
					},
				},
			},
		}
		uPVC := &unstructured.Unstructured{}
		uPVC.Object, _ = runtime.DefaultUnstructuredConverter.ToUnstructured(pvc)
		_, err := tc.PVCs.Create(context.TODO(), uPVC, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred(), "create user PVC for artifacts.volume")

		artifact := &buildv1alpha2.OSArtifact{
			TypeMeta: metav1.TypeMeta{
				Kind:       "OSArtifact",
				APIVersion: buildv1alpha2.GroupVersion.String(),
			},
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "artifacts-vol-",
			},
			Spec: buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{
					Ref: HadronPreKairosified,
				},
				Artifacts: &buildv1alpha2.ArtifactSpec{
					ISO:      true,
					DiskSize: "",
					Volume:   "my-artifacts",
				},
				Volumes: []corev1.Volume{
					{
						Name: "my-artifacts",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: pvcName,
							},
						},
					},
				},
				Exporters: []batchv1.JobSpec{
					{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{},
							Spec: corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									{
										Name:    "verify",
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

	AfterEach(func() {
		// Delete the user-created PVC (operator does not own it when artifacts.volume is set).
		_ = tc.PVCs.Delete(context.TODO(), pvcName, metav1.DeleteOptions{})
	})

	It("builds and exports using the user-supplied artifacts volume", func() {
		tc.WaitForBuildCompletion(artifactName, artifactLabelSelector)
		tc.WaitForExportCompletion(artifactLabelSelector)
		tc.Cleanup(artifactName, artifactLabelSelector)
	})
})
