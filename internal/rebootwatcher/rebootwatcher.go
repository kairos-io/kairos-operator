// Package rebootwatcher is the program that runs in the reboot Pod. The
// operator creates one reboot Pod per node, before it creates that node's
// upgrade Job, and tells the reboot Pod the upgrade Job's name. The program:
//
//   - Reads the upgrade Job through the Kubernetes API until Kubernetes marks it
//     Complete. The upgrade Job may not exist yet when the reboot Pod starts, so
//     a missing upgrade Job and API errors are retried.
//   - Reads the boot ID the upgrade Job reported. When the upgrade has
//     finished, the upgrade Job's last container writes the node's boot ID to
//     its termination message, and Kubernetes keeps that message in the status
//     of the upgrade Job's succeeded Pod. Without that boot ID the reboot Pod
//     never reboots the node.
//   - Compares it with the node's current boot ID. If they are equal, the node
//     has not rebooted since the upgrade, so the reboot Pod reboots it. If they
//     differ, the node already rebooted, so the reboot Pod exits successfully.
//   - Retries when the node's current boot ID cannot be read, because without
//     it the reboot Pod cannot tell whether the node already rebooted.
//
// The upgrade Job and its Pods stay in the cluster after the reboot. So when
// Kubernetes restarts the reboot Pod's container on the same boot, because the
// reboot failed, the program reboots again, and when Kubernetes restarts it
// after the reboot, it exits successfully. For the same reason, once the
// program has started the reboot it only ever exits with an error: exiting
// successfully would end the reboot Pod even if the node never went down.
package rebootwatcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/kairos-io/kairos-operator/internal/bootid"
)

// SubcommandName is the first argument that makes the manager binary run this
// program instead of the operator. The operator puts it in the reboot Pod's
// command, and cmd/main.go checks for it.
const SubcommandName = "reboot-watcher"

// JobNameEnv is the environment variable through which the operator tells the
// reboot Pod the name of its upgrade Job.
const JobNameEnv = "JOB_NAME"

// NamespaceEnv is the environment variable that holds the reboot Pod's
// namespace, which is also the namespace of its upgrade Job.
const NamespaceEnv = "POD_NAMESPACE"

// jobNameLabel is the label Kubernetes puts on every Pod of a Job, holding the
// Job's name. The reboot Pod uses it to find the upgrade Job's Pods.
const jobNameLabel = "batch.kubernetes.io/job-name"

// defaultPollInterval is how long the reboot Pod waits between attempts to
// read its upgrade Job, when Deps.PollInterval is not set.
const defaultPollInterval = 10 * time.Second

// defaultRebootTimeout is how long the reboot Pod waits for the node to go
// down after starting the reboot, when Deps.RebootTimeout is not set.
const defaultRebootTimeout = 5 * time.Minute

// Command is the reboot Pod's container command. It runs this program through
// the manager binary.
func Command() []string {
	return []string{"/manager", SubcommandName}
}

// Deps holds the settings of the reboot Pod's program and everything it uses
// outside its own code: the Kubernetes client, the function that reboots the
// node and the function that reads the node's boot ID. Tests replace those
// with fakes.
type Deps struct {
	// Kube is the client used to read the upgrade Job and the upgrade Job's
	// Pods.
	Kube kubernetes.Interface

	// Namespace is the namespace of the upgrade Job.
	Namespace string

	// JobName is the name of the reboot Pod's upgrade Job.
	JobName string

	// PollInterval is how long the reboot Pod waits between attempts to read
	// its upgrade Job. Defaults to 10 seconds when <= 0.
	PollInterval time.Duration

	// RebootTimeout is how long the reboot Pod waits for the node to go down
	// after starting the reboot. If the node is still up after that, the
	// reboot Pod exits with an error, so Kubernetes restarts it and it tries
	// the reboot again. Defaults to 5 minutes when <= 0.
	RebootTimeout time.Duration

	// Reboot reboots the node. When it returns nil the reboot has started,
	// and the node is expected to stop the reboot Pod's container soon after.
	Reboot func() error

	// ReadBootID returns the node's current boot ID, or "" when it cannot be
	// read. When the value is empty or malformed, the reboot Pod tries again
	// later, and it never reboots the node without a boot ID to compare.
	ReadBootID func() string

	// Logger receives the reboot Pod's log messages. When it is not set, the
	// messages are discarded.
	Logger logr.Logger
}

