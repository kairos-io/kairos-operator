package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// runBuildISOCommand runs the build-iso shell command with every absolute
// container path moved under a temp dir and a fake auroraboot on PATH. The
// fake records the directory --overlay-iso names, so the test sees the tree
// AuroraBoot would lay over the ISO.
func runBuildISOCommand(cmd, grubCfg string, overlayFiles map[string]string) (overlayArg, root string) {
	root = GinkgoT().TempDir()
	if grubCfg != "" {
		Expect(os.MkdirAll(filepath.Join(root, filepath.Dir(grubConfigMountPath)), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, grubConfigMountPath), []byte(grubCfg), 0o644)).To(Succeed())
	}
	for name, content := range overlayFiles {
		p := filepath.Join(root, overlayISOMountPath, name)
		Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
		Expect(os.WriteFile(p, []byte(content), 0o644)).To(Succeed())
	}
	bin := filepath.Join(root, "bin")
	Expect(os.MkdirAll(bin, 0o755)).To(Succeed())
	fake := `#!/bin/sh
while [ $# -gt 0 ]; do
  [ "$1" = "--overlay-iso" ] && echo "$2" > "` + root + `/overlay-arg"
  shift
done
`
	Expect(os.WriteFile(filepath.Join(bin, "auroraboot"), []byte(fake), 0o755)).To(Succeed())

	for _, p := range []string{grubConfigMountPath, isoOverlayDir, overlayISOMountPath, overlayRootfsMountPath} {
		cmd = strings.ReplaceAll(cmd, p, root+p)
	}
	c := exec.Command("sh", "-ce", cmd)
	c.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := c.CombinedOutput()
	Expect(err).ToNot(HaveOccurred(), "command %q: %s", cmd, out)

	arg, err := os.ReadFile(filepath.Join(root, "overlay-arg"))
	if os.IsNotExist(err) {
		return "", root
	}
	Expect(err).ToNot(HaveOccurred())
	return strings.TrimSpace(string(arg)), root
}

var _ = Describe("GRUBConfig in build-iso", func() {
	const grubCfg = "set timeout=10\n"

	newArtifact := func(artifacts *buildv1alpha2.ArtifactSpec) *buildv1alpha2.OSArtifact {
		return &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
			Spec:       buildv1alpha2.OSArtifactSpec{Artifacts: artifacts},
		}
	}

	It("mounts grub.cfg where the build-iso command reads it", func() {
		mounts, _, _ := builderVolumeMounts(newArtifact(&buildv1alpha2.ArtifactSpec{GRUBConfig: grubCfg}))
		Expect(mounts).To(ContainElement(corev1.VolumeMount{
			Name: configVolumeName, MountPath: grubConfigMountPath, SubPath: "grub.cfg",
		}))
	})

	It("hands AuroraBoot an --overlay-iso tree with boot/grub2/grub.cfg", func() {
		artifact := newArtifact(&buildv1alpha2.ArtifactSpec{GRUBConfig: grubCfg})
		_, overlayISO, overlayRootfs := builderVolumeMounts(artifact)
		arg, root := runBuildISOCommand(buildISOCommand(artifact, "amd64", overlayISO, overlayRootfs), grubCfg, nil)

		Expect(arg).To(Equal(root + isoOverlayDir))
		Expect(filepath.Join(arg, "boot/grub2/grub.cfg")).To(BeARegularFile())
		Expect(os.ReadFile(filepath.Join(arg, "boot/grub2/grub.cfg"))).To(BeEquivalentTo(grubCfg))
	})

	It("merges overlayISOVolume content with the grub.cfg, and grubConfig wins", func() {
		artifact := newArtifact(&buildv1alpha2.ArtifactSpec{
			GRUBConfig:       grubCfg,
			OverlayISOVolume: "iso-overlay",
		})
		_, overlayISO, overlayRootfs := builderVolumeMounts(artifact)
		cmd := buildISOCommand(artifact, "amd64", overlayISO, overlayRootfs)
		Expect(strings.Count(cmd, "--overlay-iso")).To(Equal(1))

		arg, _ := runBuildISOCommand(cmd, grubCfg, map[string]string{
			"marker.txt":          "from the volume",
			"boot/grub2/grub.cfg": "the volume's grub.cfg",
		})
		Expect(os.ReadFile(filepath.Join(arg, "marker.txt"))).To(BeEquivalentTo("from the volume"))
		Expect(os.ReadFile(filepath.Join(arg, "boot/grub2/grub.cfg"))).To(BeEquivalentTo(grubCfg))
	})

	It("passes overlayISOVolume straight through without GRUBConfig", func() {
		artifact := newArtifact(&buildv1alpha2.ArtifactSpec{OverlayISOVolume: "iso-overlay"})
		_, overlayISO, overlayRootfs := builderVolumeMounts(artifact)
		arg, root := runBuildISOCommand(buildISOCommand(artifact, "amd64", overlayISO, overlayRootfs), "", nil)
		Expect(arg).To(Equal(root + overlayISOMountPath))
	})
})
