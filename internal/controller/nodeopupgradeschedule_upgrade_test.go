package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	operatorv1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

var _ = Describe("NodeOpUpgradeSchedule upgrade execution", func() {
	var (
		schedule *operatorv1alpha1.NodeOpUpgradeSchedule
		r        *NodeOpUpgradeScheduleReconciler
		store    client.WithWatch
		now      time.Time
		request  ctrl.Request
	)
	BeforeEach(func() {
		now = time.Date(2026, time.November, 25, 10, 5, 0, 0, time.UTC)
		schedule = &operatorv1alpha1.NodeOpUpgradeSchedule{
			ObjectMeta: metav1.ObjectMeta{
				Name: "upgrade-cron", Namespace: "default", UID: types.UID("schedule-one"), Generation: 1,
			},
			Spec: operatorv1alpha1.NodeOpUpgradeScheduleSpec{
				Schedule: "*/5 * * * *",
				UpgradeTemplate: operatorv1alpha1.NodeOpUpgradeSpec{
					Image: "ghcr.io/example/kairos:v3.6.2", Concurrency: 1,
					StopOnFailure: asBool(true), UpgradeRecovery: asBool(true), Debug: asBool(true),
					NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "worker"}},
					Resources:    &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}},
					ExcludePaths: []string{"/etc/example"},
				},
			},
			Status: operatorv1alpha1.NodeOpUpgradeScheduleStatus{
				ObservedGeneration: 1, NextScheduledTime: &metav1.Time{Time: now},
			},
		}
		store = fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).
			WithStatusSubresource(&operatorv1alpha1.NodeOpUpgradeSchedule{}, &operatorv1alpha1.NodeOpUpgrade{}).
			WithObjects(schedule).Build()
		r = &NodeOpUpgradeScheduleReconciler{Client: store, now: func() time.Time { return now }}
		request = ctrl.Request{NamespacedName: client.ObjectKeyFromObject(schedule)}
	})
	reconcile := func() (ctrl.Result, error) {
		result, err := r.Reconcile(ctx, request)
		Expect(store.Get(ctx, request.NamespacedName, schedule)).To(Succeed())
		return result, err
	}
	upgrades := func() []operatorv1alpha1.NodeOpUpgrade {
		list := &operatorv1alpha1.NodeOpUpgradeList{}
		Expect(store.List(ctx, list, client.InNamespace(schedule.Namespace))).To(Succeed())
		return list.Items
	}
	setPhase := func(phase string) {
		upgrade := &operatorv1alpha1.NodeOpUpgrade{}
		Expect(store.Get(ctx, types.NamespacedName{Namespace: schedule.Namespace, Name: schedule.Status.ActiveUpgradeRef.Name}, upgrade)).To(Succeed())
		upgrade.Status.Phase = phase
		Expect(store.Status().Update(ctx, upgrade)).To(Succeed())
	}

	It("copies the fixed image template and records one owned execution", func() {
		expected := schedule.Spec.UpgradeTemplate.DeepCopy()
		result, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(scheduleUpgradeRequeue))
		children := upgrades()
		Expect(children).To(HaveLen(1))
		Expect(children[0].Spec).To(Equal(*expected))
		Expect(metav1.IsControlledBy(&children[0], schedule)).To(BeTrue())
		Expect(children[0].Annotations[scheduledAtAnnotation]).To(Equal(now.Format(time.RFC3339)))
		Expect(schedule.Status.ActiveUpgradeRef.Name).To(Equal(children[0].Name))
		Expect(schedule.Status.LastAttemptedImage).To(Equal(expected.Image))
		Expect(schedule.Status.LastScheduledTime.Time).To(BeTemporally("==", now))
		Expect(schedule.Status.NextScheduledTime).To(BeNil())
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(upgrades()).To(HaveLen(1))
	})

	It("waits through future cron times while an upgrade is running", func() {
		_, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		setPhase(phaseRunning)
		now = now.Add(24 * time.Hour)
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(upgrades()).To(HaveLen(1))
		Expect(schedule.Status.NextScheduledTime).To(BeNil())
		Expect(meta.FindStatusCondition(schedule.Status.Conditions, "Ready").Reason).To(Equal("UpgradeRunning"))
	})

	It("records success and schedules a future execution without replaying missed ticks", func() {
		_, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		setPhase(phaseCompleted)
		now = now.Add(24*time.Hour + 20*time.Second)
		result, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(4*time.Minute + 40*time.Second))
		Expect(schedule.Status.LastSuccessfulImage).To(Equal(schedule.Spec.UpgradeTemplate.Image))
		Expect(schedule.Status.ActiveUpgradeRef).To(BeNil())
		Expect(upgrades()).To(HaveLen(1))
		now = schedule.Status.NextScheduledTime.Time
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(upgrades()).To(HaveLen(2))
	})

	It("retains a failed upgrade and blocks further executions", func() {
		_, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		failedRef := schedule.Status.ActiveUpgradeRef.DeepCopy()
		setPhase(phaseFailed)
		now = now.Add(24 * time.Hour)
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(schedule.Status.ActiveUpgradeRef).To(Equal(failedRef))
		Expect(schedule.Status.LastSuccessfulImage).To(BeEmpty())
		Expect(schedule.Status.NextScheduledTime).To(BeNil())
		condition := meta.FindStatusCondition(schedule.Status.Conditions, "Ready")
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal("UpgradeFailed"))
		Expect(upgrades()).To(HaveLen(1))
	})

	It("tracks completion even after the schedule is suspended", func() {
		_, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		schedule.Spec.Suspend = true
		schedule.Generation++
		Expect(store.Update(ctx, schedule)).To(Succeed())
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(schedule.Status.ActiveUpgradeRef).NotTo(BeNil())
		setPhase(phaseCompleted)
		result, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))
		Expect(schedule.Status.LastSuccessfulImage).To(Equal(schedule.Spec.UpgradeTemplate.Image))
		Expect(schedule.Status.ActiveUpgradeRef).To(BeNil())
		Expect(schedule.Status.NextScheduledTime).To(BeNil())
		Expect(upgrades()).To(HaveLen(1))
	})

	It("keeps a failed create pending and retries the same execution", func() {
		fail := true
		r.Client = interceptor.NewClient(store, interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*operatorv1alpha1.NodeOpUpgrade); ok && fail {
					fail = false
					return fmt.Errorf("create unavailable")
				}
				return c.Create(ctx, obj, opts...)
			},
		})
		due := schedule.Status.NextScheduledTime.DeepCopy()
		_, err := reconcile()
		Expect(err).To(MatchError(ContainSubstring("create unavailable")))
		Expect(upgrades()).To(BeEmpty())
		Expect(schedule.Status.NextScheduledTime.Time).To(BeTemporally("==", due.Time))
		Expect(schedule.Status.LastScheduledTime).To(BeNil())
		Expect(schedule.Status.ActiveUpgradeRef).To(BeNil())
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(upgrades()).To(HaveLen(1))
		Expect(schedule.Status.LastScheduledTime.Time).To(BeTemporally("==", due.Time))
	})

	DescribeTable("recovers a created child after a lost response or status write", func(loseStatus bool) {
		fail := true
		r.Client = interceptor.NewClient(store, interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if err := c.Create(ctx, obj, opts...); err != nil {
					return err
				}
				if !loseStatus && fail {
					fail = false
					return fmt.Errorf("lost create response")
				}
				return nil
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object,
				patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if loseStatus && fail {
					fail = false
					return fmt.Errorf("lost status write")
				}
				return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
			},
		})
		_, err := reconcile()
		Expect(err).To(HaveOccurred())
		children := upgrades()
		Expect(children).To(HaveLen(1))
		Expect(schedule.Status.ActiveUpgradeRef).To(BeNil())
		// A fresh reconciler must recover ownership without relying on memory.
		r = &NodeOpUpgradeScheduleReconciler{Client: store, now: func() time.Time { return now }}
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(schedule.Status.ActiveUpgradeRef.Name).To(Equal(children[0].Name))
		Expect(upgrades()).To(HaveLen(1))
	}, Entry("create response", false), Entry("status write", true))

	It("recovers an upgrade that completed before its parent status was recorded", func() {
		upgrade, err := r.createScheduledUpgrade(ctx, schedule, schedule.Spec.UpgradeTemplate.Image)
		Expect(err).NotTo(HaveOccurred())
		upgrade.Status.Phase = phaseCompleted
		Expect(store.Status().Update(ctx, upgrade)).To(Succeed())
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		Expect(schedule.Status.LastSuccessfulImage).To(Equal(upgrade.Spec.Image))
		Expect(schedule.Status.ActiveUpgradeRef).To(BeNil())
		Expect(schedule.Status.NextScheduledTime.Time.After(now)).To(BeTrue())
		Expect(upgrades()).To(HaveLen(1))
	})

	It("does not adopt or overwrite a conflicting resource", func() {
		foreign := &operatorv1alpha1.NodeOpUpgrade{
			ObjectMeta: metav1.ObjectMeta{Name: scheduledUpgradeName(schedule, now), Namespace: schedule.Namespace},
			Spec:       operatorv1alpha1.NodeOpUpgradeSpec{Image: "ghcr.io/other/kairos:v9.0.0"},
		}
		Expect(store.Create(ctx, foreign)).To(Succeed())
		_, err := reconcile()
		Expect(err).To(MatchError(ContainSubstring("already used by another execution")))
		Expect(schedule.Status.ActiveUpgradeRef).To(BeNil())
		Expect(upgrades()).To(HaveLen(1))
		Expect(upgrades()[0].Spec.Image).To(Equal(foreign.Spec.Image))
	})

	It("blocks when the active reference is missing instead of resubmitting work", func() {
		schedule.Status.ActiveUpgradeRef = &corev1.LocalObjectReference{Name: "missing-upgrade"}
		Expect(store.Status().Update(ctx, schedule)).To(Succeed())
		_, err := reconcile()
		Expect(err).To(MatchError(ContainSubstring("read active upgrade")))
		Expect(schedule.Status.ActiveUpgradeRef.Name).To(Equal("missing-upgrade"))
		Expect(upgrades()).To(BeEmpty())
	})

	It("copies a selected digest and merges pull secrets without changing the template", func() {
		schedule.Spec.UpgradeTemplate.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "template-auth"}}
		schedule.Spec.ImagePolicy = &operatorv1alpha1.NodeOpUpgradeScheduleImagePolicy{
			Repository: "ghcr.io/example/kairos", Semver: "^3.6.2",
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "policy-auth"}, {Name: "template-auth"}, {Name: "policy-auth"}},
		}
		Expect(store.Update(ctx, schedule)).To(Succeed())
		selected := "ghcr.io/example/kairos:v3.6.3@sha256:" + strings.Repeat("a", 64)
		upgrade, err := r.createScheduledUpgrade(ctx, schedule, selected)
		Expect(err).NotTo(HaveOccurred())
		Expect(upgrade.Spec.Image).To(Equal(selected))
		Expect(upgrade.Spec.ImagePullSecrets).To(Equal([]corev1.LocalObjectReference{{Name: "template-auth"}, {Name: "policy-auth"}}))
		Expect(schedule.Spec.UpgradeTemplate.Image).To(Equal("ghcr.io/example/kairos:v3.6.2"))
		Expect(schedule.Spec.UpgradeTemplate.ImagePullSecrets).To(HaveLen(1))
	})

	It("does not create after the schedule was suspended during discovery", func() {
		stale := schedule.DeepCopy()
		schedule.Spec.Suspend = true
		schedule.Generation++
		Expect(store.Update(ctx, schedule)).To(Succeed())
		_, err := r.createScheduledUpgrade(ctx, stale, stale.Spec.UpgradeTemplate.Image)
		Expect(err).To(MatchError(ContainSubstring("schedule changed or stopped")))
		Expect(upgrades()).To(BeEmpty())
	})

	It("uses distinct bounded names across executions and recreated schedules", func() {
		schedule.Name = strings.Repeat("long-name-", 20) + "cron"
		first := scheduledUpgradeName(schedule, now)
		Expect(len(first)).To(BeNumerically("<=", 63))
		Expect(scheduledUpgradeName(schedule, now)).To(Equal(first))
		Expect(scheduledUpgradeName(schedule, now.Add(5*time.Minute))).NotTo(Equal(first))
		schedule.UID = types.UID("schedule-two")
		Expect(scheduledUpgradeName(schedule, now)).NotTo(Equal(first))
	})

	DescribeTable("does not create after another execution changed the schedule status", func(active bool) {
		stale := schedule.DeepCopy()
		if active {
			schedule.Status.ActiveUpgradeRef = &corev1.LocalObjectReference{Name: "another-upgrade"}
		} else {
			schedule.Status.NextScheduledTime = &metav1.Time{Time: now.Add(5 * time.Minute)}
		}
		Expect(store.Status().Update(ctx, schedule)).To(Succeed())
		_, err := r.createScheduledUpgrade(ctx, stale, stale.Spec.UpgradeTemplate.Image)
		Expect(err).To(MatchError(ContainSubstring("schedule execution changed")))
		Expect(upgrades()).To(BeEmpty())
	}, Entry("an upgrade became active", true), Entry("the execution was advanced", false))
})
