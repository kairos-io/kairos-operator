package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeDiskAuroraboot mimics what AuroraBoot v0.24.0 does with state_dir for
// the disk outputs: it recreates <state_dir>/temp-rootfs, works in it for a
// while, writes kairos-<name>.raw, and for vhd/gce converts the single
// kairos-*.raw it finds. A run that sees its working copy disappear, or more
// than one raw image, fails like the real one does.
const fakeDiskAuroraboot = `#!/bin/bash
set -e
sd=""; kind=""
for a in "$@"; do
  case "$a" in
    state_dir=*) sd="${a#state_dir=}" ;;
    disk.raw=true) kind=raw ;;
    disk.vhd=true) kind=vhd ;;
    disk.gce=true) kind=gce ;;
  esac
done
rm -rf "$sd/temp-rootfs"
mkdir -p "$sd/temp-rootfs"
echo $$ > "$sd/temp-rootfs/owner"
sleep 0.5
[ "$(cat "$sd/temp-rootfs/owner" 2>/dev/null)" = "$$" ] || { echo "temp-rootfs taken over" >&2; exit 1; }
echo raw > "$sd/kairos-$kind.raw"
rm -rf "$sd/temp-rootfs"
raws=$(ls "$sd"/kairos-*.raw | wc -l)
case "$kind" in
  vhd) [ "$raws" = 1 ] || { echo "want one raw, got $raws" >&2; exit 1; }
       mv "$sd"/kairos-*.raw "$sd/kairos-vhd.vhd" ;;
  gce) [ "$raws" = 1 ] || { echo "want one raw, got $raws" >&2; exit 1; }
       tar -czf "$sd/kairos-gce.raw.gce.tar.gz" -C "$sd" kairos-gce.raw && rm "$sd/kairos-gce.raw" ;;
esac
`

var _ = Describe("disk image containers running in parallel", func() {
	It("give each of cloud, azure and gce its own state_dir", func() {
		artifact := &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{Name: "myart"},
			Spec: buildv1alpha2.OSArtifactSpec{Artifacts: &buildv1alpha2.ArtifactSpec{
				CloudImage: true, AzureImage: true, GCEImage: true,
			}},
		}
		artifacts := artifact.Spec.Artifacts
		cmds := map[string]string{
			"cloud": buildCloudImageCmd(artifact, "amd64", artifacts),
			"azure": buildAzureCmd(artifact, "amd64", artifacts),
			"gce":   buildGCECmd(artifact, "amd64", artifacts),
		}

		root := GinkgoT().TempDir()
		bin := filepath.Join(root, "bin")
		Expect(os.MkdirAll(bin, 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "artifacts"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(bin, "auroraboot"), []byte(fakeDiskAuroraboot), 0o755)).To(Succeed())

		var wg sync.WaitGroup
		var mu sync.Mutex
		failures := map[string]string{}
		for name, cmd := range cmds {
			cmd = strings.ReplaceAll(cmd, "/artifacts", root+"/artifacts")
			wg.Add(1)
			go func(name, cmd string) {
				defer GinkgoRecover()
				defer wg.Done()
				c := exec.Command("/bin/bash", "-ce", cmd)
				c.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
				if out, err := c.CombinedOutput(); err != nil {
					mu.Lock()
					failures[name] = string(out)
					mu.Unlock()
				}
			}(name, cmd)
		}
		wg.Wait()
		Expect(failures).To(BeEmpty())

		entries, err := os.ReadDir(filepath.Join(root, "artifacts"))
		Expect(err).ToNot(HaveOccurred())
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		Expect(names).To(ConsistOf(
			artifact.ArtifactNameFor(buildv1alpha2.OSArtifactKindCloud)+".raw",
			artifact.ArtifactNameFor(buildv1alpha2.OSArtifactKindAzure)+".vhd",
			artifact.ArtifactNameFor(buildv1alpha2.OSArtifactKindGCE)+".gce.tar.gz",
		), "per-format state dirs must be cleaned up and only the images left")
	})
})
