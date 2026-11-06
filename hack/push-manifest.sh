#!/bin/sh
# Publish the multi-arch index for one image, then read it back.
#
#   IMAGE   the index tag, e.g. quay.io/kairos/operator:v1.2.3
#   ARCHES  space-separated arch names; each one must already be pushed as
#           $IMAGE-$arch by a leg of the matching build matrix
#
# The release workflow used to hand-write `docker manifest create` and one
# `docker manifest annotate` line per arch, per image. That is three places to
# edit for every platform added, and a forgotten annotate line produces an
# index that is missing a platform without failing anything. So the list comes
# from ARCHES, and the index is verified after the push.
#
# ARCHES alone cannot catch an arch that was added to a build matrix and
# forgotten in ARCHES: both sides of that comparison would be missing it. So
# the registry is asked which per-arch tags exist ($IMAGE-<arch>), and the
# release fails if one of them is not in ARCHES. KNOWN_ARCHES is the set of
# names probed; add a name there when a matrix first builds a new one.

set -eu

: "${IMAGE:?IMAGE is required}"
: "${ARCHES:?ARCHES is required}"

KNOWN_ARCHES="${KNOWN_ARCHES:-amd64 arm64 arm riscv64 ppc64le s390x 386}"

# Every per-arch tag the build matrices pushed must be in ARCHES.
missing=""
for arch in $KNOWN_ARCHES; do
    case " $ARCHES " in *" $arch "*) continue ;; esac
    if docker buildx imagetools inspect "${IMAGE}-${arch}" >/dev/null 2>&1; then
        missing="${missing} ${arch}"
    fi
done
if [ -n "$missing" ]; then
    echo "error: ${IMAGE}-<arch> exists for${missing}, which ARCHES does not list" >&2
    exit 1
fi

sources=""
for arch in $ARCHES; do
    sources="${sources} ${IMAGE}-${arch}"
done

# shellcheck disable=SC2086 # sources is a deliberate list of arguments
docker manifest create "$IMAGE" $sources

for arch in $ARCHES; do
    docker manifest annotate "$IMAGE" "${IMAGE}-${arch}" --os linux --arch "$arch"
done

docker manifest push "$IMAGE"

# Read the pushed index back from the registry rather than trusting the
# commands above. `docker manifest inspect` is not used: without --purge it
# prints the list kept in ~/.docker/manifests, which is what we just created,
# not what the registry holds.
# "unknown" is the platform a registry reports for the attestation manifests
# buildkit attaches; it is not an architecture.
expected=$(printf '%s\n' $ARCHES | tr ' ' '\n' | sed '/^$/d' | sort -u)
published=$(docker buildx imagetools inspect --raw "$IMAGE" |
    jq -r '.manifests[]?.platform.architecture' |
    grep -v '^unknown$' | sort -u)

if [ "$expected" != "$published" ]; then
    echo "error: ${IMAGE} does not list the architectures it was built for" >&2
    echo "  expected: $(echo $expected | tr '\n' ' ')" >&2
    echo "  published: $(echo $published | tr '\n' ' ')" >&2
    exit 1
fi

echo "ok: ${IMAGE} lists $(echo $published | tr '\n' ' ')"
