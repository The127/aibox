# Installing

On macOS with Apple silicon, with [Homebrew](https://brew.sh):

```
brew install --cask the127/tap/aibox
```

On Linux, each [release](https://github.com/The127/aibox/releases) has a
`deb` and an `rpm` for amd64, which depend on QEMU, virtiofsd and bubblewrap,
and a `tar.gz` with the binary. aibox needs Linux 6.7 or newer and virtiofsd
on `PATH` or at `/usr/libexec/virtiofsd`, as Debian 13, Ubuntu 24.04 and
Fedora have it. QEMU and virtiofsd from Nix work too: for a QEMU in
`/nix/store`, the sandbox of QEMU binds the store in place of `/usr/lib64`.
aibox also needs access to `/dev/kvm`.

With [Nix](https://nixos.org), on Linux on amd64 or macOS on Apple silicon,
the flake of this repository has a package of the latest release. On Linux
it brings QEMU, virtiofsd and bubblewrap along:

```
nix profile install github:The127/aibox
nix run github:The127/aibox -- run
```

The packages do not hold the VM image. The first `aibox run` downloads the
image of its release, about 150 MB, and keeps it in
`~/.aibox/image/<version>`. It takes only the image whose SHA-256 the release
built into it. After an upgrade it downloads the new image and removes the
old one.

`checksums.txt` lists the checksums of all files of a release, and cosign
signed it without a key. To check a download:

```
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/The127/aibox/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt
```
