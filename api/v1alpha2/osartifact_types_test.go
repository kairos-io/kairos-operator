package v1alpha2_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kairos-io/kairos-operator/api/v1alpha2"
)

// validImageRef returns a minimal spec with image.ref and artifacts set so Validate passes (Ref requires artifacts).
func validImageRef(ref string) v1alpha2.OSArtifactSpec {
	return v1alpha2.OSArtifactSpec{
		Image:     v1alpha2.ImageSpec{Ref: ref},
		Artifacts: &v1alpha2.ArtifactSpec{ISO: true}, // Ref requires at least one artifact type
	}
}

var _ = Describe("OSArtifactSpec.ArchSanitized", func() {
	Describe("Valid architectures (from spec.artifacts)", func() {
		It("should accept 'amd64'", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image:     v1alpha2.ImageSpec{Ref: "img"},
				Artifacts: &v1alpha2.ArtifactSpec{Arch: "amd64"},
			}
			arch, err := spec.ArchSanitized()
			Expect(err).ToNot(HaveOccurred())
			Expect(arch).To(Equal("amd64"))
		})

		It("should accept 'arm64'", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image:     v1alpha2.ImageSpec{Ref: "img"},
				Artifacts: &v1alpha2.ArtifactSpec{Arch: "arm64"},
			}
			arch, err := spec.ArchSanitized()
			Expect(err).ToNot(HaveOccurred())
			Expect(arch).To(Equal("arm64"))
		})

		// TODO: Arch will need to be set for the first stage too when we fix this:
		// https://github.com/kairos-io/kairos/issues/3966
		It("should accept empty string when Artifacts is nil", func() {
			spec := validImageRef("img")
			arch, err := spec.ArchSanitized()
			Expect(err).ToNot(HaveOccurred())
			Expect(arch).To(Equal(""))
		})

		It("should accept empty string when Artifacts.Arch is empty", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image:     v1alpha2.ImageSpec{Ref: "img"},
				Artifacts: &v1alpha2.ArtifactSpec{},
			}
			arch, err := spec.ArchSanitized()
			Expect(err).ToNot(HaveOccurred())
			Expect(arch).To(Equal(""))
		})
	})

	Describe("Invalid architectures", func() {
		It("should reject 'x86_64'", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image:     v1alpha2.ImageSpec{Ref: "img"},
				Artifacts: &v1alpha2.ArtifactSpec{Arch: "x86_64"},
			}
			arch, err := spec.ArchSanitized()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("arch must be either 'amd64', 'arm64', or empty"))
			Expect(arch).To(Equal(""))
		})

		It("should reject 'AMD64' (uppercase)", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image:     v1alpha2.ImageSpec{Ref: "img"},
				Artifacts: &v1alpha2.ArtifactSpec{Arch: "AMD64"},
			}
			_, err := spec.ArchSanitized()
			Expect(err).To(HaveOccurred())
		})
	})
})

