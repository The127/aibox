# Running Claude Code

`aibox run` in a project folder boots the VM and starts Claude Code. It
refuses to start in your home folder or above it, since the VM would then
see all of your files.

| Flag | Meaning |
|---|---|
| `--shell` | Open a shell in the VM instead of Claude Code. |
| `--memory` | Memory of the VM in MiB, 2048 unless the config says otherwise. |
| `--cpus` | Number of CPUs of the VM, 2 unless the config says otherwise. |
| `--image` | A folder with `vmlinuz` and `os.ext4` to boot instead of the image of the release. |
| `--no-sandbox` | Run QEMU without its bubblewrap sandbox, to debug it. Linux only. |

## What a project keeps

Each project has a folder below `~/.aibox/projects`, named after its path.
It holds:

| File | What it holds |
|---|---|
| `config.yaml` | The [project config](config.md). |
| `home/` | The home folder of the VM, so the login of Claude Code and `~/.claude` survive restarts. |
| `state.ext4` | A disk for `/usr/local` and `~/.cache` of the VM. See below. |
| `console.log` | The messages of the kernel and the init of the VM, of the last run. |
| `proxy.log` | Each host and port the proxy connected to or refused, once per run. |
| `tasks/` | The folders of the [tasks](tasks.md) of the project. |

Tools you install into `/usr/local` in the VM stay on the state disk across
restarts and new images. Caches of many small files, such as the Go build
cache or the one of pip, stay on it too and off the shared home, which is
slow for them. The disk takes up space on the host only as it fills, up to
the size [`disk`](config.md#disk) sets. The init formats the disk on the
first boot. It refuses a disk that holds data but no ext4 file system, so a
damaged disk is never formatted over. On macOS Virtualization.framework
locks the disk while the VM runs, so a second `aibox run` of the same
project does not start.

## On macOS

The VM is arm64 Linux, so programs of the Mac do not run in it. A mount of a
tool from the Mac, such as a Go SDK or `/nix/store`, gives the VM files it
cannot run. Take tools for the VM from a folder of arm64 Linux programs, or
install them into `/usr/local` in the VM, which keeps them.

## How it works

The terminal is an SSH session over vsock. The init of the VM runs Claude
Code on a pseudo terminal and serves it to aibox, which puts your terminal
into raw mode and attaches it. The size of your terminal and its changes
reach the VM, and the exit code of Claude Code comes back.
