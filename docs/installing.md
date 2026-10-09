# Installing

## macOS

On a Mac with Apple silicon, install aibox with [Homebrew](https://brew.sh):

```
brew install --cask the127/tap/aibox
```

## Linux

Each [release](https://github.com/The127/aibox/releases) has a `deb` and an
`rpm` for amd64. They pull in QEMU, virtiofsd and bubblewrap. There is also a
`tar.gz` with just the binary, and then you install those three yourself.

aibox needs:

- Linux 6.7 or newer
- access to `/dev/kvm`
- virtiofsd on your `PATH` or at `/usr/libexec/virtiofsd`, which is where
  Debian 13, Ubuntu 24.04 and Fedora put it

QEMU and virtiofsd from Nix work too. When QEMU is in `/nix/store`, its
sandbox binds the store in place of `/usr/lib64`.

## Nix

The flake in this repository has a package of the latest release, for Linux
on amd64 and macOS on Apple silicon. On Linux it brings QEMU, virtiofsd and
bubblewrap with it.

```
nix profile install github:The127/aibox
```

You can also run it without installing:

```
nix run github:The127/aibox -- run
```

## The VM image

The packages don't include the VM image. The first `aibox run` downloads the
image for its release, which is about 150 MB, and keeps it in
`~/.aibox/image/<version>`. aibox only takes the image whose SHA-256 was
built into the release. After an upgrade it downloads the new image and
removes the old one.

## Checking a download

Each release has a `checksums.txt` with the checksums of all its files. It is
signed with cosign, without a key. To check a download, run this in the
folder you downloaded it to:

```
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/The127/aibox/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt
```

The first command checks that the release workflow of this repository signed
`checksums.txt`. The second checks your files against it.