var _ = Describe("OSArtifactSpec.Validate", func() {
	Describe("spec.image is required", func() {
		It("validates when image.ref is set with artifacts", func() {
			spec := validImageRef("quay.io/kairos/kairos:v1")
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns error when image.ref is set without artifacts", func() {
			spec := v1alpha2.OSArtifactSpec{Image: v1alpha2.ImageSpec{Ref: "quay.io/kairos/kairos:v1"}}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.artifacts is required when spec.image.ref is set"))
		})
	})

	Describe("spec.volumes", func() {
		It("returns error for reserved volume name", func() {
			spec := validImageRef("img")
			spec.Volumes = []corev1.Volume{{Name: "artifacts"}}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reserved"))
		})

		It("returns error for duplicate volume name", func() {
			spec := validImageRef("img")
			spec.Volumes = []corev1.Volume{{Name: "v1"}, {Name: "v1"}}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("duplicate"))
		})
	})

	Describe("spec.image", func() {
		It("returns error when ref empty and neither buildOptions nor ociSpec", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{},
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at least one of buildOptions or ociSpec must be set"))
		})

		It("returns error when buildImage set and ref set (mutually exclusive)", func() {
			spec := validImageRef("quay.io/kairos/kairos:v1")
			spec.Image.BuildImage = &v1alpha2.BuildImage{Registry: "my-registry.io", Repository: "my-image", Tag: "tag"}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("buildImage"))
			Expect(err.Error()).To(ContainSubstring("ref must be empty"))
		})

		It("returns nil when ref set (buildOptions and ociSpec ignored)", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					Ref:          "quay.io/kairos/kairos:v1",
					BuildOptions: &v1alpha2.BuildOptions{Version: "v1"},
				},
				Artifacts: &v1alpha2.ArtifactSpec{ISO: true}, // Ref requires at least one artifact type
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns error when buildOptions without version", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					BuildOptions: &v1alpha2.BuildOptions{BaseImage: "ubuntu:22.04"},
				},
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("buildOptions.version is required"))
		})

		It("returns error when buildOptions without baseImage", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					BuildOptions: &v1alpha2.BuildOptions{Version: "v3.6.0"},
				},
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("buildOptions.baseImage is required"))
		})

		It("returns nil when only ref", func() {
			spec := validImageRef("quay.io/kairos/kairos:v1")
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns nil when only buildOptions with version and baseImage", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					BuildOptions: &v1alpha2.BuildOptions{Version: "v3.6.0", BaseImage: "ubuntu:22.04"},
				},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns nil when only ociSpec ref", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					OCISpec: &v1alpha2.OCISpec{
						Ref: &v1alpha2.SecretKeySelector{Name: "my-ocispec", Key: "ociSpec"},
					},
				},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns nil when both buildOptions and ociSpec (operator injects FROM + kairos-init)", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					BuildOptions: &v1alpha2.BuildOptions{Version: "v3.6.0", BaseImage: "ubuntu:22.04"},
					OCISpec: &v1alpha2.OCISpec{
						Ref: &v1alpha2.SecretKeySelector{Name: "my-ocispec", Key: "ociSpec"},
					},
				},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns error when ociSpec.buildContextVolume references missing volume", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					OCISpec: &v1alpha2.OCISpec{
						Ref:                &v1alpha2.SecretKeySelector{Name: "df", Key: "ociSpec"},
						BuildContextVolume: "missing-vol",
					},
				},
				Volumes: []corev1.Volume{{Name: "ctx"}},
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("buildContextVolume"))
		})

		It("returns nil when ociSpec.buildContextVolume references existing volume", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					OCISpec: &v1alpha2.OCISpec{
						Ref:                &v1alpha2.SecretKeySelector{Name: "df", Key: "ociSpec"},
						BuildContextVolume: "ctx",
					},
				},
				Volumes: []corev1.Volume{{Name: "ctx"}},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns error when image.caCertificatesVolume references missing volume", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					OCISpec: &v1alpha2.OCISpec{
						Ref: &v1alpha2.SecretKeySelector{Name: "df", Key: "ociSpec"},
					},
					CACertificatesVolume: "missing-ca-vol",
				},
				Volumes: []corev1.Volume{{Name: "other"}},
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("caCertificatesVolume"))
		})

		It("returns nil when image.caCertificatesVolume references existing volume", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					OCISpec: &v1alpha2.OCISpec{
						Ref: &v1alpha2.SecretKeySelector{Name: "df", Key: "ociSpec"},
					},
					CACertificatesVolume: "my-ca-certs",
				},
				Volumes: []corev1.Volume{{Name: "my-ca-certs"}},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns nil when buildImage set and building (ref empty)", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					BuildImage: &v1alpha2.BuildImage{Registry: "my-registry.io", Repository: "my-image", Tag: "tag"},
					OCISpec:    &v1alpha2.OCISpec{Ref: &v1alpha2.SecretKeySelector{Name: "df", Key: "ociSpec"}},
				},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns error when buildImage set but tag missing", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					BuildImage: &v1alpha2.BuildImage{Registry: "r.io", Repository: "img", Tag: ""},
					OCISpec:    &v1alpha2.OCISpec{Ref: &v1alpha2.SecretKeySelector{Name: "df", Key: "ociSpec"}},
				},
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("buildImage"))
			Expect(err.Error()).To(ContainSubstring("registry, repository, and tag are all required"))
		})

		It("returns error when push is true but buildImage is missing", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					Push:    true,
					OCISpec: &v1alpha2.OCISpec{Ref: &v1alpha2.SecretKeySelector{Name: "df", Key: "ociSpec"}},
				},
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("push"))
			Expect(err.Error()).To(ContainSubstring("buildImage"))
		})

		It("returns error when push is true but buildImage is incomplete", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					Push:       true,
					BuildImage: &v1alpha2.BuildImage{Registry: "r.io", Repository: "", Tag: "latest"},
					OCISpec:    &v1alpha2.OCISpec{Ref: &v1alpha2.SecretKeySelector{Name: "df", Key: "ociSpec"}},
				},
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("buildImage"))
			// Either our push validation or the "all required" validation may fire
			Expect(err.Error()).To(Or(ContainSubstring("push"), ContainSubstring("registry, repository, and tag are all required")))
		})

		It("returns nil when push is true and buildImage is set with registry, repository, tag", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					Push:       true,
					BuildImage: &v1alpha2.BuildImage{Registry: "r.io", Repository: "ns/img", Tag: "latest"},
					OCISpec:    &v1alpha2.OCISpec{Ref: &v1alpha2.SecretKeySelector{Name: "df", Key: "ociSpec"}},
				},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns nil when imageCredentialsSecretRef is set (used for pull and push)", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					BuildImage:                &v1alpha2.BuildImage{Registry: "r.io", Repository: "ns/img", Tag: "latest"},
					OCISpec:                   &v1alpha2.OCISpec{Ref: &v1alpha2.SecretKeySelector{Name: "df", Key: "ociSpec"}},
					ImageCredentialsSecretRef: &v1alpha2.SecretKeySelector{Name: "registry-creds"},
				},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})
	})

	Describe("spec.artifacts", func() {
		It("returns error when overlayISOVolume references missing volume", func() {
			spec := validImageRef("img")
			spec.Artifacts = &v1alpha2.ArtifactSpec{ISO: true, OverlayISOVolume: "missing"}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlayISOVolume"))
			Expect(err.Error()).To(ContainSubstring("missing"))
		})

		It("returns error when overlayRootfsVolume references missing volume", func() {
			spec := validImageRef("img")
			spec.Artifacts = &v1alpha2.ArtifactSpec{ISO: true, OverlayRootfsVolume: "missing"}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlayRootfsVolume"))
		})

		It("returns nil when overlay volumes exist in spec.volumes", func() {
			spec := validImageRef("img")
			spec.Volumes = []corev1.Volume{{Name: "iso-ov"}, {Name: "rootfs-ov"}}
			spec.Artifacts = &v1alpha2.ArtifactSpec{
				ISO:                 true,
				OverlayISOVolume:    "iso-ov",
				OverlayRootfsVolume: "rootfs-ov",
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		Describe("artifacts.volume", func() {
			It("returns error when artifacts.volume references missing volume", func() {
				spec := validImageRef("img")
				spec.Artifacts.Volume = "missing-artifacts-vol"
				spec.Volumes = []corev1.Volume{{Name: "other"}}
				err := spec.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("artifacts.volume"))
				Expect(err.Error()).To(ContainSubstring("missing-artifacts-vol"))
			})

			It("returns nil when artifacts.volume references existing volume", func() {
				spec := validImageRef("img")
				spec.Artifacts.Volume = "my-artifacts"
				spec.Volumes = []corev1.Volume{{Name: "my-artifacts", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
				Expect(spec.Validate()).ToNot(HaveOccurred())
			})

			It("returns nil when artifacts.volume is empty (default behavior)", func() {
				spec := validImageRef("img")
				Expect(spec.Artifacts.Volume).To(BeEmpty())
				Expect(spec.Validate()).ToNot(HaveOccurred())
			})
		})

		Describe("uki", func() {
			It("returns error when uki.iso is true but keysVolume is empty", func() {
				spec := validImageRef("img")
				spec.Artifacts = &v1alpha2.ArtifactSpec{
					UKI: &v1alpha2.UKISpec{ISO: true},
				}
				err := spec.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("uki.keysVolume"))
				Expect(err.Error()).To(ContainSubstring("required"))
			})

			It("returns error when uki.keysVolume references missing volume", func() {
				spec := validImageRef("img")
				spec.Artifacts = &v1alpha2.ArtifactSpec{
					UKI: &v1alpha2.UKISpec{ISO: true, KeysVolume: "missing-keys"},
				}
				err := spec.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("uki.keysVolume"))
				Expect(err.Error()).To(ContainSubstring("missing-keys"))
			})

			It("returns nil when uki has keysVolume and volume exists in spec.volumes", func() {
				spec := validImageRef("img")
				spec.Volumes = []corev1.Volume{{Name: "uki-keys"}}
				spec.Artifacts = &v1alpha2.ArtifactSpec{
					UKI: &v1alpha2.UKISpec{ISO: true, KeysVolume: "uki-keys"},
				}
				Expect(spec.Validate()).ToNot(HaveOccurred())
			})

			It("returns nil when only uki artifacts are requested (no iso/cloudImage/etc)", func() {
				spec := validImageRef("img")
				spec.Volumes = []corev1.Volume{{Name: "uki-keys"}}
				spec.Artifacts = &v1alpha2.ArtifactSpec{
					UKI: &v1alpha2.UKISpec{EFI: true, KeysVolume: "uki-keys"},
				}
				Expect(spec.Validate()).ToNot(HaveOccurred())
			})
		})
	})
})

