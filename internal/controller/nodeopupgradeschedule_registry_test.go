package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	operatorv1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

type scheduleTestRegistry struct {
	mu             sync.Mutex
	tags           []string
	manifest       []byte
	listStatus     int
	manifestStatus int
	requireAuth    bool
	paginate       bool
	requests       []string
}

func (f *scheduleTestRegistry) configure(fn func(*scheduleTestRegistry)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *scheduleTestRegistry) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.requests...)
}

func (f *scheduleTestRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req.URL.Path)
	if f.requireAuth {
		user, password, ok := req.BasicAuth()
		if !ok || user != "registry-user" || password != "registry-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="test-registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	switch {
	case req.URL.Path == "/v2/":
		w.WriteHeader(http.StatusOK)
	case req.URL.Path == "/v2/kairos/tags/list":
		if f.listStatus != 0 {
			w.WriteHeader(f.listStatus)
			return
		}
		tags := f.tags
		if f.paginate && len(tags) > 1 {
			if req.URL.Query().Get("last") == "" {
				tags = tags[:1]
				w.Header().Set("Link", `</v2/kairos/tags/list?last=page-one>; rel="next"`)
			} else {
				tags = tags[1:]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "kairos", "tags": tags})
	case strings.HasPrefix(req.URL.Path, "/v2/kairos/manifests/"):
		if f.manifestStatus != 0 {
			w.WriteHeader(f.manifestStatus)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		_, _ = w.Write(f.manifest)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

var _ = Describe("NodeOpUpgradeSchedule registry checks", func() {
	var (
		schedule *operatorv1alpha1.NodeOpUpgradeSchedule
		r        *NodeOpUpgradeScheduleReconciler
		fixture  *scheduleTestRegistry
		server   *httptest.Server
		now      time.Time
		request  ctrl.Request
		digest   string
	)

	BeforeEach(func() {
		manifest, err := empty.Image.RawManifest()
		Expect(err).NotTo(HaveOccurred())
		hash, err := empty.Image.Digest()
		Expect(err).NotTo(HaveOccurred())
		digest = hash.String()
		fixture = &scheduleTestRegistry{manifest: manifest, tags: []string{"latest", "3.6.3", "v3.6.10", "3.7.0", "3.6.11-rc.1"}}
		server = httptest.NewServer(fixture)
		DeferCleanup(server.Close)
		now = time.Date(2026, time.November, 25, 10, 0, 30, 0, time.UTC)
		repository := strings.TrimPrefix(server.URL, "http://") + "/kairos"
		schedule = &operatorv1alpha1.NodeOpUpgradeSchedule{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "registry-check-", Namespace: "default"},
			Spec: operatorv1alpha1.NodeOpUpgradeScheduleSpec{
				Schedule: "*/5 * * * *",
				ImagePolicy: &operatorv1alpha1.NodeOpUpgradeScheduleImagePolicy{
					Repository: repository, Semver: ">=3.6.2 <3.7.0",
				},
				UpgradeTemplate: operatorv1alpha1.NodeOpUpgradeSpec{Image: repository + ":v3.6.2"},
			},
		}
		Expect(k8sClient.Create(ctx, schedule)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.DeleteAllOf(ctx, &operatorv1alpha1.NodeOpUpgrade{}, client.InNamespace(schedule.Namespace),
				client.MatchingLabels{scheduleUIDLabel: string(schedule.UID)})).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, schedule))).To(Succeed())
		})
		request = ctrl.Request{NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}}
		r = &NodeOpUpgradeScheduleReconciler{Client: k8sClient, now: func() time.Time { return now }}
	})

	reconcile := func() (ctrl.Result, error) {
		result, err := r.Reconcile(ctx, request)
		Expect(k8sClient.Get(ctx, request.NamespacedName, schedule)).To(Succeed())
		return result, err
	}
	makeDue := func() {
		_, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(fixture.paths()).To(BeEmpty())
		now = schedule.Status.NextScheduledTime.Time
	}
	createSecret := func(secretType corev1.SecretType, contents string) string {
		key := corev1.DockerConfigJsonKey
		if secretType == corev1.SecretTypeDockercfg {
			key = corev1.DockerConfigKey
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "registry-auth-", Namespace: schedule.Namespace},
			Type:       secretType, Data: map[string][]byte{key: []byte(contents)},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, secret))).To(Succeed()) })
		return secret.Name
	}

	It("checks only when due, follows pagination, and pins the highest eligible tag", func() {
		fixture.configure(func(f *scheduleTestRegistry) { f.paginate = true })
		makeDue()
		result, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(scheduleUpgradeRequeue))
		Expect(schedule.Status.AvailableImage).To(Equal(schedule.Spec.ImagePolicy.Repository + ":v3.6.10@" + digest))
		Expect(schedule.Status.LastCheckTime.Time).To(BeTemporally("==", now))
		Expect(schedule.Status.LastScheduledTime.Time).To(BeTemporally("==", now))
		Expect(meta.FindStatusCondition(schedule.Status.Conditions, "UpgradeAvailable").Status).To(Equal(metav1.ConditionTrue))
		Expect(fixture.paths()).To(ContainElement("/v2/kairos/manifests/v3.6.10"))
		Expect(fixture.paths()).NotTo(ContainElement(ContainSubstring("/blobs/")))
		paths := fixture.paths()
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(fixture.paths()).To(Equal(paths))
		Expect(schedule.Status.ActiveUpgradeRef).NotTo(BeNil())
		Expect(schedule.Status.LastAttemptedImage).To(Equal(schedule.Status.AvailableImage))
	})

	It("uses the last successful image rather than the starting template as its baseline", func() {
		schedule.Status.LastSuccessfulImage = schedule.Spec.ImagePolicy.Repository + ":v3.6.10@" + digest
		Expect(k8sClient.Status().Update(ctx, schedule)).To(Succeed())
		makeDue()
		_, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(schedule.Status.AvailableImage).To(BeEmpty())
		Expect(schedule.Status.LastCheckTime).NotTo(BeNil())
		condition := meta.FindStatusCondition(schedule.Status.Conditions, "UpgradeAvailable")
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal("NoNewVersion"))
		Expect(fixture.paths()).NotTo(ContainElement(ContainSubstring("/manifests/")))
	})

	It("clears a previously available image when no matching newer tag remains", func() {
		makeDue()
		_, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(schedule.Status.AvailableImage).NotTo(BeEmpty())
		fixture.configure(func(f *scheduleTestRegistry) { f.tags = []string{"latest", "v3.6.2", "3.5.0", "3.7.0"} })
		upgrade := &operatorv1alpha1.NodeOpUpgrade{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: schedule.Namespace, Name: schedule.Status.ActiveUpgradeRef.Name}, upgrade)).To(Succeed())
		upgrade.Status.Phase = phaseCompleted
		Expect(k8sClient.Status().Update(ctx, upgrade)).To(Succeed())
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		now = schedule.Status.NextScheduledTime.Time
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(schedule.Status.AvailableImage).To(BeEmpty())
		Expect(meta.FindStatusCondition(schedule.Status.Conditions, "UpgradeAvailable").Status).To(Equal(metav1.ConditionFalse))
	})

	DescribeTable("retries failures without advancing the pending execution", func(manifestFailure bool) {
		makeDue()
		due := schedule.Status.NextScheduledTime.DeepCopy()
		fixture.configure(func(f *scheduleTestRegistry) {
			if manifestFailure {
				f.manifestStatus = http.StatusNotFound
			} else {
				f.listStatus = http.StatusForbidden
			}
		})
		_, err := reconcile()
		Expect(err).To(HaveOccurred())
		Expect(schedule.Status.NextScheduledTime).To(Equal(due))
		Expect(schedule.Status.LastScheduledTime).To(BeNil())
		Expect(schedule.Status.LastCheckTime).To(BeNil())
		Expect(schedule.Status.AvailableImage).To(BeEmpty())
		Expect(meta.FindStatusCondition(schedule.Status.Conditions, "Ready").Reason).To(Equal("ImageCheckFailed"))
		Expect(meta.FindStatusCondition(schedule.Status.Conditions, "UpgradeAvailable").Status).To(Equal(metav1.ConditionUnknown))
		fixture.configure(func(f *scheduleTestRegistry) { f.listStatus, f.manifestStatus = 0, 0 })
		now = now.Add(10 * time.Second)
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(schedule.Status.LastScheduledTime).To(Equal(due))
		Expect(schedule.Status.AvailableImage).NotTo(BeEmpty())
	}, Entry("tag listing fails", false), Entry("selected tag disappears", true))

	DescribeTable("authenticates with referenced pull secrets", func(legacy, templateSecret bool) {
		fixture.configure(func(f *scheduleTestRegistry) { f.requireAuth = true })
		secretType := corev1.SecretTypeDockerConfigJson
		auth := base64.StdEncoding.EncodeToString([]byte("registry-user:registry-password"))
		contents := fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, strings.TrimPrefix(server.URL, "http://"), auth)
		if legacy {
			secretType = corev1.SecretTypeDockercfg
			contents = fmt.Sprintf(`{%q:{"auth":%q}}`, strings.TrimPrefix(server.URL, "http://"), auth)
		}
		refs := []corev1.LocalObjectReference{{Name: createSecret(secretType, contents)}}
		if templateSecret {
			schedule.Spec.UpgradeTemplate.ImagePullSecrets = refs
		} else {
			schedule.Spec.ImagePolicy.ImagePullSecrets = refs
		}
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		makeDue()
		_, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(schedule.Status.AvailableImage).NotTo(BeEmpty())
	}, Entry("Docker config secret", false, false), Entry("legacy Docker secret", true, false), Entry("template pull secret", false, true))

	It("does not treat an authentication failure as no available upgrade", func() {
		fixture.configure(func(f *scheduleTestRegistry) { f.requireAuth = true })
		makeDue()
		_, err := reconcile()
		Expect(err).To(HaveOccurred())
		Expect(schedule.Status.LastCheckTime).To(BeNil())
		Expect(meta.FindStatusCondition(schedule.Status.Conditions, "UpgradeAvailable").Status).To(Equal(metav1.ConditionUnknown))
	})

	It("reports a missing pull secret without contacting the registry", func() {
		schedule.Spec.ImagePolicy.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "missing-secret"}}
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		makeDue()
		_, err := reconcile()
		Expect(err).To(MatchError(ContainSubstring("read image pull secret")))
		Expect(fixture.paths()).To(BeEmpty())
		Expect(schedule.Status.LastCheckTime).To(BeNil())
	})

	It("clears the selection when the policy is removed", func() {
		makeDue()
		_, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(schedule.Status.AvailableImage).NotTo(BeEmpty())
		schedule.Spec.ImagePolicy = nil
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(schedule.Status.AvailableImage).To(BeEmpty())
		Expect(meta.FindStatusCondition(schedule.Status.Conditions, "UpgradeAvailable")).To(BeNil())
	})

	It("honors cancellation during registry access", func() {
		policy, err := parseScheduledImagePolicy(schedule)
		Expect(err).NotTo(HaveOccurred())
		cancelled, cancelCheck := context.WithCancel(ctx)
		cancelCheck()
		err = r.checkScheduledImage(cancelled, schedule, policy)
		Expect(err).To(MatchError(ContainSubstring("context canceled")))
		Expect(schedule.Status.LastCheckTime).To(BeNil())
	})
})

