package controller

import (
	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("genConfigMap", func() {
	// configSubPath returns the ConfigMap key a container mounts from the
	// generated ConfigMap volume.
	configSubPath := func(c corev1.Container) string {
		for _, m := range c.VolumeMounts {
			if m.Name == configVolumeName {
				return m.SubPath
			}
		}
		return ""
	}

	var artifact *buildv1alpha2.OSArtifact

	BeforeEach(func() {
		artifact = &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
			Spec: buildv1alpha2.OSArtifactSpec{
				Artifacts: &buildv1alpha2.ArtifactSpec{
					OSRelease:     "NAME=custom-os\n",
					KairosRelease: "KAIROS_VERSION=v9.9.9\n",
				},
			},
		}
	})

	It("stores kairosRelease under the key the kairos-release init container mounts", func() {
		cm := (&OSArtifactReconciler{}).genConfigMap(artifact)
		key := configSubPath(kairosReleaseContainer("tool"))
		Expect(key).ToNot(BeEmpty())
		Expect(cm.Data).To(HaveKeyWithValue(key, "KAIROS_VERSION=v9.9.9\n"))
	})

	It("stores osRelease under the key the os-release init container mounts", func() {
		cm := (&OSArtifactReconciler{}).genConfigMap(artifact)
		key := configSubPath(osReleaseContainer("tool"))
		Expect(key).ToNot(BeEmpty())
		Expect(cm.Data).To(HaveKeyWithValue(key, "NAME=custom-os\n"))
	})
})
