package rebootwatcher_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr/funcr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kairos-io/kairos-operator/internal/bootid"
	"github.com/kairos-io/kairos-operator/internal/rebootwatcher"
)

const (
	bootA     = "11111111-1111-1111-1111-111111111111"
	bootB     = "22222222-2222-2222-2222-222222222222"
	namespace = "ns"
	jobName   = "upgrade-job"
)

// rebootRecorder is the Reboot func the tests inject. It records calls and
// returns whatever err is set.
type rebootRecorder struct {
	calls atomic.Int32
	err   error
}

func (r *rebootRecorder) fn() error {
	r.calls.Add(1)
	return r.err
}

func boot(id string) func() string { return func() string { return id } }

func baseDeps(kube *fake.Clientset, reboot func() error, readBootID func() string) rebootwatcher.Deps {
	return rebootwatcher.Deps{
		Kube:          kube,
		Namespace:     namespace,
		JobName:       jobName,
		PollInterval:  5 * time.Millisecond,
		RebootTimeout: 50 * time.Millisecond,
		Reboot:        reboot,
		ReadBootID:    readBootID,
		Logger:        crlog.Log,
	}
}

func job(complete bool) *batchv1.Job {
	status := corev1.ConditionFalse
	if complete {
		status = corev1.ConditionTrue
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: namespace},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobComplete, Status: status},
		}},
	}
}

func jobPod(phase corev1.PodPhase, message string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "job-pod",
			Namespace: namespace,
			Labels:    map[string]string{"batch.kubernetes.io/job-name": jobName},
		},
		Status: corev1.PodStatus{
			Phase: phase,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  bootid.ReporterContainerName,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: message}},
			}},
		},
	}
}

