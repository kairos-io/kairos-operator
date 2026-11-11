package utils

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// KubernetesNameLengthLimit is the maximum length allowed for Kubernetes resource names
const KubernetesNameLengthLimit = 63

// hashSuffixLength is the length of the suffix TruncateNameWithHash appends:
// 1 hyphen plus 12 hash characters.
const hashSuffixLength = 13

// TruncateNameWithHash takes a name and ensures it's no longer than maxLength characters.
// If the name is longer than maxLength, it will be truncated and a hash suffix will be added.
// The hash suffix is 13 characters long (1 hyphen + 12 hash characters).
//
// The result is always a valid Kubernetes resource name, which means two things
// beyond the length:
//
//   - The hash is printed with %016x rather than %x. Sum64 returns a uint64 and
//     %x drops its leading zeros, so a hash below 2^60 renders as fewer than 16
//     characters and taking the first 12 of it is out of range.
//   - The truncated prefix is trimmed of trailing "-" and ".". Cutting a name at
//     a fixed offset lands on one of those often enough on cloud hostnames, and
//     a name whose label starts with a separator is rejected by the API server.
func TruncateNameWithHash(name string, maxLength int) string {
	prefixLength := maxLength - hashSuffixLength
	if len(name) <= prefixLength {
		return name
	}

	// Create a hash of the full name using FNV-1a
	h := fnv.New64a()
	h.Write([]byte(name))
	hash := fmt.Sprintf("%016x", h.Sum64())[:12] // Take first 12 chars of hex

	prefix := strings.TrimRight(name[:prefixLength], "-.")
	if prefix == "" {
		return hash
	}

	return fmt.Sprintf("%s-%s", prefix, hash)
}

// KubernetesLabelValueLengthLimit is the maximum length allowed for a
// Kubernetes label value. It happens to match KubernetesNameLengthLimit, but
// the two are validated by different rules, so they are kept apart.
const KubernetesLabelValueLengthLimit = 63

// TruncateLabelValue takes an object name and returns a value that is always
// accepted as a Kubernetes label value.
//
// Object names are DNS subdomains, so they may be up to 253 characters, while a
// label value may be at most 63. Copying a name straight into a label therefore
// makes the whole object invalid and every Create fails. The charset of a label
// value is a superset of the charset of a name, so the only thing that has to
// be handled is the length, and TruncateNameWithHash already keeps the result
// collision-free and free of a trailing separator.
//
// A value that already fits is returned untouched, so the labels the operator
// writes and the selectors it matches on do not change for any object that was
// valid before. TruncateNameWithHash alone would not do: it starts hashing as
// soon as the value is longer than maxLength less the 13-character suffix, and
// that would move every label value between 51 and 63 characters.
func TruncateLabelValue(value string) string {
	if len(value) <= KubernetesLabelValueLengthLimit {
		return value
	}

	return TruncateNameWithHash(value, KubernetesLabelValueLengthLimit)
}