// Run is the main function of the reboot Pod's program, described in the
// package documentation: it waits for the upgrade Job, compares the boot IDs
// and reboots the node. cmd/main.go calls it when the manager binary is
// started as the reboot Pod.
//
// It returns nil only when the node already rebooted after the upgrade Job
// finished. Every other way out returns an error, so the reboot Pod's
// container exits with an error and Kubernetes restarts it: ctx.Err() when ctx
// is cancelled, Reboot's error (prefixed with "reboot: ") when the reboot
// fails, and a timeout error when the node is still up RebootTimeout after the
// reboot started.
func Run(ctx context.Context, d Deps) error {
	if d.Kube == nil {
		return errors.New("rebootwatcher: Deps.Kube is required")
	}
	if d.Namespace == "" {
		return errors.New("rebootwatcher: Deps.Namespace is required")
	}
	if d.JobName == "" {
		return errors.New("rebootwatcher: Deps.JobName is required")
	}
	if d.Reboot == nil {
		return errors.New("rebootwatcher: Deps.Reboot is required")
	}
	if d.ReadBootID == nil {
		return errors.New("rebootwatcher: Deps.ReadBootID is required")
	}
	if d.PollInterval <= 0 {
		d.PollInterval = defaultPollInterval
	}
	if d.RebootTimeout <= 0 {
		d.RebootTimeout = defaultRebootTimeout
	}

	log := d.Logger
	if log.GetSink() == nil {
		log = logr.Discard()
	}

	log.Info("waiting for the upgrade Job", "namespace", d.Namespace, "job", d.JobName)
	for {
		baseline, current, ready := check(ctx, d, log)
		if ready {
			return act(ctx, d, log, baseline, current)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.PollInterval):
		}
	}
}

// check makes one attempt to collect both boot IDs: the one the upgrade Job
// reported (baseline) and the node's current one. Once the upgrade Job is
// Complete and both boot IDs are valid, it returns them with ready set to
// true. Otherwise it logs why and returns ready false, and the reboot Pod
// tries again later. While the upgrade runs, the same reasons (upgrade Job
// not created yet, not Complete yet, no boot ID reported yet) repeat on every
// attempt, so they are logged at V(1). API errors are logged as errors.
func check(ctx context.Context, d Deps, log logr.Logger) (baseline, current string, ready bool) {
	job, err := d.Kube.BatchV1().Jobs(d.Namespace).Get(ctx, d.JobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		log.V(1).Info("cannot get the upgrade Job yet", "error", err.Error())
		return "", "", false
	}
	if err != nil {
		log.Error(err, "cannot get the upgrade Job")
		return "", "", false
	}
	if !jobComplete(job) {
		log.V(1).Info("upgrade Job is not complete yet")
		return "", "", false
	}

	pods, err := d.Kube.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: jobNameLabel + "=" + d.JobName,
	})
	if err != nil {
		log.Error(err, "cannot list the upgrade Job's Pods")
		return "", "", false
	}
	baseline = bootid.FromJobPods(pods.Items)
	if baseline == "" {
		log.V(1).Info("upgrade Job is complete but reports no boot ID yet")
		return "", "", false
	}

	current = d.ReadBootID()
	if !bootid.Valid(current) {
		log.Info("current boot ID is unavailable or malformed")
		return "", "", false
	}
	return baseline, current, true
}

// act compares the two boot IDs. If they differ, the node already rebooted and
// act returns nil. If they are equal, act reboots the node and waits for it to
// go down.
func act(ctx context.Context, d Deps, log logr.Logger, baseline, current string) error {
	log.Info("upgrade Job is complete", "jobBootID", baseline, "currentBootID", current)
	if baseline != current {
		log.Info("host already rebooted, nothing to do")
		return nil
	}

	if err := d.Reboot(); err != nil {
		return fmt.Errorf("reboot: %w", err)
	}

	// The node is going down and will stop this container. If it stays up
	// instead, returning an error makes Kubernetes restart the container,
	// which tries the reboot again.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d.RebootTimeout):
		return fmt.Errorf("host did not go down within %s after reboot was requested", d.RebootTimeout)
	}
}

// jobComplete reports whether Kubernetes has marked the Job Complete.
func jobComplete(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// NsenterReboot reboots the node by running `nsenter -i -m -t 1 -- reboot`,
// which runs `reboot` in the namespaces of the node's init process instead of
// the reboot Pod's own. It is the Reboot function used in production; tests
// use a stub.
func NsenterReboot() error {
	cmd := exec.Command("nsenter", "-i", "-m", "-t", "1", "--", "reboot")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
