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
	// Get node name from environment variable
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		fmt.Println("NODE_NAME environment variable is required")
		os.Exit(1)
	}

	// Create the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		fmt.Printf("Error creating in-cluster config: %v\n", err)
		os.Exit(1)
	}

	// Create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Printf("Error creating clientset: %v\n", err)
		os.Exit(1)
	}

	// Get the node
	node, err := clientset.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		fmt.Printf("Error getting node %s: %v\n", nodeName, err)
		os.Exit(1)
	}

	// Check if it's a Kairos node
	isKairos, err := checkKairosNode()
	if err != nil {
		fmt.Printf("Error checking if node is Kairos: %v\n", err)
		os.Exit(1)
	}

	if isKairos {
		// Label the node
		if node.Labels == nil {
			node.Labels = make(map[string]string)
		}
		node.Labels["kairos.io/managed"] = "true"

		// Update the node
		_, err = clientset.CoreV1().Nodes().Update(context.TODO(), node, metav1.UpdateOptions{})
		if err != nil {
			fmt.Printf("Error updating node %s: %v\n", nodeName, err)
			os.Exit(1)
		}
		fmt.Printf("Successfully labeled node %s as kairos.io/managed=true\n", nodeName)
	} else {
		fmt.Printf("Node %s is not a Kairos node\n", nodeName)
	}
}

func checkKairosNode() (bool, error) {
	// Check if kairos-release exists and has content
	if content, err := os.ReadFile("/etc/kairos-release"); err == nil {
		// Only consider it a Kairos node if the file has content
		if len(content) > 0 {
			fmt.Println("Kairos release file found with content")
			return true, nil
		}
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
				fmt.Println("Kairos ID found in os-release")
				return true, nil
			}
		}
	}

	return false, nil
}