var _ = Describe("Reboot Pod program", func() {
	ctxWithTimeout := func(d time.Duration) context.Context {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		DeferCleanup(cancel)
		return ctx
	}

	newKube := func(objs ...runtime.Object) *fake.Clientset {
		return fake.NewSimpleClientset(objs...)
	}

	It("validates its dependencies", func() {
		good := baseDeps(newKube(), (&rebootRecorder{}).fn, boot(bootA))

		d := good
		d.Kube = nil
		Expect(rebootwatcher.Run(context.Background(), d)).To(MatchError(ContainSubstring("Kube")))
		d = good
		d.Namespace = ""
		Expect(rebootwatcher.Run(context.Background(), d)).To(MatchError(ContainSubstring("Namespace")))
		d = good
		d.JobName = ""
		Expect(rebootwatcher.Run(context.Background(), d)).To(MatchError(ContainSubstring("JobName")))
		d = good
		d.Reboot = nil
		Expect(rebootwatcher.Run(context.Background(), d)).To(MatchError(ContainSubstring("Reboot")))
		d = good
		d.ReadBootID = nil
		Expect(rebootwatcher.Run(context.Background(), d)).To(MatchError(ContainSubstring("ReadBootID")))
	})

	It("reboots once when the upgrade Job's boot ID equals the current one, then reports that the node did not go down", func() {
		reboot := &rebootRecorder{}
		kube := newKube(job(true), jobPod(corev1.PodSucceeded, bootA+"\n"))

		err := rebootwatcher.Run(ctxWithTimeout(5*time.Second), baseDeps(kube, reboot.fn, boot(bootA)))

		Expect(err).To(MatchError(ContainSubstring("host did not go down within 50ms after reboot was requested")))
		Expect(reboot.calls.Load()).To(BeEquivalentTo(1))
	})

	It("returns ctx.Err() when cancelled while waiting for the node to go down", func() {
		reboot := &rebootRecorder{}
		kube := newKube(job(true), jobPod(corev1.PodSucceeded, bootA))

		deps := baseDeps(kube, reboot.fn, boot(bootA))
		deps.RebootTimeout = time.Minute
		err := rebootwatcher.Run(ctxWithTimeout(100*time.Millisecond), deps)

		Expect(err).To(MatchError(context.DeadlineExceeded))
		Expect(reboot.calls.Load()).To(BeEquivalentTo(1))
	})

	It("returns nil without rebooting when the boot ID changed", func() {
		reboot := &rebootRecorder{}
		kube := newKube(job(true), jobPod(corev1.PodSucceeded, bootA))

		err := rebootwatcher.Run(ctxWithTimeout(5*time.Second), baseDeps(kube, reboot.fn, boot(bootB)))

		Expect(err).NotTo(HaveOccurred())
		Expect(reboot.calls.Load()).To(BeEquivalentTo(0))
	})

	It("wraps the reboot error", func() {
		bootErr := errors.New("nsenter reboot failed")
		reboot := &rebootRecorder{err: bootErr}
		kube := newKube(job(true), jobPod(corev1.PodSucceeded, bootA))

		err := rebootwatcher.Run(ctxWithTimeout(5*time.Second), baseDeps(kube, reboot.fn, boot(bootA)))

		Expect(err).To(MatchError(bootErr))
		Expect(err).To(MatchError(ContainSubstring("reboot: ")))
	})

	It("survives restarts: reboots again on the same boot, returns nil on a new boot", func() {
		kube := newKube(job(true), jobPod(corev1.PodSucceeded, bootA))

		failing := &rebootRecorder{err: errors.New("boom")}
		err := rebootwatcher.Run(ctxWithTimeout(5*time.Second), baseDeps(kube, failing.fn, boot(bootA)))
		Expect(err).To(MatchError(ContainSubstring("reboot: ")))
		Expect(failing.calls.Load()).To(BeEquivalentTo(1))

		again := &rebootRecorder{}
		err = rebootwatcher.Run(ctxWithTimeout(5*time.Second), baseDeps(kube, again.fn, boot(bootA)))
		Expect(err).To(MatchError(ContainSubstring("host did not go down")))
		Expect(again.calls.Load()).To(BeEquivalentTo(1))

		after := &rebootRecorder{}
		Expect(rebootwatcher.Run(ctxWithTimeout(5*time.Second), baseDeps(kube, after.fn, boot(bootB)))).To(Succeed())
		Expect(after.calls.Load()).To(BeEquivalentTo(0))
	})

	It("keeps waiting while the upgrade Job does not exist and continues once it appears", func() {
		reboot := &rebootRecorder{}
		kube := newKube()

		go func() {
			defer GinkgoRecover()
			time.Sleep(40 * time.Millisecond)
			_, err := kube.BatchV1().Jobs(namespace).Create(context.Background(), job(true), metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, err = kube.CoreV1().Pods(namespace).Create(context.Background(), jobPod(corev1.PodSucceeded, bootA), metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
		}()

		err := rebootwatcher.Run(ctxWithTimeout(5*time.Second), baseDeps(kube, reboot.fn, boot(bootB)))

		Expect(err).NotTo(HaveOccurred())
		Expect(reboot.calls.Load()).To(BeEquivalentTo(0))
	})

	It("does not reboot while the upgrade Job is not complete", func() {
		reboot := &rebootRecorder{}
		kube := newKube(job(false), jobPod(corev1.PodSucceeded, bootA))

		err := rebootwatcher.Run(ctxWithTimeout(80*time.Millisecond), baseDeps(kube, reboot.fn, boot(bootA)))

		Expect(err).To(MatchError(context.DeadlineExceeded))
		Expect(reboot.calls.Load()).To(BeEquivalentTo(0))
	})

	DescribeTable("does not reboot when the completed upgrade Job reports no usable boot ID",
		func(pods ...*corev1.Pod) {
			reboot := &rebootRecorder{}
			objs := make([]runtime.Object, 0, 1+len(pods))
			objs = append(objs, job(true))
			for _, p := range pods {
				objs = append(objs, p)
			}

			err := rebootwatcher.Run(ctxWithTimeout(80*time.Millisecond), baseDeps(newKube(objs...), reboot.fn, boot(bootA)))

			Expect(err).To(MatchError(context.DeadlineExceeded))
			Expect(reboot.calls.Load()).To(BeEquivalentTo(0))
		},
		Entry("no Pods at all"),
		Entry("no Succeeded Pod", jobPod(corev1.PodFailed, bootA)),
		Entry("termination message is not a boot ID", jobPod(corev1.PodSucceeded, "not a boot id")),
	)

	DescribeTable("logs each attempt that finds the upgrade Job not ready only at V(1)",
		func(message string, objs ...runtime.Object) {
			run := func(verbosity int) string {
				var out strings.Builder
				log := funcr.New(func(prefix, args string) {
					out.WriteString(args + "\n")
				}, funcr.Options{Verbosity: verbosity})
				d := baseDeps(newKube(objs...), (&rebootRecorder{}).fn, boot(bootA))
				d.Logger = log
				Expect(rebootwatcher.Run(ctxWithTimeout(30*time.Millisecond), d)).To(MatchError(context.DeadlineExceeded))
				return out.String()
			}

			quiet := run(0)
			Expect(quiet).To(ContainSubstring("waiting for the upgrade Job"))
			Expect(quiet).NotTo(ContainSubstring(message))
			Expect(run(1)).To(ContainSubstring(message))
		},
		Entry("the upgrade Job does not exist", "cannot get the upgrade Job yet"),
		Entry("the upgrade Job is not complete", "upgrade Job is not complete yet", job(false)),
		Entry("the upgrade Job reports no boot ID", "upgrade Job is complete but reports no boot ID yet", job(true)),
	)

	It("does not reboot when the current boot ID is invalid", func() {
		reboot := &rebootRecorder{}
		kube := newKube(job(true), jobPod(corev1.PodSucceeded, bootA))

		err := rebootwatcher.Run(ctxWithTimeout(80*time.Millisecond), baseDeps(kube, reboot.fn, boot("")))

		Expect(err).To(MatchError(context.DeadlineExceeded))
		Expect(reboot.calls.Load()).To(BeEquivalentTo(0))
	})

	It("retries without rebooting when the API returns an error", func() {
		reboot := &rebootRecorder{}
		kube := newKube(job(true), jobPod(corev1.PodSucceeded, bootA))
		kube.PrependReactor("get", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewInternalError(errors.New("api down"))
		})

		err := rebootwatcher.Run(ctxWithTimeout(80*time.Millisecond), baseDeps(kube, reboot.fn, boot(bootA)))

		Expect(err).To(MatchError(context.DeadlineExceeded))
		Expect(reboot.calls.Load()).To(BeEquivalentTo(0))
	})

	It("retries without rebooting when listing the upgrade Job's Pods fails", func() {
		reboot := &rebootRecorder{}
		kube := newKube(job(true), jobPod(corev1.PodSucceeded, bootA))
		kube.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewInternalError(errors.New("api down"))
		})

		err := rebootwatcher.Run(ctxWithTimeout(80*time.Millisecond), baseDeps(kube, reboot.fn, boot(bootA)))

		Expect(err).To(MatchError(context.DeadlineExceeded))
		Expect(reboot.calls.Load()).To(BeEquivalentTo(0))
	})

	It("stops polling and returns ctx.Err() when the context is cancelled", func() {
		reboot := &rebootRecorder{}

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()

		err := rebootwatcher.Run(ctx, baseDeps(newKube(), reboot.fn, boot(bootA)))
		Expect(err).To(MatchError(context.Canceled))
		Expect(reboot.calls.Load()).To(BeEquivalentTo(0))
	})
})
