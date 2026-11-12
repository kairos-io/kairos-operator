package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // dot-imports are standard for ginkgo tests
	. "github.com/onsi/gomega"    //nolint:revive // dot-imports are standard for ginkgo tests

	"github.com/kairos-io/kairos-operator/test/utils"
)

// The stand-in upgrade images are busybox plus a fake kairos-agent that
// writes a report of what it finds under its `--source dir:<path>` onto the
// node, instead of installing anything.
//
// upgradeSourceImage carries its own /etc/hosts, /etc/hostname and
// /etc/resolv.conf with marker contents. /etc/resolv.conf is a regular file
// on purpose. When an image ships it as a symlink, the container runtime
// follows the link and mounts the pod's file at the link target under /run,
// which the upgrade script then hides by mounting the host's /run over it, so
// a symlink would not show whether the pod's file leaks into the upgrade.
//
// upgradeSourceImageBare has none of the three files. Hadron images, for
// example, ship no /etc/resolv.conf and get the symlink created at boot.
//
// upgradeSourceImageLink ships /etc/resolv.conf as a relative symlink to
// systemd-resolved's stub file, which does not exist in the image. The
// runtime follows the link to mount the pod's copy and creates an empty file
// at the target, so the upgrade must keep the link without looking at it.
const (
	upgradeSourceImage     = "kairos-e2e-upgrade-source:e2e"
	upgradeSourceImageBare = "kairos-e2e-upgrade-source-bare:e2e"
	upgradeSourceImageLink = "kairos-e2e-upgrade-source-link:e2e"
	imageResolvLinkTarget  = "../run/systemd/resolve/stub-resolv.conf"
)

const (
	imageHostsMarker    = "# hosts file from the upgrade image"
	imageHostnameMarker = "image-hostname-marker"
	imageResolvMarker   = "# resolv.conf from the upgrade image"
	upgradeReportPath   = "/tmp/kairos-e2e-upgrade-source-report"
)

const fakeKairosAgent = `#!/bin/sh
set -e
src=""
for arg in "$@"; do
  case "$arg" in
    dir:*) src="${arg#dir:}" ;;
  esac
done
report=/host` + upgradeReportPath + `
{
  echo "args=$*"
  echo "source=${src}"
  echo "resolv=$(cat "${src}/etc/resolv.conf" | tr '\n' ' ')"
  echo "resolv_link=$(readlink "${src}/etc/resolv.conf" || true)"
  echo "hosts=$(cat "${src}/etc/hosts" | tr '\n' ' ')"
  echo "hostname=$(cat "${src}/etc/hostname" | tr '\n' ' ')"
  echo "pod_resolv=$(cat /etc/resolv.conf | tr '\n' ' ')"
  echo "source_proc_entries=$(ls -A "${src}/proc" | wc -l)"
} > "${report}"
`

// etcFiles selects the image's /etc/hosts, /etc/hostname and /etc/resolv.conf.
type etcFiles int

const (
	etcFilesMarkers etcFiles = iota
	etcFilesNone
	etcFilesResolvLink
)

func buildUpgradeSourceImage(tag string, files etcFiles) {
	ctxDir := GinkgoT().TempDir()
	write := func(rel, content string, mode os.FileMode) {
		p := filepath.Join(ctxDir, rel)
		Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
		Expect(os.WriteFile(p, []byte(content), mode)).To(Succeed())
	}
	switch files {
	case etcFilesMarkers:
		write("rootfs/etc/hosts", imageHostsMarker+"\n127.0.0.1 localhost\n", 0o644)
		write("rootfs/etc/hostname", imageHostnameMarker+"\n", 0o644)
		write("rootfs/etc/resolv.conf", imageResolvMarker+"\nnameserver 192.0.2.53\n", 0o644)
	case etcFilesResolvLink:
		write("rootfs/etc/hosts", imageHostsMarker+"\n127.0.0.1 localhost\n", 0o644)
		write("rootfs/etc/hostname", imageHostnameMarker+"\n", 0o644)
		Expect(os.Symlink(imageResolvLinkTarget, filepath.Join(ctxDir, "rootfs/etc/resolv.conf"))).To(Succeed())
	case etcFilesNone:
	}
	write("rootfs/usr/bin/kairos-agent", fakeKairosAgent, 0o755)
	// busybox ships none of the three files, so the bare image needs no
	// removal step. COPY keeps a symlink as is. RUN steps see the build's own resolv.conf/hosts/hostname
	// mounted over /etc, so the files go in with COPY. busybox has no /run,
	// which the upgrade script mounts the host's /run onto, and every Kairos
	// image has.
	write("Dockerfile", "FROM busybox:latest\nRUN mkdir -p /run\nCOPY rootfs/ /\n", 0o644)

	out, err := exec.Command("docker", "build", "-t", tag, ctxDir).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	out, err = exec.Command("kind", "load", "docker-image", tag, "--name", clusterName).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
}

func readUpgradeReport() map[string]string {
	out, err := exec.Command("docker", "exec", kairosNode, "cat", upgradeReportPath).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	report := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			report[k] = strings.TrimSpace(v)
		}
	}
	return report
}

