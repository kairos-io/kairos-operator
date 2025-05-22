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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kairos-io/kairos-operator/test/utils"
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

	kubeconfig  string
	clusterName string
	clientset   *kubernetes.Clientset
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

var _ = BeforeSuite(func() {
	kubeconfig, clusterName = createCluster()
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())
	clientset, err = kubernetes.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred())

	os.Setenv("KUBECONFIG", kubeconfig)

	// Build and load both images
	Expect(buildAndLoadImages(clusterName)).To(Succeed(), "Failed to build and load images")

	// The tests-e2e are intended to run on a temporary cluster that is created and destroyed for testing.
	// To prevent errors when tests run in environments with CertManager already installed,
	// we check for its presence before execution.
	// Setup CertManager before the suite if not skipped and if not already installed
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
})

var _ = AfterSuite(func() {
	By("deleting the kind cluster")
	exec.Command("kind", "delete", "cluster", "--name", clusterName).Run()
})

func createCluster() (string, string) {
	// Create a temporary directory for the kubeconfig
	tmpDir, err := os.MkdirTemp("", "node-labeler-e2e-*")
	Expect(err).NotTo(HaveOccurred())

	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")

	// Generate a unique cluster name using timestamp
	clusterName := fmt.Sprintf("node-labeler-e2e-%d", time.Now().UnixNano())

	// Create the kind cluster with the custom kubeconfig path
	cmd := exec.Command("kind", "create", "cluster",
		"--name", clusterName,
		"--config", "../../test/e2e/kind-2node.yaml",
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
