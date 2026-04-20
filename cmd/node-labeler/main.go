package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	every := flag.Int("every", 0, "re-label every N seconds; 0 runs once and exits")
	flag.Parse()

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		fmt.Fprintln(os.Stderr, "NODE_NAME environment variable is required")
		os.Exit(1)
	}

	clientset, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error building clientset: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if *every > 0 {
		for {
			if err := syncLabels(ctx, clientset, nodeName, hostEtcPath(), "/proc/cmdline"); err != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "error syncing labels: %v\n", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(*every) * time.Second):
			}
		}
	} else {
		if err := syncLabels(ctx, clientset, nodeName, hostEtcPath(), "/proc/cmdline"); err != nil {
			fmt.Fprintf(os.Stderr, "error syncing labels: %v\n", err)
			os.Exit(1)
		}
	}
}

func hostEtcPath() string {
	if p := os.Getenv("HOST_ETC_PATH"); p != "" {
		return p
	}
	return "/host/etc"
}

func buildClientset() (kubernetes.Interface, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}