var _ = Describe("NodeOpUpgrade E2E", Ordered, func() {
	const (
		name     = "e2e-upgrade-source"
		nameBare = "e2e-upgrade-source-bare"
		nameLink = "e2e-upgrade-source-link"
	)
	var kairosNodeName string

	BeforeAll(func() {
		out, err := exec.Command("docker", "exec", kairosNode, "hostname").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		kairosNodeName = strings.TrimSpace(string(out))

		By("building and loading the stand-in upgrade images")
		buildUpgradeSourceImage(upgradeSourceImage, etcFilesMarkers)
		buildUpgradeSourceImage(upgradeSourceImageBare, etcFilesNone)
		buildUpgradeSourceImage(upgradeSourceImageLink, etcFilesResolvLink)
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			for _, args := range [][]string{
				{"get", "nodeopupgrade,nodeop", "-n", "default", "-o", "yaml"},
				{"get", "jobs,pods", "-n", "default", "-o", "wide"},
				{"get", "events", "-n", "default", "--sort-by=.lastTimestamp"},
			} {
				out, _ := exec.Command("kubectl", args...).CombinedOutput()
				_, _ = fmt.Fprintf(GinkgoWriter, "--- kubectl %s\n%s\n", strings.Join(args, " "), out)
			}
			pods, _ := exec.Command("kubectl", "get", "pods", "-n", "default", "-o", "name").CombinedOutput()
			for _, pod := range strings.Fields(string(pods)) {
				out, _ := exec.Command("kubectl", "logs", "-n", "default", pod, "--all-containers", "--prefix").CombinedOutput()
				_, _ = fmt.Fprintf(GinkgoWriter, "--- kubectl logs %s\n%s\n", pod, out)
			}
		}
		for _, n := range []string{name, nameBare, nameLink} {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "nodeopupgrade", n, "-n", "default",
				"--ignore-not-found=true", "--wait=true"))
		}
		_, _ = utils.Run(exec.Command("docker", "exec", kairosNode, "rm", "-f", upgradeReportPath))
		_, _ = utils.Run(exec.Command("kubectl", "uncordon", kairosNodeName))
	})

	// runUpgrade applies a recovery-only NodeOpUpgrade of image to the Kairos
	// node, waits for it to complete and returns the fake agent's report.
	// Recovery-only upgrades do not reboot the node, which a kind node cannot
	// survive. Force skips the version check, since the stand-in images have
	// no kairos-release.
	runUpgrade := func(nodeOpUpgradeName, image string) map[string]string {
		_, _ = utils.Run(exec.Command("docker", "exec", kairosNode, "rm", "-f", upgradeReportPath))

		manifest := fmt.Sprintf(`apiVersion: operator.kairos.io/v1alpha1
kind: NodeOpUpgrade
metadata:
  name: %s
  namespace: default
spec:
  image: %s
  upgradeActive: false
  upgradeRecovery: true
  force: true
  nodeSelector:
    matchLabels:
      kubernetes.io/hostname: %s
`, nodeOpUpgradeName, image, kairosNodeName)

		By("applying the NodeOpUpgrade")
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))

		By("waiting for the NodeOpUpgrade to complete")
		Eventually(func(g Gomega) {
			out, err := exec.Command("kubectl", "get", "nodeopupgrade", nodeOpUpgradeName, "-n", "default",
				"-o", "json").CombinedOutput()
			g.Expect(err).NotTo(HaveOccurred(), string(out))
			var s struct {
				Status struct {
					Phase string `json:"phase"`
				} `json:"status"`
			}
			g.Expect(json.Unmarshal(out, &s)).To(Succeed())
			if s.Status.Phase == "Failed" {
				StopTrying("the NodeOpUpgrade failed").Now()
			}
			g.Expect(s.Status.Phase).To(Equal("Completed"))
		}, 5*time.Minute, 5*time.Second).Should(Succeed())

		report := readUpgradeReport()
		By(fmt.Sprintf("checking the upgrade source (report: %v)", report))
		Expect(report["args"]).To(HavePrefix("upgrade --recovery --source dir:"))
		Expect(report["source"]).NotTo(BeEmpty())
		Expect(report["source"]).NotTo(Equal("/"))
		return report
	}

	It("copies the image's own resolv.conf, hosts and hostname, not the pod's", func() {
		report := runUpgrade(name, upgradeSourceImage)

		Expect(report["args"]).NotTo(ContainSubstring("--exclude-path"))
		Expect(report["resolv"]).To(HavePrefix(imageResolvMarker),
			"the source must hold the image's resolv.conf, not the pod's")
		Expect(report["hosts"]).To(HavePrefix(imageHostsMarker))
		Expect(report["hostname"]).To(Equal(imageHostnameMarker))

		By("checking that the source has none of the pod's other mounts")
		Expect(report["source_proc_entries"]).To(Equal("0"))

		By("checking that the upgrade container itself kept the cluster DNS settings")
		Expect(report["pod_resolv"]).To(ContainSubstring("svc.cluster.local"))
	})

	It("leaves out the empty files the runtime creates when the image has none", func() {
		report := runUpgrade(nameBare, upgradeSourceImageBare)

		By("checking the source shows the runtime's empty placeholders, not the pod's files")
		Expect(report["resolv"]).To(BeEmpty())
		Expect(report["hosts"]).To(BeEmpty())
		Expect(report["hostname"]).To(BeEmpty())

		By("checking all three are passed to kairos-agent as exclusions")
		Expect(report["args"]).To(HaveSuffix(
			" --exclude-path /etc/resolv.conf --exclude-path /etc/hosts --exclude-path /etc/hostname"))
	})

	It("keeps an image's resolv.conf symlink even though its target is empty in the pod", func() {
		report := runUpgrade(nameLink, upgradeSourceImageLink)

		Expect(report["resolv_link"]).To(Equal(imageResolvLinkTarget))
		By("checking the link's target is the runtime's empty file, the case the check must not follow")
		Expect(report["resolv"]).To(BeEmpty())
		Expect(report["args"]).NotTo(ContainSubstring("--exclude-path"))
	})
})
