package hack

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDocker stands in for the docker CLI. The "registry" is a directory:
// registry/<key> exists for every tag that was pushed, and index/<key> holds
// the raw index `docker buildx imagetools inspect --raw` returns. `docker
// manifest inspect` answers from a local store, as the real one does without
// --purge, and always lists what was created locally.
const fakeDocker = `#!/bin/sh
key() { printf %s "$1" | tr '/:' '__'; }
case "$1 $2" in
"buildx imagetools")
    shift 3
    raw=0
    if [ "$1" = "--raw" ]; then raw=1; shift; fi
    k=$(key "$1")
    if [ "$raw" = 1 ]; then
        [ -f "$REG/index/$k" ] || exit 1
        cat "$REG/index/$k"
        exit 0
    fi
    [ -f "$REG/tags/$k" ] || exit 1
    exit 0
    ;;
"manifest create")
    shift 2
    image=$1; shift
    : > "$REG/local-$(key "$image")"
    for s in "$@"; do echo "${s##*-}" >> "$REG/local-$(key "$image")"; done
    ;;
"manifest annotate") ;;
"manifest push")
    image=$3
    if [ -n "${REGISTRY_ARCHES-x}" ] && [ -z "${REGISTRY_ARCHES+set}" ]; then REGISTRY_ARCHES=$(cat "$REG/local-$(key "$image")"); fi
    out='{"manifests":['
    sep=""
    for a in $REGISTRY_ARCHES; do
        out="$out$sep{\"platform\":{\"architecture\":\"$a\"}}"
        sep=","
    done
    printf '%s]}' "$out" > "$REG/index/$(key "$image")"
    ;;
"manifest inspect")
    # the stale local store: always what "create" put there
    out='{"manifests":['
    sep=""
    for a in $(cat "$REG/local-$(key "$3")"); do
        out="$out$sep{\"platform\":{\"architecture\":\"$a\"}}"
        sep=","
    done
    printf '%s]}' "$out"
    ;;
esac
`

func runPushManifest(t *testing.T, arches string, tags []string, registryArches *string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	dir := t.TempDir()
	reg := filepath.Join(dir, "reg")
	for _, d := range []string{"tags", "index"} {
		if err := os.MkdirAll(filepath.Join(reg, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	image := "example.test/op:v1"
	for _, a := range tags {
		k := strings.NewReplacer("/", "_", ":", "_").Replace(image + "-" + a)
		if err := os.WriteFile(filepath.Join(reg, "tags", k), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "push-manifest.sh")
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"REG="+reg, "IMAGE="+image, "ARCHES="+arches)
	if registryArches != nil {
		cmd.Env = append(cmd.Env, "REGISTRY_ARCHES="+*registryArches)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestPushManifest(t *testing.T) {
	t.Run("passes when ARCHES matches the pushed tags", func(t *testing.T) {
		out, err := runPushManifest(t, "amd64 arm64", []string{"amd64", "arm64"}, nil)
		if err != nil {
			t.Fatalf("want success, got %v\n%s", err, out)
		}
	})

	t.Run("fails when an arch was built but is missing from ARCHES", func(t *testing.T) {
		out, err := runPushManifest(t, "amd64 arm64", []string{"amd64", "arm64", "riscv64"}, nil)
		if err == nil {
			t.Fatalf("want failure, got success\n%s", out)
		}
		if !strings.Contains(out, "riscv64") {
			t.Fatalf("error should name riscv64\n%s", out)
		}
	})

	t.Run("reads the index from the registry, not the local store", func(t *testing.T) {
		onlyAmd64 := "amd64"
		out, err := runPushManifest(t, "amd64 arm64", []string{"amd64", "arm64"}, &onlyAmd64)
		if err == nil {
			t.Fatalf("want failure when the registry holds a single arch, got success\n%s", out)
		}
		if !strings.Contains(out, "does not list the architectures") {
			t.Fatalf("unexpected error\n%s", out)
		}
	})
}