var _ = Describe("Scheduled registry credentials", func() {
	var secret *corev1.Secret
	var r *NodeOpUpgradeScheduleReconciler
	BeforeEach(func() {
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "registry-auth", Namespace: "default"},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"ghcr.io/example":{"username":"registry-user","password":"registry-password"}}}`)},
		}
	})
	keychain := func(namespace string, refs ...corev1.LocalObjectReference) (authn.Keychain, error) {
		r = &NodeOpUpgradeScheduleReconciler{
			Client: fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(secret).Build(),
		}
		return r.scheduleRegistryKeychain(ctx, namespace, refs)
	}

	It("uses the library's repository-scoped credential matching", func() {
		chain, err := keychain("default", corev1.LocalObjectReference{Name: secret.Name})
		Expect(err).NotTo(HaveOccurred())
		repository, err := name.NewRepository("ghcr.io/example/kairos")
		Expect(err).NotTo(HaveOccurred())
		auth, err := chain.Resolve(repository)
		Expect(err).NotTo(HaveOccurred())
		config, err := auth.Authorization()
		Expect(err).NotTo(HaveOccurred())
		Expect(config.Username).To(Equal("registry-user"))
		Expect(config.Password).To(Equal("registry-password"))
		other, err := name.NewRepository("other.example/example/kairos")
		Expect(err).NotTo(HaveOccurred())
		auth, err = chain.Resolve(other)
		Expect(err).NotTo(HaveOccurred())
		Expect(auth).To(Equal(authn.Anonymous))
	})

	It("never reads a pull secret from another namespace", func() {
		_, err := keychain("other-namespace", corev1.LocalObjectReference{Name: secret.Name})
		Expect(err).To(MatchError(ContainSubstring("read image pull secret")))
	})

	It("does not include secret content in credential decoding errors", func() {
		secret.Data[corev1.DockerConfigJsonKey] = []byte(`{"auths":{"ghcr.io":{"auth":"private-credential-value"}}}`)
		_, err := keychain("default", corev1.LocalObjectReference{Name: secret.Name})
		Expect(err).To(MatchError(ContainSubstring("invalid registry credentials")))
		Expect(err.Error()).NotTo(ContainSubstring("private-credential-value"))
	})

	It("preserves identity and registry tokens", func() {
		secret.Data[corev1.DockerConfigJsonKey] = []byte(`{"auths":{"ghcr.io":{"identitytoken":"identity","registrytoken":"registry"}}}`)
		chain, err := keychain("default", corev1.LocalObjectReference{Name: secret.Name})
		Expect(err).NotTo(HaveOccurred())
		repository, err := name.NewRepository("ghcr.io/example/kairos")
		Expect(err).NotTo(HaveOccurred())
		auth, err := chain.Resolve(repository)
		Expect(err).NotTo(HaveOccurred())
		config, err := auth.Authorization()
		Expect(err).NotTo(HaveOccurred())
		Expect(config).To(Equal(&authn.AuthConfig{IdentityToken: "identity", RegistryToken: "registry"}))
	})

	DescribeTable("uses anonymous access when secrets contain no Docker registry credentials", func(secretType corev1.SecretType, data map[string][]byte) {
		secret.Type = secretType
		secret.Data = data
		chain, err := keychain("default", corev1.LocalObjectReference{Name: secret.Name})
		Expect(err).NotTo(HaveOccurred())
		repository, err := name.NewRepository("ghcr.io/example/kairos")
		Expect(err).NotTo(HaveOccurred())
		auth, err := chain.Resolve(repository)
		Expect(err).NotTo(HaveOccurred())
		Expect(auth).To(Equal(authn.Anonymous))
	},
		Entry("unsupported secret type", corev1.SecretTypeOpaque, map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"ghcr.io":{"username":"user","password":"password"}}}`)}),
		Entry("missing Docker config", corev1.SecretTypeDockerConfigJson, nil),
		Entry("empty Docker config", corev1.SecretTypeDockerConfigJson, map[string][]byte{corev1.DockerConfigJsonKey: {}}),
	)
})

