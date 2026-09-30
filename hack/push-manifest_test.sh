#!/bin/sh
# push-manifest.sh publishes a multi-arch index and then reads it back, so that
# an arch built by the release matrix but missing from the index fails the
# release. That read-back is the kind of check that can silently never fire, so
# this drives the script against a docker stub and asserts it both ways.
#
# No docker and no registry: the stub records the commands it was given and
# answers `manifest inspect` from ARCHES_PUBLISHED.

set -eu

repo_root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"

cat > "$tmp/bin/docker" <<'STUB'
#!/bin/sh
# A docker stand-in. Logs every invocation, and answers `manifest inspect`
# with an index listing ARCHES_PUBLISHED (plus the "unknown" entry a real
# registry carries for attestation manifests).
echo "$*" >> "$LOG"
if [ "${1:-}" = manifest ] && [ "${2:-}" = inspect ]; then
    # Shaped like a real `docker manifest inspect`: an image index whose last
    # entry is the attestation manifest, whose platform is unknown/unknown.
    echo '{'
    echo '  "manifests": ['
    for arch in ${ARCHES_PUBLISHED:-}; do
        echo "    {\"platform\": {\"architecture\": \"$arch\", \"os\": \"linux\"}},"
    done
    echo '    {"platform": {"architecture": "unknown", "os": "unknown"}}'
    echo '  ]'
    echo '}'
fi
exit 0
STUB
chmod +x "$tmp/bin/docker"

fail() { echo "FAIL: $1" >&2; exit 1; }

run() {
    rm -f "$tmp/log"
    : > "$tmp/log"
    PATH="$tmp/bin:$PATH" LOG="$tmp/log" \
    IMAGE="$1" ARCHES="$2" ARCHES_PUBLISHED="$3" \
        "$repo_root/hack/push-manifest.sh" >"$tmp/out" 2>"$tmp/err"
}

IMG=quay.io/kairos/operator-node-labeler:v9.9.9

# 1. The index lists everything that was built. The script has to create the
#    index from one source tag per arch, annotate each one, push, and pass.
if ! run "$IMG" "amd64 arm64 riscv64" "amd64 arm64 riscv64"; then
    fail "a complete index was rejected: $(cat "$tmp/err")"
fi
grep -qx "manifest create $IMG $IMG-amd64 $IMG-arm64 $IMG-riscv64" "$tmp/log" ||
    fail "manifest create did not name one source tag per arch: $(grep create "$tmp/log")"
for arch in amd64 arm64 riscv64; do
    grep -qx "manifest annotate $IMG $IMG-$arch --os linux --arch $arch" "$tmp/log" ||
        fail "no annotate line for $arch"
done
grep -qx "manifest push $IMG" "$tmp/log" || fail "the index was never pushed"
grep -q 'ok: .* lists amd64 arm64 riscv64' "$tmp/out" ||
    fail "expected the arch list on success, got: $(cat "$tmp/out")"
echo "ok: a complete index is created, annotated, pushed and accepted"

# 2. The whole point: an arch in the build matrix but missing from the index
#    has to fail. Before the read-back this produced a green release with an
#    index that no RISC-V node could pull from.
if run "$IMG" "amd64 arm64 riscv64" "amd64 arm64"; then
    fail "an index missing riscv64 was accepted"
fi
grep -q "does not list the architectures it was built for" "$tmp/err" ||
    fail "expected the mismatch error, got: $(cat "$tmp/err")"
grep -q "riscv64" "$tmp/err" || fail "the error does not name the missing arch"
echo "ok: an index missing an arch fails the release"

# 3. The mismatch check has to be symmetric. An index carrying an arch nobody
#    asked for means the matrix and ARCHES have drifted the other way.
if run "$IMG" "amd64 arm64" "amd64 arm64 riscv64"; then
    fail "an index with an unexpected arch was accepted"
fi
echo "ok: an index with an unexpected arch fails the release"

# 4. The "unknown" platform entry a registry adds for attestation manifests is
#    not an architecture. Counting it would fail every release.
if ! run "$IMG" "amd64" "amd64"; then
    fail "the attestation manifest's unknown platform was counted as an arch"
fi
echo "ok: the unknown attestation platform is ignored"

# 5. Neither input may be assumed.
if IMAGE="" ARCHES="amd64" PATH="$tmp/bin:$PATH" LOG="$tmp/log" \
    "$repo_root/hack/push-manifest.sh" >/dev/null 2>&1; then
    fail "an empty IMAGE was accepted"
fi
echo "ok: an empty IMAGE is refused"

echo "PASS: hack/push-manifest.sh verifies the index it pushes"
