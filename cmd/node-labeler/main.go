package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	// Create the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		fmt.Printf("Error getting in-cluster config: %v\n", err)
		os.Exit(1)
	}

	// Create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Printf("Error creating clientset: %v\n", err)
		os.Exit(1)
	}

	// Get the node name from the downward API
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		fmt.Println("NODE_NAME environment variable not set")
		os.Exit(1)
	}

	// Read the release files
	isKairos, err := checkKairosNode()
	if err != nil {
		fmt.Printf("Error checking Kairos node: %v\n", err)
		os.Exit(1)
	}

	// Get the node
	node, err := clientset.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		fmt.Printf("Error getting node: %v\n", err)
		os.Exit(1)
	}

	// Update node labels if needed
	if isKairos {
		if node.Labels == nil {
			node.Labels = make(map[string]string)
		}
		if node.Labels["kairos.io/managed"] != "true" {
			node.Labels["kairos.io/managed"] = "true"
			_, err = clientset.CoreV1().Nodes().Update(context.TODO(), node, metav1.UpdateOptions{})
			if err != nil {
				fmt.Printf("Error updating node labels: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Successfully labeled node as Kairos")
		} else {
			fmt.Println("Node already labeled as Kairos")
		}
	} else {
		fmt.Println("Node is not a Kairos node")
	}
}

func checkKairosNode() (bool, error) {
	// Check if kairos-release exists
	if _, err := os.Stat("/etc/kairos-release"); err == nil {
		return true, nil
	}

	// Check os-release for Kairos
	osRelease, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return false, fmt.Errorf("error reading os-release: %v", err)
	}

	// Check if it's a Kairos node by looking for Kairos in the ID field
	lines := strings.Split(string(osRelease), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "ID=") {
			id := strings.TrimPrefix(line, "ID=")
			id = strings.Trim(id, "\"")
			if strings.Contains(strings.ToLower(id), "kairos") {
				return true, nil
			}
		}
	}

	return false, nil
}
