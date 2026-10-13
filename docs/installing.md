# Installing

## Homebrew

On a Mac with Apple silicon or on Linux on amd64, install aibox with
[Homebrew](https://brew.sh):

```
brew install --cask the127/tap/aibox
```

On Linux, Homebrew installs just the binary. Install QEMU, virtiofsd and
bubblewrap from your distribution, not from Homebrew. Ubuntu only lets its
own bubblewrap make the namespaces that the sandbox needs.

```
sudo apt install qemu-system-x86 virtiofsd bubblewrap        # Debian, Ubuntu
sudo dnf install qemu-system-x86-core virtiofsd bubblewrap   # Fedora
```

aibox takes the first of these programs on your `PATH`, and Homebrew puts
its own folder first. If you installed any of them with Homebrew, uninstall
them there.

The rest of [Linux](#linux) applies too.

## Linux

Each [release](https://github.com/The127/aibox/releases) has a `deb` and an
`rpm` for amd64. They pull in QEMU, virtiofsd and bubblewrap. There is also a
`tar.gz` with just the binary, and then you install those three yourself.

aibox needs:

- Linux 7.0 or newer, built with vsock (`CONFIG_VSOCKETS` and
  `CONFIG_VHOST_VSOCK`). Most distributions have it as a module. aibox
  opens `/dev/vhost-vsock` to make the kernel load it. If that doesn't
  work, load it with `sudo modprobe vhost_vsock`.
- access to `/dev/kvm`
- virtiofsd on your `PATH` or at `/usr/libexec/virtiofsd`, which is where
  Debian 13, Ubuntu 24.04 and Fedora put it

QEMU and virtiofsd from Nix work too.

## Windows

aibox runs on Windows 11 on amd64 with Hyper-V, which Windows 11 Pro,
Enterprise and Education have. Download `aibox.exe` from the
[releases](https://github.com/The127/aibox/releases).

Hyper-V is off until you turn it on. Do that once, from a PowerShell you
opened as administrator, and restart Windows:

```
Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V -All
```

`aibox service install` tells you when Hyper-V is off, and so does `aibox
run` when the service can't make a VM because of it. If you turn Hyper-V
off later, the service no longer starts: run `aibox service install`
again to see why.

Hyper-V only lets administrators start VMs, so aibox comes with a Windows
service that starts them for you. Install it once, from a terminal you
opened as administrator:

```
aibox service install
```

That downloads the VM image, puts the service under `C:\Program Files\aibox`
and its kernel and root disk under `C:\ProgramData\aibox`, and starts it.
From then on you run `aibox run` and `aibox task` as yourself, without
administrator rights. Run `aibox service install` again after you update
aibox; aibox tells you when a service is too old for it. Run `aibox
service uninstall` to remove it all.

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
