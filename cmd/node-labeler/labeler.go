package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// labelKairosManaged identifies nodes the operator is responsible for labeling.
const labelKairosManaged = "kairos.io/managed"

// Boot state label values, the same set kairos/sdk/state classifies a boot into.
const (
	bootStateActive    = "active"
	bootStatePassive   = "passive"
	bootStateRecovery  = "recovery"
	bootStateAutoReset = "autoreset"
	bootStateLiveCD    = "livecd"
	bootStateUnknown   = "unknown"
)

const (
	// ukiCmdlineFlag is on the command line of every UKI artifact, and is how
	// immucore, the agent and the SDK all recognise a Trusted Boot system.
	ukiCmdlineFlag = "rd.immucore.uki"
	// autoResetCmdlineFlag is on the command line of the statereset boot entry,
	// which boots the recovery system and then resets the installation.
	autoResetCmdlineFlag = "kairos.reset"
	// inRAMCmdlineFlag opts a boot into the in-RAM workflow, where the rootfs
	// is a tmpfs copy of the installed system.
	inRAMCmdlineFlag = "kairos.ram"
	// systemd-boot's vendor GUID. It never changes, and the loader writes its
	// EFI variables under it.
	systemdBootVendorGUID = "4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"
	// loaderEntrySelectedVar names the entry systemd-boot booted, for example
	// "active.conf". Volatile: the loader writes it on every boot.
	loaderEntrySelectedVar = "LoaderEntrySelected-" + systemdBootVendorGUID
	// loaderDevicePartUUIDVar holds the partition UUID of the EFI System
	// Partition the loader ran from. A live medium cannot set it, so its
	// absence separates an installed boot from an ISO or a netboot.
	loaderDevicePartUUIDVar = "LoaderDevicePartUUID-" + systemdBootVendorGUID
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

// syncLabels reads Kairos metadata from etcPath, cmdlinePath and firmwarePath
// and applies the resulting labels and annotations to the named node.
// Returns nil without patching if the node is not a Kairos node.
func syncLabels(ctx context.Context, clientset kubernetes.Interface,
	nodeName, etcPath, cmdlinePath, firmwarePath string) error {
	labels, annotations := collectMetadata(etcPath, cmdlinePath, firmwarePath)

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

// collectMetadata parses kairos-release, the kernel command line and, on a UKI
// boot, the EFI variables under firmwarePath, and returns the labels
// and annotations to apply. Returns empty maps (not an error) if not a Kairos node.
func collectMetadata(etcPath, cmdlinePath, firmwarePath string) (labels, annotations map[string]string) {
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
		labels = map[string]string{labelKairosManaged: "true"} //nolint:goconst // common label value; not worth a constant
		if bootState := detectBootState(cmdlinePath, firmwarePath); bootState != "" {
			labels["kairos.io/boot-state"] = bootState
		}
		return labels, map[string]string{}
	}

	labels = map[string]string{labelKairosManaged: "true"} //nolint:goconst // common label value; not worth a constant
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

	if bootState := detectBootState(cmdlinePath, firmwarePath); bootState != "" {
		labels["kairos.io/boot-state"] = bootState
	}

	return labels, annotations
}

// detectBootState returns the Kairos boot state label value (active, passive,
// recovery, autoreset, livecd, or unknown) for the boot described by
// cmdlinePath. firmwarePath is the host's /sys/firmware, which is where a UKI
// boot keeps the answer: every UKI role is the same artifact with the same
// embedded command line, so only the loader entry systemd-boot recorded in an
// EFI variable says which role booted. It mirrors detectBoot in
// kairos/sdk/state.
func detectBootState(cmdlinePath, firmwarePath string) string {
	data, err := os.ReadFile(cmdlinePath)
	if err != nil {
		return ""
	}
	cmdline := string(data)

	// An in-RAM boot runs the installed system from a tmpfs and carries no
	// COS_* label, but it is the current install, so it is active.
	if isInRAMBoot(cmdline) {
		return bootStateActive
	}

	if strings.Contains(cmdline, ukiCmdlineFlag) {
		return ukiBootState(firmwarePath)
	}

	switch {
	// The automatic state reset boots the recovery system with kairos.reset on
	// the command line, so it also satisfies the COS_RECOVERY case below and
	// has to be matched before it.
	case strings.Contains(cmdline, autoResetCmdlineFlag):
		return bootStateAutoReset
	case strings.Contains(cmdline, "COS_ACTIVE"):
		return bootStateActive
	case strings.Contains(cmdline, "COS_PASSIVE"):
		return bootStatePassive
	case strings.Contains(cmdline, "COS_RECOVERY"),
		strings.Contains(cmdline, "COS_SYSTEM"),
		strings.Contains(cmdline, "recovery-mode"):
		return bootStateRecovery
	case strings.Contains(cmdline, "live:LABEL"),
		strings.Contains(cmdline, "live:CDLABEL"),
		strings.Contains(cmdline, "netboot"):
		return bootStateLiveCD
	default:
		return bootStateUnknown
	}
}

// isInRAMBoot reports whether the command line opts the boot into the in-RAM
// workflow, as either the bare kairos.ram token, kairos.ram=<value>, or any
// kairos.ram.<child> sub-flag.
func isInRAMBoot(cmdline string) bool {
	for _, token := range strings.Fields(cmdline) {
		if token == inRAMCmdlineFlag ||
			strings.HasPrefix(token, inRAMCmdlineFlag+".") ||
			strings.HasPrefix(token, inRAMCmdlineFlag+"=") {
			return true
		}
	}
	return false
}

// efiControlChars matches the bytes an EFI variable file carries around its
// payload: the four byte attribute header and the NUL bytes of the UTF-16
// encoding. Stripping them leaves the printable value.
var efiControlChars = regexp.MustCompile("[[:cntrl:]]")

// ukiBootState reads the boot entry systemd-boot selected and maps it to a boot
// state. Returns unknown when the EFI variables are not readable, which is what
// a node gets if firmwarePath was never mounted.
func ukiBootState(firmwarePath string) string {
	if firmwarePath == "" {
		return bootStateUnknown
	}
	efiVars := filepath.Join(firmwarePath, "efi", "efivars")

	// LoaderDevicePartUUID is written only when the loader ran from a disk, so
	// a boot without it came from a live medium.
	devicePart, err := os.ReadFile(filepath.Join(efiVars, loaderDevicePartUUIDVar))
	if err != nil || len(devicePart) == 0 {
		return bootStateLiveCD
	}

	entry, err := os.ReadFile(filepath.Join(efiVars, loaderEntrySelectedVar))
	if err != nil {
		return bootStateUnknown
	}
	name := efiControlChars.ReplaceAllString(string(entry), "")
	if !strings.HasSuffix(name, ".conf") {
		return bootStateUnknown
	}

	switch {
	case strings.HasPrefix(name, "active"):
		return bootStateActive
	case strings.HasPrefix(name, "passive"):
		return bootStatePassive
	case strings.HasPrefix(name, "recovery"):
		return bootStateRecovery
	case strings.HasPrefix(name, "statereset"):
		return bootStateAutoReset
	default:
		return bootStateUnknown
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
