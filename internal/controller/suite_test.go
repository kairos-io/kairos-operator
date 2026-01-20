package controller

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	err = kairosiov1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = buildv1alpha2.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}

// Helper function to mark a job as successfully completed with proper conditions
func markJobAsCompleted(ctx context.Context, k8sClient client.Client, job *batchv1.Job) error {
	now := metav1.Now()

	// Set the job status fields
	job.Status.Succeeded = 1
	job.Status.Active = 0
	job.Status.Failed = 0
	job.Status.StartTime = &now
	job.Status.CompletionTime = &now

	// Add both SuccessCriteriaMet and JobComplete conditions (Kubernetes requires both)
	job.Status.Conditions = []batchv1.JobCondition{
		{
			Type:               batchv1.JobSuccessCriteriaMet,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "SuccessCriteriaMet",
			Message:            "Job success criteria met",
		},
		{
			Type:               batchv1.JobComplete,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "JobCompleted",
			Message:            "Job completed successfully",
		},
	}

	return k8sClient.Status().Update(ctx, job)
}

// randStringRunes generates a random string of specified length
func randStringRunes(n int) string {
	var letterRunes = []rune("abcdefghijklmnopqrstuvwxyz")
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

// createRandomNamespace creates a random namespace for testing
func createRandomNamespace(clientset *kubernetes.Clientset) string {
	name := randStringRunes(10)
	_, err := clientset.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}, metav1.CreateOptions{})
	Expect(err).ToNot(HaveOccurred())

	// Create default service account to avoid pod creation errors
	_, err = clientset.CoreV1().ServiceAccounts(name).Create(context.Background(), &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: name,
		},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).ToNot(HaveOccurred())
	}

	return name
}

// deleteNamepace deletes a namespace and waits for it to be fully deleted
func deleteNamepace(clientset *kubernetes.Clientset, name string) {
	err := clientset.CoreV1().Namespaces().Delete(context.Background(), name, metav1.DeleteOptions{})
	Expect(err).ToNot(HaveOccurred())

	// Wait for the namespace to be fully deleted to ensure clean test isolation
	Eventually(func() bool {
		_, err := clientset.CoreV1().Namespaces().Get(context.Background(), name, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, 2*time.Minute, 1*time.Second).Should(BeTrue(), "namespace should be deleted")
}

// Helper function to mark a job as failed with proper conditions
func markJobAsFailed(ctx context.Context, k8sClient client.Client, job *batchv1.Job) error {
	now := metav1.Now()

	// Set the job status fields
	job.Status.Succeeded = 0
	job.Status.Active = 0
	job.Status.Failed = 1
	job.Status.StartTime = &now
	// CompletionTime is not set for failed jobs typically

	// Add both FailureTarget and JobFailed conditions (Kubernetes requires both)
	job.Status.Conditions = []batchv1.JobCondition{
		{
			Type:               batchv1.JobFailureTarget,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "FailureTarget",
			Message:            "Job failure target reached",
		},
		{
			Type:               batchv1.JobFailed,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "JobFailed",
			Message:            "Job failed",
		},
	}

	return k8sClient.Status().Update(ctx, job)
}
