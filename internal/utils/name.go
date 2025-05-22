package utils

import (
	"fmt"
	"hash/fnv"
)

// KubernetesNameLengthLimit is the maximum length allowed for Kubernetes resource names
const KubernetesNameLengthLimit = 63

// TruncateNameWithHash takes a name and ensures it's no longer than maxLength characters.
// If the name is longer than maxLength, it will be truncated and a hash suffix will be added.
// The hash suffix is 13 characters long (1 hyphen + 12 hash characters).
func TruncateNameWithHash(name string, maxLength int) string {
	// Calculate prefix length by subtracting the hash suffix length (13)
	prefixLength := maxLength - 13
	if len(name) > prefixLength {
		// Create a hash of the full name using FNV-1a
		h := fnv.New64a()
		h.Write([]byte(name))
		hash := fmt.Sprintf("%x", h.Sum64())[:12] // Take first 12 chars of hex

		// Use first prefixLength chars of the name + hash
		return fmt.Sprintf("%s-%s", name[:prefixLength], hash)
	}
	return name
}
