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
# from ARCHES, and the index is verified against it after the push.

set -eu

: "${IMAGE:?IMAGE is required}"
: "${ARCHES:?ARCHES is required}"

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

# Read the pushed index back rather than trusting the commands above. This is
# what turns "an arch was added to the build matrix but not to ARCHES" into a
# failed release instead of an index quietly missing that platform.
# "unknown" is the platform a registry reports for the attestation manifests
# buildkit attaches; it is not an architecture.
expected=$(printf '%s\n' $ARCHES | tr ' ' '\n' | sed '/^$/d' | sort -u)
published=$(docker manifest inspect "$IMAGE" |
    jq -r '.manifests[]?.platform.architecture' |
    grep -v '^unknown$' | sort -u)

if [ "$expected" != "$published" ]; then
    echo "error: ${IMAGE} does not list the architectures it was built for" >&2
    echo "  expected: $(echo $expected | tr '\n' ' ')" >&2
    echo "  published: $(echo $published | tr '\n' ' ')" >&2
    exit 1
fi

echo "ok: ${IMAGE} lists $(echo $published | tr '\n' ' ')"
