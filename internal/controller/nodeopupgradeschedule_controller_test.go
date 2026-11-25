package controller

import (
	"time"

	"github.com/distribution/reference"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorv1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

var _ = Describe("NodeOpUpgradeSchedule Controller", func() {
	var (
		schedule *operatorv1alpha1.NodeOpUpgradeSchedule
		r        *NodeOpUpgradeScheduleReconciler
		now      time.Time
		request  ctrl.Request
	)

	BeforeEach(func() {
		now = time.Date(2026, time.November, 25, 10, 0, 30, 0, time.UTC)
		schedule = &operatorv1alpha1.NodeOpUpgradeSchedule{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "cron-", Namespace: "default"},
			Spec: operatorv1alpha1.NodeOpUpgradeScheduleSpec{
				Schedule:        "*/5 * * * *",
				UpgradeTemplate: operatorv1alpha1.NodeOpUpgradeSpec{Image: "example.com/kairos:v3.6.2"},
			},
		}
		Expect(k8sClient.Create(ctx, schedule)).To(Succeed())
		request = ctrl.Request{NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}}
		r = &NodeOpUpgradeScheduleReconciler{Client: k8sClient, now: func() time.Time { return now }}
	})

	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(ctx, &operatorv1alpha1.NodeOpUpgrade{}, client.InNamespace(schedule.Namespace),
			client.MatchingLabels{scheduleUIDLabel: string(schedule.UID)})).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, schedule))).To(Succeed())
	})

	reconcile := func() ctrl.Result {
		result, err := r.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, request.NamespacedName, schedule)).To(Succeed())
		return result
	}

	It("waits until the next tick, records it once, and waits again", func() {
		Expect(reconcile().RequeueAfter).To(Equal(4*time.Minute + 30*time.Second))
		Expect(schedule.Status.NextScheduledTime.Time).To(BeTemporally("==", now.Add(4*time.Minute+30*time.Second)))
		Expect(schedule.Status.LastScheduledTime).To(BeNil())
		Expect(schedule.Status.ObservedGeneration).To(Equal(schedule.Generation))

		now = now.Add(time.Minute)
		version := schedule.ResourceVersion
		Expect(reconcile().RequeueAfter).To(Equal(3*time.Minute + 30*time.Second))
		Expect(schedule.ResourceVersion).To(Equal(version))
		Expect(schedule.Status.LastScheduledTime).To(BeNil())

		now = schedule.Status.NextScheduledTime.Time
		Expect(reconcile().RequeueAfter).To(Equal(scheduleUpgradeRequeue))
		Expect(schedule.Status.LastScheduledTime.Time).To(BeTemporally("==", now))
		upgrade := &operatorv1alpha1.NodeOpUpgrade{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: schedule.Namespace, Name: schedule.Status.ActiveUpgradeRef.Name}, upgrade)).To(Succeed())
		upgrade.Status.Phase = phaseCompleted
		Expect(k8sClient.Status().Update(ctx, upgrade)).To(Succeed())
		Expect(reconcile().RequeueAfter).To(Equal(5 * time.Minute))
		version = schedule.ResourceVersion
		Expect(reconcile().RequeueAfter).To(Equal(5 * time.Minute))
		Expect(schedule.ResourceVersion).To(Equal(version))
		Expect(schedule.Status.LastCheckTime).To(BeNil())
		Expect(schedule.Status.ActiveUpgradeRef).To(BeNil())
	})

	It("continues after a restart and processes only one overdue tick", func() {
		reconcile()
		due := schedule.Status.NextScheduledTime.DeepCopy()
		now = now.Add(24 * time.Hour)
		r = &NodeOpUpgradeScheduleReconciler{Client: k8sClient, now: func() time.Time { return now }}
		Expect(reconcile().RequeueAfter).To(Equal(scheduleUpgradeRequeue))
		Expect(schedule.Status.LastScheduledTime).To(Equal(due))
		version := schedule.ResourceVersion
		reconcile()
		Expect(schedule.ResourceVersion).To(Equal(version))
	})

	It("stops when the resource is deleted before the tick", func() {
		reconcile()
		Expect(k8sClient.Delete(ctx, schedule)).To(Succeed())
		now = now.Add(5 * time.Minute)
		result, err := r.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))
	})

	It("stops for a resource waiting for deletion", func() {
		schedule.Finalizers = []string{"test.kairos.io/hold"}
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Get(ctx, request.NamespacedName, schedule)).To(Succeed())
			schedule.Finalizers = nil
			Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		})
		reconcile()
		Expect(k8sClient.Delete(ctx, schedule)).To(Succeed())
		now = now.Add(5 * time.Minute)
		Expect(reconcile()).To(Equal(ctrl.Result{}))
		Expect(schedule.Status.LastScheduledTime).To(BeNil())
	})

	It("pauses and resumes without replaying suspended ticks", func() {
		reconcile()
		schedule.Spec.Suspend = true
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		now = now.Add(5 * time.Minute)
		Expect(reconcile()).To(Equal(ctrl.Result{}))
		Expect(schedule.Status.NextScheduledTime).To(BeNil())
		Expect(schedule.Status.LastScheduledTime).To(BeNil())
		Expect(meta.FindStatusCondition(schedule.Status.Conditions, "Ready").Reason).To(Equal("Suspended"))

		schedule.Spec.Suspend = false
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		Expect(reconcile().RequeueAfter).To(Equal(4*time.Minute + 30*time.Second))
		Expect(schedule.Status.LastScheduledTime).To(BeNil())
	})

	It("recalculates an edited schedule and ignores the old wake-up", func() {
		reconcile()
		schedule.Spec.Schedule = "0 * * * *"
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		Expect(reconcile().RequeueAfter).To(Equal(59*time.Minute + 30*time.Second))
		now = now.Add(5 * time.Minute)
		Expect(reconcile().RequeueAfter).To(Equal(54*time.Minute + 30*time.Second))
		Expect(schedule.Status.LastScheduledTime).To(BeNil())
	})

	It("recalculates after the time zone changes", func() {
		schedule.Spec.Schedule = "0 4 * * *"
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		reconcile()
		schedule.Spec.TimeZone = "America/Denver"
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		Expect(reconcile().RequeueAfter).To(Equal(59*time.Minute + 30*time.Second))
		Expect(schedule.Status.LastScheduledTime).To(BeNil())
	})

	DescribeTable("reports invalid schedules without requeueing", func(expression, zone string) {
		reconcile()
		schedule.Spec.Schedule = expression
		schedule.Spec.TimeZone = zone
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		Expect(reconcile()).To(Equal(ctrl.Result{}))
		Expect(schedule.Status.NextScheduledTime).To(BeNil())
		condition := meta.FindStatusCondition(schedule.Status.Conditions, "Ready")
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal("InvalidSchedule"))
	},
		Entry("out of range", "60 * * * *", "UTC"),
		Entry("six fields", "0 * * * * *", "UTC"),
		Entry("descriptor", "@hourly", "UTC"),
		Entry("inline time zone", "CRON_TZ=UTC 0 * * * *", "UTC"),
		Entry("inline time zone without fields", "CRON_TZ=UTC", "UTC"),
		Entry("invalid time zone", "* * * * *", "Unknown/Zone"),
		Entry("impossible date", "0 0 31 2 *", "UTC"),
	)

	It("recovers when an invalid schedule is corrected", func() {
		schedule.Spec.Schedule = "60 * * * *"
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		reconcile()
		schedule.Spec.Schedule = "*/5 * * * *"
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		Expect(reconcile().RequeueAfter).To(Equal(4*time.Minute + 30*time.Second))
		Expect(meta.FindStatusCondition(schedule.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionTrue))
	})

	It("keeps the repository validation pattern aligned with the upstream grammar", func() {
		var pattern string
		for _, crd := range testEnv.CRDs {
			if crd.Name != "nodeopupgradeschedules.operator.kairos.io" {
				continue
			}
			for _, version := range crd.Spec.Versions {
				if version.Name == "v1alpha1" {
					pattern = version.Schema.OpenAPIV3Schema.Properties["spec"].Properties["imagePolicy"].Properties["repository"].Pattern
				}
			}
		}
		Expect(pattern).To(Equal("^" + reference.NameRegexp.String() + "$"))
	})

	DescribeTable("validates repository names at admission", func(repository string, valid bool) {
		schedule.Spec.ImagePolicy = &operatorv1alpha1.NodeOpUpgradeScheduleImagePolicy{
			Repository: repository, Semver: ">=3.6.2 <3.7.0",
		}
		err := k8sClient.Update(ctx, schedule)
		if valid {
			Expect(err).NotTo(HaveOccurred())
		} else {
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("spec.imagePolicy.repository"))
		}
	},
		Entry("registry and path", "ghcr.io/example/kairos", true),
		Entry("registry port", "registry.example:5000/team/kairos", true),
		Entry("localhost", "localhost:5000/kairos", true),
		Entry("IPv4 registry", "192.168.1.10:5000/team/kairos", true),
		Entry("IPv6 registry", "[2001:db8::1]:5000/team/kairos", true),
		Entry("Docker Hub namespace", "example/kairos", true),
		Entry("Docker Hub short name", "kairos", true),
		Entry("valid separators", "ghcr.io/team/my__kairos--os.image", true),
		Entry("uppercase registry", "REGISTRY.EXAMPLE/team/kairos", true),
		Entry("empty", "", false),
		Entry("spaces", "   ", false),
		Entry("embedded space", "ghcr.io/example/kai ros", false),
		Entry("leading whitespace", " ghcr.io/example/kairos", false),
		Entry("trailing newline", "ghcr.io/example/kairos\n", false),
		Entry("HTTPS scheme", "https://ghcr.io/example/kairos", false),
		Entry("OCI scheme", "oci://ghcr.io/example/kairos", false),
		Entry("tag suffix", "ghcr.io/example/kairos:v3.6.2", false),
		Entry("digest suffix", "ghcr.io/example/kairos@sha256:abcdef", false),
		Entry("uppercase repository path", "ghcr.io/Example/kairos", false),
		Entry("empty path segment", "ghcr.io/example//kairos", false),
		Entry("leading slash", "/ghcr.io/example/kairos", false),
		Entry("trailing slash", "ghcr.io/example/kairos/", false),
		Entry("invalid separator", "ghcr.io/example/kairos___image", false),
		Entry("non-numeric port", "registry.example:port/kairos", false),
		Entry("missing port", "registry.example:/kairos", false),
		Entry("unbracketed IPv6", "2001:db8::1/kairos", false),
		Entry("credentials", "user:password@ghcr.io/example/kairos", false),
		Entry("query", "ghcr.io/example/kairos?tag=latest", false),
		Entry("fragment", "ghcr.io/example/kairos#latest", false),
	)

	DescribeTable("rejects blank semver policies at admission", func(expression string) {
		schedule.Spec.ImagePolicy = &operatorv1alpha1.NodeOpUpgradeScheduleImagePolicy{
			Repository: "example.com/kairos", Semver: expression,
		}
		err := k8sClient.Update(ctx, schedule)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	},
		Entry("empty", ""),
		Entry("spaces", "   "),
		Entry("tabs and newlines", "\t\n"),
	)

	It("reports invalid semver policies and resumes after correction", func() {
		reconcile()
		schedule.Spec.ImagePolicy = &operatorv1alpha1.NodeOpUpgradeScheduleImagePolicy{
			Repository: "example.com/kairos", Semver: "not-a-constraint",
		}
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		now = now.Add(5 * time.Minute)
		Expect(reconcile()).To(Equal(ctrl.Result{}))
		Expect(schedule.Status.NextScheduledTime).To(BeNil())
		Expect(schedule.Status.LastScheduledTime).To(BeNil())
		condition := meta.FindStatusCondition(schedule.Status.Conditions, "Ready")
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal("InvalidImagePolicy"))
		Expect(condition.Message).To(ContainSubstring("invalid semver constraint"))
		Expect(condition.ObservedGeneration).To(Equal(schedule.Generation))

		schedule.Spec.ImagePolicy.Semver = "~3.6.2"
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		Expect(reconcile().RequeueAfter).To(Equal(4*time.Minute + 30*time.Second))
		Expect(meta.FindStatusCondition(schedule.Status.Conditions, "Ready").Status).To(Equal(metav1.ConditionTrue))
		Expect(schedule.Status.LastScheduledTime).To(BeNil())
	})

	It("accepts a valid semver policy and waits before checking the registry", func() {
		schedule.Spec.ImagePolicy = &operatorv1alpha1.NodeOpUpgradeScheduleImagePolicy{
			Repository: "example.invalid/kairos", Semver: "^3.6.2",
		}
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		Expect(reconcile().RequeueAfter).To(Equal(4*time.Minute + 30*time.Second))
		Expect(schedule.Status.LastScheduledTime).To(BeNil())
		Expect(schedule.Status.LastCheckTime).To(BeNil())
		Expect(schedule.Status.ActiveUpgradeRef).To(BeNil())
	})

	It("honors daylight saving changes", func() {
		now = time.Date(2026, time.March, 7, 10, 0, 0, 0, time.UTC)
		schedule.Spec.Schedule = "0 3 * * *"
		schedule.Spec.TimeZone = "America/Denver"
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
		Expect(reconcile().RequeueAfter).To(Equal(23 * time.Hour))
	})
})