const (
	nameOverride = "chronos"
	defaultName  = "kairos"
)

var _ = Describe("OSArtifact.ArtifactNameFor", func() {
	Describe("with name overrides set", func() {
		It("returns nameOverride.ISO for kind 'iso'", func() {
			artifact := &v1alpha2.OSArtifact{
				Spec: v1alpha2.OSArtifactSpec{
					NameOverride: v1alpha2.NameOverrideSpec{
						ISO: nameOverride,
					},
				},
			}
			artifact.Name = defaultName
			Expect(artifact.ArtifactNameFor("iso")).To(Equal(nameOverride))
		})

		It("returns nameOverride.CloudImage for kind 'cloud'", func() {
			artifact := &v1alpha2.OSArtifact{
				Spec: v1alpha2.OSArtifactSpec{
					NameOverride: v1alpha2.NameOverrideSpec{
						CloudImage: nameOverride,
					},
				},
			}
			artifact.Name = defaultName
			Expect(artifact.ArtifactNameFor("cloud")).To(Equal(nameOverride))
		})

		It("returns nameOverride.AzureImage for kind 'azure'", func() {
			artifact := &v1alpha2.OSArtifact{
				Spec: v1alpha2.OSArtifactSpec{
					NameOverride: v1alpha2.NameOverrideSpec{
						AzureImage: nameOverride,
					},
				},
			}
			artifact.Name = defaultName
			Expect(artifact.ArtifactNameFor("azure")).To(Equal(nameOverride))
		})

		It("returns nameOverride.GCEImage for kind 'gce'", func() {
			artifact := &v1alpha2.OSArtifact{
				Spec: v1alpha2.OSArtifactSpec{
					NameOverride: v1alpha2.NameOverrideSpec{
						GCEImage: nameOverride,
					},
				},
			}
			artifact.Name = defaultName
			Expect(artifact.ArtifactNameFor("gce")).To(Equal(nameOverride))
		})

		It("returns nameOverride.UKI for kind 'uki'", func() {
			artifact := &v1alpha2.OSArtifact{
				Spec: v1alpha2.OSArtifactSpec{
					NameOverride: v1alpha2.NameOverrideSpec{
						UKI: nameOverride,
					},
				},
			}
			artifact.Name = defaultName
			Expect(artifact.ArtifactNameFor("uki")).To(Equal(nameOverride))
		})

		It("returns metadata.name for kind 'netboot' (no override field)", func() {
			artifact := &v1alpha2.OSArtifact{
				Spec: v1alpha2.OSArtifactSpec{},
			}
			artifact.Name = defaultName
			Expect(artifact.ArtifactNameFor("netboot")).To(Equal(defaultName))
		})

		It("falls back to the resource name for unmatched kinds", func() {
			artifact := &v1alpha2.OSArtifact{
				Spec: v1alpha2.OSArtifactSpec{
					NameOverride: v1alpha2.NameOverrideSpec{
						ISO: nameOverride,
					},
				},
			}
			artifact.Name = defaultName
			Expect(artifact.ArtifactNameFor("cloud")).To(Equal(defaultName))
			Expect(artifact.ArtifactNameFor("unknown")).To(Equal(defaultName))
		})
	})

	Describe("without name overrides", func() {
		It("returns the resource name for every kind when no overrides are set", func() {
			artifact := &v1alpha2.OSArtifact{
				Spec: v1alpha2.OSArtifactSpec{},
			}
			artifact.Name = defaultName
			Expect(artifact.ArtifactNameFor("iso")).To(Equal(defaultName))
			Expect(artifact.ArtifactNameFor("cloud")).To(Equal(defaultName))
			Expect(artifact.ArtifactNameFor("azure")).To(Equal(defaultName))
			Expect(artifact.ArtifactNameFor("gce")).To(Equal(defaultName))
			Expect(artifact.ArtifactNameFor("netboot")).To(Equal(defaultName))
			Expect(artifact.ArtifactNameFor("uki")).To(Equal(defaultName))
		})
	})
})

