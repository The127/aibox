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

Our packages don't include the VM image. The first `aibox run` downloads the
image for its release, which is about 150 MB, and keeps it in
`~/.aibox/image/<version>`. aibox only takes the image whose SHA-256 was
built into the release. After an upgrade it downloads the new image and
removes the old one.

## Packaging aibox

If you package aibox, you can ship the VM image with it. Then your users
don't download it, and your package manager updates it together with aibox.

Put `vmlinuz` and `os.ext4` from the release's `aibox-image_<arch>.tar.gz`
into a folder that all users can read. Our release binaries look in
`/usr/lib/aibox` on Linux and in `/opt/homebrew/lib/aibox` on macOS. If you
build aibox yourself, set the folder at build time:

```
go build -ldflags "-X github.com/the127/aibox/internal/image.systemDir=/usr/lib/aibox" ./cmd/aibox
```

When that folder has both files, aibox uses them and downloads nothing. If
the folder is there but a file is missing, aibox says so and downloads the
image instead. `--image` still takes precedence.

Ship the image of the same release as the binary. aibox checks the kernel
against the SHA-256 built into the release and refuses to start with any
other. If you build aibox yourself, build in the SHA-256 of each kernel too,
or aibox boots any kernel:

```
go build -ldflags "-X github.com/the127/aibox/internal/image.kernelDigests=amd64:<sha256>,arm64:<sha256>" ./cmd/aibox
```

Note that aibox doesn't check the root disk yet.

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
