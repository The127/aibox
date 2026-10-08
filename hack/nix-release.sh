#!/usr/bin/env bash
# Point the Nix package at a release: write its version and the hash of its
# Linux archive into nix/release.json. The release workflow runs it after
# goreleaser, with the checksums.txt of the release.
#
#   hack/nix-release.sh 1.2.0 checksums.txt
set -euo pipefail

version=${1:?the version of the release, without the v}
checksums=${2:?the checksums.txt of the release}
archive="aibox_${version}_linux_amd64.tar.gz"

hex=$(awk -v name="$archive" '$2 == name { print $1 }' "$checksums")
if [ -z "$hex" ]; then
    echo "$checksums lists no $archive"
    exit 1
fi

# Nix takes the hash as SRI, the base64 of the digest
sri="sha256-$(printf "$(printf '%s' "$hex" | sed 's/../\\x&/g')" | base64)"

cat > nix/release.json <<EOF
{
  "version": "$version",
  "hash": "$sri"
}
EOF