var _ = Describe("OSArtifactSpec.Resources", func() {
	It("should have zero-value Resources when not set", func() {
		spec := v1alpha2.OSArtifactSpec{
			Image:     v1alpha2.ImageSpec{Ref: "img"},
			Artifacts: &v1alpha2.ArtifactSpec{ISO: true},
		}
		Expect(spec.Resources).To(Equal(v1alpha2.ResourcesSpec{}))
	})

	It("should accept resources with requests and limits for a specific artifact kind", func() {
		resources := &v1alpha2.ResourcesSpec{
			ISO: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
		}
		spec := v1alpha2.OSArtifactSpec{
			Image:     v1alpha2.ImageSpec{Ref: "img"},
			Artifacts: &v1alpha2.ArtifactSpec{ISO: true},
			Resources: *resources,
		}
		Expect(spec.Resources.ISO).ToNot(BeNil())
		Expect(spec.Resources.ISO.Requests).ToNot(BeNil())
		Expect(spec.Resources.ISO.Limits).ToNot(BeNil())
		Expect(spec.Resources.ISO.Requests.Cpu().String()).To(Equal("500m"))
		Expect(spec.Resources.ISO.Limits.Memory().String()).To(Equal("1Gi"))
	})

	It("should accept resources with only limits for a specific artifact kind", func() {
		resources := &v1alpha2.ResourcesSpec{
			CloudImage: &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("2"),
				},
			},
		}
		spec := v1alpha2.OSArtifactSpec{
			Image:     v1alpha2.ImageSpec{Ref: "img"},
			Artifacts: &v1alpha2.ArtifactSpec{CloudImage: true},
			Resources: *resources,
		}
		Expect(spec.Resources.CloudImage.Limits.Cpu().String()).To(Equal("2"))
		Expect(spec.Resources.CloudImage.Requests).To(BeNil())
	})

	It("should accept resources with only requests for a specific artifact kind", func() {
		resources := &v1alpha2.ResourcesSpec{
			Pod: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			},
		}
		spec := v1alpha2.OSArtifactSpec{
			Image:     v1alpha2.ImageSpec{Ref: "img"},
			Artifacts: &v1alpha2.ArtifactSpec{ISO: true},
			Resources: *resources,
		}
		Expect(spec.Resources.Pod.Requests.Memory().String()).To(Equal("256Mi"))
		Expect(spec.Resources.Pod.Limits).To(BeNil())
	})

	It("should accept ResourcesSpec with multiple artifact kinds set", func() {
		resources := &v1alpha2.ResourcesSpec{
			ISO:        &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}},
			CloudImage: &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")}},
			Pod:        &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}},
		}
		spec := v1alpha2.OSArtifactSpec{
			Image:     v1alpha2.ImageSpec{Ref: "img"},
			Artifacts: &v1alpha2.ArtifactSpec{ISO: true, CloudImage: true},
			Resources: *resources,
		}
		Expect(spec.Resources.ISO).ToNot(BeNil())
		Expect(spec.Resources.CloudImage).ToNot(BeNil())
		Expect(spec.Resources.Pod).ToNot(BeNil())
		Expect(spec.Resources.Netboot).To(BeNil())
		Expect(spec.Resources.UKI).To(BeNil())
	})
})
