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

The VM keeps a home folder and a disk for each project, so the login of
Claude Code, `~/.claude`, and tools installed into `/usr/local` survive
restarts. The messages of the kernel and the init go into `console.log`, and
the hosts and ports the proxy connected to or refused into `proxy.log`, each
once per run, both in the folder of the project below `~/.aibox/projects`.
