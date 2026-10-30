package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeKind puts a `kind` on PATH that records its arguments, and returns the
// file the calls are recorded in.
func fakeKind(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\n"
	if err := os.WriteFile(filepath.Join(dir, "kind"), []byte(script), 0o755); err != nil { //nolint:gosec // test helper script must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestDeleteKindClusterSkipsEmptyName(t *testing.T) {
	log := fakeKind(t)
	if err := deleteKindCluster(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); err == nil {
		out, _ := os.ReadFile(log) //nolint:gosec // path built from t.TempDir
		t.Fatalf("kind was run for an empty cluster name: %q", out)
	}
}

func TestDeleteKindClusterDeletesNamedCluster(t *testing.T) {
	log := fakeKind(t)
	if err := deleteKindCluster("kairos-operator-e2e-1"); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(log) //nolint:gosec // path built from t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "delete cluster --name kairos-operator-e2e-1" {
		t.Fatalf("unexpected kind call: %q", got)
	}
}
