/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kairos-io/kairos-operator/test/utils"
)

const (
	// namespace where the project is deployed in
	namespace = "operator-system"
	// serviceAccountName created for the project
	serviceAccountName = "operator-kairos-operator"

	// metricsServiceName is the name of the metrics service of the project
	metricsServiceName = "operator-kairos-operator-metrics-service"

	// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
	metricsRoleBindingName = "operator-metrics-binding"
)

var (
	// Optional Environment Variables:
	// - CERT_MANAGER_INSTALL_SKIP=true: Skips CertManager installation during test setup.
	// These variables are useful if CertManager is already installed, avoiding
	// re-installation and conflicts.
	skipCertManagerInstall = os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true"
	// isCertManagerAlreadyInstalled will be set true when CertManager CRDs be found on the cluster
	isCertManagerAlreadyInstalled = false

	// projectImage is the name of the image which will be build and loaded
	// with the code source changes to be tested.
	projectImage = "quay.io/kairos/operator:v0.0.1"
	// nodeLabelerImage is the name of the node-labeler image
	nodeLabelerImage = "quay.io/kairos/operator-node-labeler:v0.0.1"

	kubeconfig        string
	clusterName       string
	clientset         *kubernetes.Clientset
	controllerPodName string
	kairosNode        string
)

// TestE2E runs the end-to-end (e2e) test suite for the project. These tests execute in an isolated,
// temporary environment to validate project changes with the purposed to be used in CI jobs.
// The default setup requires Kind, builds/loads the Manager Docker image locally, and installs
// CertManager.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting operator integration test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	kubeconfig, clusterName = createCluster()
	makeNodeBeKairos()
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())
	clientset, err = kubernetes.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred())

	os.Setenv("KUBECONFIG", kubeconfig)

	// Build and load both images
	Expect(buildAndLoadImages(clusterName)).To(Succeed(), "Failed to build and load images")

	if !skipCertManagerInstall {
		By("checking if cert manager is installed already")
		isCertManagerAlreadyInstalled = utils.IsCertManagerCRDsInstalled()
		if !isCertManagerAlreadyInstalled {
			_, _ = fmt.Fprintf(GinkgoWriter, "Installing CertManager...\n")
			Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: CertManager is already installed. Skipping installation...\n")
		}
	}

	installOperator()
})

var _ = AfterSuite(func() {
	By("deleting the kind cluster")
	exec.Command("kind", "delete", "cluster", "--name", clusterName).Run()
})

func createCluster() (string, string) {
	// Create a temporary directory for the kubeconfig
	tmpDir, err := os.MkdirTemp("", "kairos-operator-e2e-*")
	Expect(err).NotTo(HaveOccurred())

	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")

	// Generate a unique cluster name using timestamp
	clusterName := fmt.Sprintf("kairos-operator-e2e-%d", time.Now().UnixNano())

	// Create the kind cluster with the custom kubeconfig path
	cmd := exec.Command("kind", "create", "cluster",
		"--name", clusterName,
		"--config", "kind-2node.yaml",
		"--kubeconfig", kubeconfigPath)
	_, err = cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred())

	// Wait for nodes to be ready
	By("waiting for nodes to be ready")
	cmd = exec.Command("kubectl", "--kubeconfig", kubeconfigPath, "wait", "--for=condition=Ready", "nodes", "--all", "--timeout=5m")
	_, err = cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred())

	return kubeconfigPath, clusterName
}

func getControllerPodName() {
	cmd := exec.Command("kubectl", "get",
		"pods", "-l", "app.kubernetes.io/name=kairos-operator,app.kubernetes.io/component=operator",
		"-o", "go-template={{ range .items }}"+
			"{{ if not .metadata.deletionTimestamp }}"+
			"{{ .metadata.name }}"+
			"{{ \"\\n\" }}{{ end }}{{ end }}",
		"-n", namespace,
	)

	podOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve kairos-operator pod information")
	podNames := utils.GetNonEmptyLines(podOutput)

	// Add detailed error reporting
	if len(podNames) != 1 {
		By("Fetching all pods in namespace for debugging")
		allPodsCmd := exec.Command("kubectl", "get", "pods", "-n", namespace, "-o", "wide")
		allPodsOutput, _ := utils.Run(allPodsCmd)
		Fail(fmt.Sprintf("Expected exactly 1 operator pod running, but found %d pods. Pod output: %s\nAll pods in namespace:\n%s",
			len(podNames), podOutput, allPodsOutput))
	}

	controllerPodName = podNames[0]
	Expect(controllerPodName).To(ContainSubstring("kairos-operator"))
}

func waitUntilControllerIsRunning() {
	By("validating that the kairos-operator pod is running as expected")
	Eventually(func() bool {
		pods, err := clientset.CoreV1().Pods("operator-system").List(context.TODO(), metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name=kairos-operator,app.kubernetes.io/component=operator",
		})
		if err != nil {
			return false
		}
		if len(pods.Items) == 0 {
			return false
		}
		pod := pods.Items[0]
		return pod.Status.Phase == corev1.PodRunning &&
			len(pod.Status.ContainerStatuses) > 0 &&
			pod.Status.ContainerStatuses[0].Ready
	}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Operator pod should be running")
}

func installOperator() {
	By("deploying the operator and node labeler")
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "apply", "-k", "config/default")
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))

	By("waiting for the service account to be created")
	Eventually(func() error {
		cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "get", "serviceaccount", serviceAccountName, "-n", namespace)
		_, err := cmd.CombinedOutput()
		return err
	}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Service account should be created")

	By("getting the controller pod name")
	getControllerPodName()
	By("waiting the controller pod to be running")
	waitUntilControllerIsRunning()
}

// buildAndLoadImages builds and loads both the operator and node-labeler images into the kind cluster
func buildAndLoadImages(clusterName string) error {
	// Build and load the operator image
	By("building the manager(Operator) image")
	cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectImage))
	_, err := utils.Run(cmd)
	if err != nil {
		return fmt.Errorf("failed to build the manager(Operator) image: %w", err)
	}

	By("loading the manager(Operator) image on Kind")
	err = utils.LoadImageToKindClusterWithName(clusterName, projectImage)
	if err != nil {
		return fmt.Errorf("failed to load the manager(Operator) image into Kind: %w", err)
	}

	// Build and load the node-labeler image
	By("building the node-labeler image")
	cmd = exec.Command("docker", "build", "-t", nodeLabelerImage, "-f", "Dockerfile.node-labeler", ".")
	_, err = utils.Run(cmd)
	if err != nil {
		return fmt.Errorf("failed to build the node-labeler image: %w", err)
	}

	By("loading the node-labeler image on Kind")
	err = utils.LoadImageToKindClusterWithName(clusterName, nodeLabelerImage)
	if err != nil {
		return fmt.Errorf("failed to load the node-labeler image into Kind: %w", err)
	}

	return nil
}

// The labeler simply looks for /etc/kairos-release to tell if the Node is a
// Kairos one. We create this file in one of the Nodes.
func makeNodeBeKairos() {
	By("injecting /etc/kairos-release into one node")
	out, err := exec.Command("kind", "get", "nodes", "--name", clusterName).CombinedOutput()
	Expect(err).NotTo(HaveOccurred())
	nodes := strings.Fields(string(out))
	Expect(len(nodes)).To(Equal(2))
	// Inject kairos-release into the first node
	cmd := exec.Command("docker", "exec", nodes[0], "bash", "-c", "echo 'kairos' > /etc/kairos-release")
	out, err = cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))

	kairosNode = nodes[0]
}
