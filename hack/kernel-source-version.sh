#!/usr/bin/env bash
# Print the Debian version of the kernel source the kernels were built from,
# which kernel.Imagefile and kernel.Containerfile put in their version string
# after the #. The images workflow names the cache of the kernels with it.
#
#   hack/kernel-source-version.sh out/vmlinuz out/vmlinuz-hyperv
set -euo pipefail

[ $# -gt 0 ] || { echo "name at least one kernel" >&2; exit 1; }

version=
for kernel in "$@"; do
    # the version string reads "... (user@host) #7.2.9-1 SMP ...", as the
    # header of an x86 bzImage and as the banner of an arm64 Image
    found=$(LC_ALL=C grep -aoE '\) #[0-9][^ ]*-[^ ]+ ' "$kernel" | head -n1 | sed 's/^) #//; s/ $//') || true
    if [ -z "$found" ]; then
        echo "$kernel has no Debian version in its version string" >&2
        exit 1
    fi
    if [ -n "$version" ] && [ "$found" != "$version" ]; then
        echo "$kernel is built from $found, the kernel before it from $version" >&2
        exit 1
    fi
    version=$found
done

echo "$version"
