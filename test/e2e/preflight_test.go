package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // dot-imports are standard for ginkgo tests
	. "github.com/onsi/gomega"    //nolint:revive // dot-imports are standard for ginkgo tests
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kairos-io/kairos-operator/test/utils"
)

// nodeOpStatus is a tiny subset of NodeOp.status that we care about in these
// tests. We parse the JSON ourselves rather than importing the v1alpha1
// package, to keep this file's coupling to the controller minimal.
type nodeOpStatus struct {
	Status struct {
		Phase        string `json:"phase"`
		NodeStatuses map[string]struct {
			Phase   string `json:"phase"`
			JobName string `json:"jobName"`
			Message string `json:"message"`
		} `json:"nodeStatuses"`
	} `json:"status"`
}

var _ = Describe("NodeOp Preflight E2E", func() {
	var kairosNodeName string

	BeforeEach(func() {
		out, err := exec.Command("docker", "exec", kairosNode, "hostname").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		kairosNodeName = strings.TrimSpace(string(out))

		// Defensive: clear any leftover cordon from a prior failed run before
		// this spec starts asserting anything about node schedulability.
		_, _ = utils.Run(exec.Command("kubectl", "uncordon", kairosNodeName))
	})

	AfterEach(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "nodeop", "--all", "-n", "default",
			"--ignore-not-found=true", "--wait=true"))

		// Best-effort uncordon in case a spec failed midway through the cordon/drain phase.
		_, _ = utils.Run(exec.Command("kubectl", "uncordon", kairosNodeName))
	})

	applyNodeOp := func(manifest string) {
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
	}

	getNodeOp := func(name string) nodeOpStatus {
		out, err := exec.Command("kubectl", "get", "nodeop", name, "-n", "default", "-o", "json").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		var s nodeOpStatus
		Expect(json.Unmarshal(out, &s)).To(Succeed())
		return s
	}

	It("skips the node when preflight writes a reason to /dev/termination-log", func() {
		const name = "e2e-preflight-skip"
		manifest := fmt.Sprintf(`apiVersion: operator.kairos.io/v1alpha1
kind: NodeOp
metadata:
  name: %s
  namespace: default
spec:
  image: busybox:latest
  command:
  - sh
  - -c
  - 'echo MAIN JOB SHOULD NOT RUN; exit 1'
  cordon: true
  nodeSelector:
    matchLabels:
      kubernetes.io/hostname: %s
  preflight:
    command:
    - sh
    - -c
    - 'echo "e2e preflight skip reason" > /dev/termination-log'
`, name, kairosNodeName)

		By("applying the NodeOp")
		applyNodeOp(manifest)

		By("waiting for NodeStatus to report Completed with the skip reason")
		Eventually(func(g Gomega) {
			status := getNodeOp(name).Status.NodeStatuses[kairosNodeName]
			g.Expect(status.Phase).To(Equal("Completed"))
			g.Expect(status.JobName).To(BeEmpty(), "no Job should have been created for a skipped node")
			g.Expect(status.Message).To(ContainSubstring("Skipped by preflight"))
			g.Expect(status.Message).To(ContainSubstring("e2e preflight skip reason"))
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying no Job exists for this NodeOp")
		jobs, err := clientset.BatchV1().Jobs("default").List(context.TODO(), metav1.ListOptions{
			LabelSelector: "kairos.io/nodeop=" + name,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(jobs.Items).To(BeEmpty())

		By("verifying the Kairos node was NOT cordoned (preflight ran before cordon/drain)")
		node, err := clientset.CoreV1().Nodes().Get(context.TODO(), kairosNodeName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(node.Spec.Unschedulable).To(BeFalse(),
			"preflight reported skip, so the node must never have been cordoned")

		By("verifying the preflight Pod was cleaned up after the verdict was recorded")
		Eventually(func() int {
			pods, err := clientset.CoreV1().Pods("default").List(context.TODO(), metav1.ListOptions{
				LabelSelector: "kairos.io/preflight=true,kairos.io/nodeop=" + name,
			})
			if err != nil {
				return -1
			}
			return len(pods.Items)
		}, 2*time.Minute, 5*time.Second).Should(Equal(0))
	})

	It("proceeds with the main Job when preflight exits silently (no termination-log write)", func() {
		const name = "e2e-preflight-proceed"
		// Note: deliberately NOT setting cordon: true here. The
		// cordon/drain/uncordon mechanism is exercised by unit tests; this
		// spec focuses purely on "preflight proceeded → main Job ran" and
		// avoids leaving the node in a cordoned state if anything fails
		// mid-spec, which would leak into other tests in the suite.
		manifest := fmt.Sprintf(`apiVersion: operator.kairos.io/v1alpha1
kind: NodeOp
metadata:
  name: %s
  namespace: default
spec:
  image: busybox:latest
  command:
  - sh
  - -c
  - 'echo MAIN JOB RAN; exit 0'
  nodeSelector:
    matchLabels:
      kubernetes.io/hostname: %s
  preflight:
    command:
    - sh
    - -c
    - 'echo no skip needed; exit 0'
`, name, kairosNodeName)

		By("applying the NodeOp")
		applyNodeOp(manifest)

		By("waiting for the main Job to be created with this NodeOp's label")
		var jobName string
		Eventually(func(g Gomega) {
			jobs, err := clientset.BatchV1().Jobs("default").List(context.TODO(), metav1.ListOptions{
				LabelSelector: "kairos.io/nodeop=" + name,
			})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(jobs.Items).To(HaveLen(1),
				"once preflight reported proceed, exactly one main Job should exist")
			jobName = jobs.Items[0].Name
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for the main Job to complete successfully")
		Eventually(func(g Gomega) {
			job, err := clientset.BatchV1().Jobs("default").Get(context.TODO(), jobName, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(job.Status.Succeeded).To(BeNumerically(">", 0))
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying NodeStatus rolls up to Completed and references the actual Job name")
		Eventually(func(g Gomega) {
			status := getNodeOp(name).Status.NodeStatuses[kairosNodeName]
			g.Expect(status.Phase).To(Equal("Completed"))
			g.Expect(status.JobName).To(Equal(jobName),
				"NodeStatus.JobName should match the Job that actually ran")
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})
})
