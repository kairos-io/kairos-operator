package controller

import (
	"context"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// startupRetryInterval is how long the operator waits between attempts of a
// startup task that failed.
const startupRetryInterval = 5 * time.Second

// addStartupTask registers fn with mgr as a task that runs once the manager
// has started, and retries it until it succeeds. Setup work that talks to the
// API server goes here instead of running directly in SetupWithManager: the
// API server can be unreachable for a while when the operator starts, for
// example right after the control plane node rebooted during an upgrade, and
// the operator should wait for it instead of exiting. what names the task in
// the log.
func addStartupTask(mgr manager.Manager, what string, fn func(context.Context) error) error {
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		runUntilDone(ctx, what, startupRetryInterval, fn)
		return nil
	}))
}

// runUntilDone calls fn every interval until it succeeds, logging each
// failure. It returns early when ctx is cancelled, which happens when the
// operator shuts down.
func runUntilDone(ctx context.Context, what string, interval time.Duration, fn func(context.Context) error) {
	log := logf.FromContext(ctx).WithName("setup")
	for {
		err := fn(ctx)
		if err == nil {
			return
		}
		log.Error(err, "Startup task failed, retrying", "task", what, "interval", interval)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
