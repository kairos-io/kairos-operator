package controller

import (
	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("OSArtifact schema", func() {
	newArtifact := func(name string, opts *buildv1alpha2.BuildOptions) *buildv1alpha2.OSArtifact {
		return &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{BuildOptions: opts},
			},
		}
	}

	It("rejects buildOptions without baseImage at admission", func() {
		err := k8sClient.Create(ctx, newArtifact("no-base-image", &buildv1alpha2.BuildOptions{Version: "v3.6.0"}))
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an invalid error, got %v", err)
		Expect(err.Error()).To(ContainSubstring("baseImage"))
	})

	It("accepts buildOptions with baseImage", func() {
		artifact := newArtifact("with-base-image", &buildv1alpha2.BuildOptions{Version: "v3.6.0", BaseImage: "ubuntu:24.04"})
		Expect(k8sClient.Create(ctx, artifact)).To(Succeed())
		Expect(k8sClient.Delete(ctx, artifact)).To(Succeed())
	})
})
