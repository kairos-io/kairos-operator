package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// labelFields maps KAIROS_<KEY> suffixes from kairos-release to node label names.
var labelFields = map[string]string{
	"ID":                      "kairos.io/id",
	"FAMILY":                  "kairos.io/family",
	"FLAVOR":                  "kairos.io/flavor",
	"FLAVOR_RELEASE":          "kairos.io/flavor-release",
	"VARIANT":                 "kairos.io/variant",
	"RELEASE":                 "kairos.io/release",
	"MODEL":                   "kairos.io/model",
	"ARCH":                    "kairos.io/arch",
	"TRUSTED_BOOT":            "kairos.io/trusted-boot",
	"FIPS":                    "kairos.io/fips",
	"SOFTWARE_VERSION_PREFIX": "kairos.io/software-version-prefix",
	"SOFTWARE_VERSION":        "kairos.io/software-version",
}

// annotationFields maps KAIROS_<KEY> suffixes to node annotation names.
var annotationFields = map[string]string{
	"NAME":           "kairos.io/name",
	"ID_LIKE":        "kairos.io/id-like",
	"INIT_VERSION":   "kairos.io/init-version",
	"VERSION":        "kairos.io/version",
	"BUG_REPORT_URL": "kairos.io/bug-report-url",
	"HOME_URL":       "kairos.io/home-url",
}

// syncLabels reads Kairos metadata from etcPath and cmdlinePath and applies
// the resulting labels and annotations to the named node.
// Returns nil without patching if the node is not a Kairos node.
func syncLabels(ctx context.Context, clientset kubernetes.Interface, nodeName, etcPath, cmdlinePath string) error {
	labels, annotations := collectMetadata(etcPath, cmdlinePath)

	if len(labels) == 0 {
		fmt.Printf("node %s is not a Kairos node, skipping\n", nodeName)
		return nil
	}

	if err := patchNode(ctx, clientset, nodeName, labels, annotations); err != nil {
		return err
	}

	fmt.Printf("labeled node %s\n", nodeName)
	return nil
}

// collectMetadata parses kairos-release and /proc/cmdline and returns the labels
// and annotations to apply. Returns empty maps (not an error) if not a Kairos node.
func collectMetadata(etcPath, cmdlinePath string) (labels, annotations map[string]string) {
	release, err := parseEnvFile(etcPath + "/kairos-release")
	if err != nil {
		fmt.Printf("cannot read kairos-release, assuming non-Kairos node: %v\n", err)
		return nil, nil
	}

	if release == nil {
		if !isKairosOSRelease(etcPath) {
			return nil, nil
		}
		fmt.Println("Kairos ID found in os-release")
		labels = map[string]string{"kairos.io/managed": "true"}
		if bootState := detectBootState(cmdlinePath); bootState != "" {
			labels["kairos.io/boot-state"] = bootState
		}
		return labels, map[string]string{}
	}

	labels = map[string]string{"kairos.io/managed": "true"}
	annotations = map[string]string{}

	for key, labelName := range labelFields {
		if v := release["KAIROS_"+key]; v != "" {
			labels[labelName] = sanitizeLabelValue(v)
		}
	}

	for key, annotationName := range annotationFields {
		if v := release["KAIROS_"+key]; v != "" {
			annotations[annotationName] = v
		}
	}

	if bootState := detectBootState(cmdlinePath); bootState != "" {
		labels["kairos.io/boot-state"] = bootState
	}

	return labels, annotations
}

// detectBootState reads cmdlinePath and returns the Kairos boot state label value
// (active, passive, recovery, livecd, or unknown).
func detectBootState(cmdlinePath string) string {
	data, err := os.ReadFile(cmdlinePath)
	if err != nil {
		return ""
	}
	cmdline := string(data)

	switch {
	case strings.Contains(cmdline, "COS_ACTIVE"):
		return "active"
	case strings.Contains(cmdline, "COS_PASSIVE"):
		return "passive"
	case strings.Contains(cmdline, "COS_RECOVERY"),
		strings.Contains(cmdline, "COS_SYSTEM"),
		strings.Contains(cmdline, "recovery-mode"):
		return "recovery"
	case strings.Contains(cmdline, "live:LABEL"),
		strings.Contains(cmdline, "live:CDLABEL"),
		strings.Contains(cmdline, "netboot"):
		return "livecd"
	default:
		return "unknown"
	}
}

func isKairosOSRelease(etcPath string) bool {
	osRelease, err := parseEnvFile(etcPath + "/os-release")
	if err != nil || osRelease == nil {
		return false
	}
	return strings.Contains(strings.ToLower(osRelease["ID"]), "kairos")
}

// parseEnvFile parses a KEY=VALUE or KEY="VALUE" env file into a map.
// Returns nil (not an error) if the file does not exist.
func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	result := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		result[k] = strings.Trim(v, `"'`)
	}
	return result, scanner.Err()
}

var invalidLabelChars = regexp.MustCompile(`[^a-zA-Z0-9\-_.]`)

// sanitizeLabelValue makes s a valid Kubernetes label value by replacing
// invalid characters with "-" and truncating to 63 characters.
func sanitizeLabelValue(s string) string {
	s = invalidLabelChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-_.")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-_.")
	}
	return s
}

type nodePatch struct {
	Metadata nodePatchMetadata `json:"metadata"`
}

type nodePatchMetadata struct {
	Labels      map[string]*string `json:"labels,omitempty"`
	Annotations map[string]*string `json:"annotations,omitempty"`
}

func patchNode(ctx context.Context, clientset kubernetes.Interface, nodeName string,
	labels, annotations map[string]string) error {
	node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting node %s: %w", nodeName, err)
	}

	patchLabels := toNullablePatch(labels, node.Labels)
	patchAnnotations := toNullablePatch(annotations, node.Annotations)

	if len(patchLabels) == 0 && len(patchAnnotations) == 0 {
		return nil
	}

	patch := nodePatch{
		Metadata: nodePatchMetadata{
			Labels:      patchLabels,
			Annotations: patchAnnotations,
		},
	}

	data, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshaling patch: %w", err)
	}

	_, err = clientset.CoreV1().Nodes().Patch(
		ctx,
		nodeName,
		types.MergePatchType,
		data,
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patching node %s: %w", nodeName, err)
	}

	return nil
}

// toNullablePatch builds a map[string]*string that sets all values from newValues
// and nullifies any existing kairos.io/* key absent from newValues, removing stale entries.
func toNullablePatch(newValues, existing map[string]string) map[string]*string {
	result := make(map[string]*string, len(newValues))
	for k, v := range newValues {
		result[k] = &v
	}
	for k := range existing {
		if strings.HasPrefix(k, "kairos.io/") {
			if _, present := newValues[k]; !present {
				result[k] = nil
			}
		}
	}
	return result
}