var _ = Describe("Scheduled image policy parsing", func() {
	DescribeTable("preserves the baseline tag when an image is pinned by digest", func(repository, template, successful, expectedTag string) {
		schedule := &operatorv1alpha1.NodeOpUpgradeSchedule{
			Spec: operatorv1alpha1.NodeOpUpgradeScheduleSpec{
				ImagePolicy:     &operatorv1alpha1.NodeOpUpgradeScheduleImagePolicy{Repository: repository, Semver: ">=3.6.2"},
				UpgradeTemplate: operatorv1alpha1.NodeOpUpgradeSpec{Image: template},
			},
			Status: operatorv1alpha1.NodeOpUpgradeScheduleStatus{LastSuccessfulImage: successful},
		}
		policy, err := parseScheduledImagePolicy(schedule)
		Expect(err).NotTo(HaveOccurred())
		Expect(policy.currentTag).To(Equal(expectedTag))
		selected, err := policy.semver.selectUpgrade([]string{"v3.6.3", "v3.6.4"}, policy.currentTag)
		Expect(err).NotTo(HaveOccurred())
		Expect(selected).To(Equal("v3.6.4"))
	},
		Entry("tagged template", "ghcr.io/example/kairos", "ghcr.io/example/kairos:v3.6.2", "", "v3.6.2"),
		Entry("digest-pinned template", "ghcr.io/example/kairos", "ghcr.io/example/kairos:v3.6.2@sha256:"+strings.Repeat("a", 64), "", "v3.6.2"),
		Entry("successful upgrade takes precedence", "ghcr.io/example/kairos", "ghcr.io/example/kairos:v3.6.2", "ghcr.io/example/kairos:v3.6.3@sha256:"+strings.Repeat("b", 64), "v3.6.3"),
		Entry("registry port", "localhost:5000/kairos", "localhost:5000/kairos:v3.6.2@sha256:"+strings.Repeat("a", 64), "", "v3.6.2"),
		Entry("IPv6 registry", "[2001:db8::1]:5000/kairos", "[2001:db8::1]:5000/kairos:v3.6.2@sha256:"+strings.Repeat("a", 64), "", "v3.6.2"),
	)

	DescribeTable("rejects invalid repository or baseline references", func(repository, current string) {
		schedule := &operatorv1alpha1.NodeOpUpgradeSchedule{Spec: operatorv1alpha1.NodeOpUpgradeScheduleSpec{
			ImagePolicy:     &operatorv1alpha1.NodeOpUpgradeScheduleImagePolicy{Repository: repository, Semver: ">=3.6.2"},
			UpgradeTemplate: operatorv1alpha1.NodeOpUpgradeSpec{Image: current},
		}}
		_, err := parseScheduledImagePolicy(schedule)
		Expect(err).To(HaveOccurred())
	},
		Entry("tag in repository", "ghcr.io/example/kairos:v3.6.2", "ghcr.io/example/kairos:v3.6.2"),
		Entry("scheme in repository", "https://ghcr.io/example/kairos", "ghcr.io/example/kairos:v3.6.2"),
		Entry("digest in repository", "ghcr.io/example/kairos@sha256:abc", "ghcr.io/example/kairos:v3.6.2"),
		Entry("empty repository segment", "ghcr.io/example//kairos", "ghcr.io/example/kairos:v3.6.2"),
		Entry("invalid repository separator", "ghcr.io/example/kairos___image", "ghcr.io/example/kairos:v3.6.2"),
		Entry("mutable current tag", "ghcr.io/example/kairos", "ghcr.io/example/kairos:latest"),
		Entry("missing current tag", "ghcr.io/example/kairos", "ghcr.io/example/kairos"),
		Entry("digest-only baseline", "ghcr.io/example/kairos", "ghcr.io/example/kairos@sha256:"+strings.Repeat("a", 64)),
		Entry("invalid digest", "ghcr.io/example/kairos", "ghcr.io/example/kairos:v3.6.2@sha256:bad"),
		Entry("multiple digest separators", "ghcr.io/example/kairos", "ghcr.io/example/kairos:v3.6.2@sha256:"+strings.Repeat("a", 64)+"@sha256:"+strings.Repeat("b", 64)),
	)
})
