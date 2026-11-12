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

// upgradeSourceImage is a stand-in for a Kairos image. It carries its own
// /etc/hosts, /etc/hostname and /etc/resolv.conf with marker contents, and a
// fake kairos-agent that writes a report of what it finds under its
// `--source dir:<path>` onto the node, instead of installing anything.
//
// /etc/resolv.conf is a regular file on purpose. When an image ships it as a
// symlink, the container runtime follows the link and mounts the pod's file
// at the link target under /run, which the upgrade script then hides by
// mounting the host's /run over it, so a symlink would not show whether the
// pod's file leaks into the upgrade.
const upgradeSourceImage = "kairos-e2e-upgrade-source:e2e"

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
  echo "hosts=$(cat "${src}/etc/hosts" | tr '\n' ' ')"
  echo "hostname=$(cat "${src}/etc/hostname" | tr '\n' ' ')"
  echo "pod_resolv=$(cat /etc/resolv.conf | tr '\n' ' ')"
  echo "source_proc_entries=$(ls -A "${src}/proc" | wc -l)"
} > "${report}"
`

func buildUpgradeSourceImage() {
	ctxDir := GinkgoT().TempDir()
	write := func(rel, content string, mode os.FileMode) {
		p := filepath.Join(ctxDir, rel)
		Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
		Expect(os.WriteFile(p, []byte(content), mode)).To(Succeed())
	}
	write("rootfs/etc/hosts", imageHostsMarker+"\n127.0.0.1 localhost\n", 0o644)
	write("rootfs/etc/hostname", imageHostnameMarker+"\n", 0o644)
	write("rootfs/etc/resolv.conf", imageResolvMarker+"\nnameserver 192.0.2.53\n", 0o644)
	write("rootfs/usr/bin/kairos-agent", fakeKairosAgent, 0o755)
	// RUN steps see the build's own resolv.conf/hosts/hostname mounted over
	// /etc, so the files go in with COPY. busybox has no /run, which the
	// upgrade script mounts the host's /run onto, and every Kairos image has.
	write("Dockerfile", "FROM busybox:latest\nRUN mkdir -p /run\nCOPY rootfs/ /\n", 0o644)

	out, err := exec.Command("docker", "build", "-t", upgradeSourceImage, ctxDir).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	out, err = exec.Command("kind", "load", "docker-image", upgradeSourceImage, "--name", clusterName).CombinedOutput()
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
	const name = "e2e-upgrade-source"
	var kairosNodeName string

	BeforeAll(func() {
		out, err := exec.Command("docker", "exec", kairosNode, "hostname").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		kairosNodeName = strings.TrimSpace(string(out))

		By("building and loading the stand-in upgrade image")
		buildUpgradeSourceImage()
	})

	AfterAll(func() {
		if CurrentSpecReport().Failed() {
			for _, args := range [][]string{
				{"get", "nodeopupgrade", name, "-n", "default", "-o", "yaml"},
				{"get", "nodeop", name, "-n", "default", "-o", "yaml"},
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
		_, _ = utils.Run(exec.Command("kubectl", "delete", "nodeopupgrade", name, "-n", "default",
			"--ignore-not-found=true", "--wait=true"))
		_, _ = utils.Run(exec.Command("docker", "exec", kairosNode, "rm", "-f", upgradeReportPath))
		_, _ = utils.Run(exec.Command("kubectl", "uncordon", kairosNodeName))
	})

	It("copies the image's own resolv.conf, hosts and hostname, not the pod's", func() {
		_, _ = utils.Run(exec.Command("docker", "exec", kairosNode, "rm", "-f", upgradeReportPath))

		// Recovery-only upgrades do not reboot the node, which a kind node
		// cannot survive. Force skips the version check, since the stand-in
		// image has no kairos-release.
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
`, name, upgradeSourceImage, kairosNodeName)

		By("applying the NodeOpUpgrade")
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))

		By("waiting for the NodeOpUpgrade to complete")
		Eventually(func(g Gomega) {
			out, err := exec.Command("kubectl", "get", "nodeopupgrade", name, "-n", "default",
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

		Expect(report["resolv"]).To(HavePrefix(imageResolvMarker),
			"the source must hold the image's resolv.conf, not the pod's")
		Expect(report["hosts"]).To(HavePrefix(imageHostsMarker))
		Expect(report["hostname"]).To(Equal(imageHostnameMarker))

		By("checking that the source has none of the pod's other mounts")
		Expect(report["source_proc_entries"]).To(Equal("0"))

		By("checking that the upgrade container itself kept the cluster DNS settings")
		Expect(report["pod_resolv"]).To(ContainSubstring("svc.cluster.local"))
	})
})
