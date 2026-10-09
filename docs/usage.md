# Running Claude Code

Go to your project folder and start aibox:

```
cd ~/projects/my-app
aibox run
```

aibox boots the VM, mounts the project at `/project` and starts Claude Code
there. Log in like you would on your machine. aibox keeps the login for the
next run.

aibox won't start in your home folder or above it, because the VM would see
all your files.

| Flag | What it does |
|---|---|
| `--shell` | Opens a shell in the VM instead of Claude Code. |
| `--memory` | Sets the VM's memory in MiB. The default is 2048, or what the config says. |
| `--cpus` | Sets the VM's number of CPUs. The default is 2, or what the config says. |
| `--image` | Boots a folder with `vmlinuz` and `os.ext4` instead of the release's image. |
| `--no-sandbox` | Runs QEMU without its bubblewrap sandbox, for debugging. Linux only. |

## What aibox keeps for a project

Each project gets a folder in `~/.aibox/projects`, named after the project's
path:

| File | What's in it |
|---|---|
| `config.yaml` | The [project config](config.md). |
| `home/` | The VM's home folder, so Claude Code's login and `~/.claude` survive restarts. |
| `state.ext4` | A disk for the VM's `/usr/local` and `~/.cache`. |
| `console.log` | The kernel and init messages of the last run. |
| `proxy.log` | Every host and port the proxy connected to or refused, once per run. |
| `tasks/` | The project's [tasks](tasks.md). |

Tools you install into `/usr/local` in the VM stay on the state disk. They
survive restarts and new images. Caches with lots of small files, like Go's
build cache or pip's, live there too. The shared home folder would be slow
for them.

The disk only takes up space on your machine as it fills, up to the size you
set with [`disk`](config.md#disk). The VM formats it on the first boot. It
won't format a disk that has data on it but no ext4 file system, so a broken
disk never gets wiped.

## On macOS

The VM runs arm64 Linux, so Mac programs don't run in it. If you mount a
tool from your Mac, like a Go SDK or `/nix/store`, the VM can't run it. Get
arm64 Linux builds of your tools instead, or install them into `/usr/local`
in the VM, where they stay.

You can only run one `aibox run` per project at a time on macOS.
Virtualization.framework locks the state disk while the VM runs.

## How it works

The terminal is an SSH session over vsock. Inside the VM, the init runs
Claude Code on a pseudo terminal. aibox connects to it, puts your terminal
into raw mode and attaches it. When you resize your terminal, the VM gets
the new size. When Claude Code exits, aibox exits with the same code.
