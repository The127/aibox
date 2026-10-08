#!/usr/bin/env bash
# Point the Nix package at a release: write its version and the hash of
# its archive for each system into nix/release.json. The release workflow
# runs it after goreleaser, with the checksums.txt of the release.
#
#   hack/nix-release.sh 1.3.0 checksums.txt
set -euo pipefail

version=${1:?the version of the release, without the v}
checksums=${2:?the checksums.txt of the release}

# sri prints the hash of the archive of the platform as SRI, the base64 of
# the digest, which is how Nix takes it
sri() {
    local archive="aibox_${version}_$1.tar.gz" hex
    hex=$(awk -v name="$archive" '$2 == name { print $1 }' "$checksums")
    if [ -z "$hex" ]; then
        echo "$checksums lists no $archive" >&2
        exit 1
    fi

    printf 'sha256-%s' "$(printf "$(printf '%s' "$hex" | sed 's/../\\x&/g')" | base64)"
}

linux=$(sri linux_amd64)
darwin=$(sri darwin_arm64)

cat > nix/release.json <<EOF
{
  "version": "$version",
  "hashes": {
    "x86_64-linux": "$linux",
    "aarch64-darwin": "$darwin"
  }
}
EOF
