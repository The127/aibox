# aibox

Run Claude Code inside a microVM, with your project folder mounted into it.

- The VM runs on QEMU's microvm machine type.
- The project folder is shared into the VM with virtio-fs. Your user on the
  host is the user inside the VM. The VM sees `.git/config` and `.git/hooks`
  read-only: a config key or a hook written there would run on the host the
  next time you use git, and nothing in `git status` or `git diff` would
  show it. `.git` itself can neither be renamed nor removed in the VM, so it
  cannot be swapped for a writable copy, and a `.git` file of a worktree or
  submodule is read-only as a whole. `.git/info` stays writable, since
  lefthook keeps unstaged changes there during a commit. The rest of `.git`
  and the working tree stay writable, so `git worktree add` and submodules
  work. Inside the VM, `git -c key=value` still sets a key for one command.
  Not covered: a worktree or submodule made in the VM, whose own config and
  hooks git reads when you run it inside that folder on the host; a project
  that gets its `.git` only inside the VM; a config that includes a file
  from the working tree; `.git/config.worktree`; and a git dir placed inside
  the project with `--separate-git-dir`. A symlink in place of one of the
  protected paths stops the VM from starting.
- The VM has no network card. All traffic goes over vsock to a proxy on the
  host, which only lets through hosts on an allowlist. Each VM gets a vsock
  namespace of its own, which aibox creates inside an unprivileged user
  namespace, so no root is needed. Every VM can then use the same address,
  CID 3, and other processes on the host cannot connect to it.
- The terminal is an SSH session over vsock. The init of the VM runs Claude
  Code on a pseudo terminal and serves it to aibox on the host, which puts
  your terminal into raw mode and attaches it. The size of your terminal and
  its changes reach the VM, and the exit code of Claude Code comes back. The
  virtio console of the VM, with the messages of the kernel and of the init,
  goes into `console.log` in the project's aibox folder.
- Settings for each project live outside the project, in
  `~/.aibox/projects/<escaped path>/config.yaml`, so the VM can't change them.
- The VM's home directory is `~/.aibox/projects/<escaped path>/home/`, shared
  into the VM over virtio-fs, so `~/.claude` and the login survive restarts.
- Each project has a disk of its own, `state.ext4` next to the home, that
  the VM keeps `/usr/local` and `~/.cache` on. Tools installed there survive
  restarts and rebuilds of the image, and caches of many small files, like
  the Go build cache or pip's, stay off the shared home, which is slow for
  them. The file is sparse and takes up host disk only as it fills. Its
  size is fixed when the disk is created on the first run, 16 GiB unless
  `disk` in the config says otherwise. The init formats it on the first
  boot and refuses a disk whose superblock has data but no ext4 magic, so
  a damaged disk is never formatted over.
- Containers and VMs run inside the VM without root. The kernel has user
  namespaces, cgroups, overlayfs and now KVM, the init opens `/dev/kvm` and
  `/dev/fuse` to everyone and mounts cgroup2, and the image has `newuidmap`
  and `newgidmap` with a subuid range for the user. podman's storage lives
  on the state disk under `~/.local/share/containers`, since overlayfs does
  not work on virtio-fs. A container shares the network of the VM, so it has
  only loopback and the proxy, and gets the proxy variables. `preset:docker`
  allows pulls from Docker Hub. podman and QEMU themselves come from the
  host like other tools, through `mounts` and `path`. The init gives the
  user a cgroup with the controllers cpuset, cpu, io, memory and pids and
  starts the command in a cgroup below it, so limits such as `--memory` and
  `--pids-limit` apply. Setuid programs inside an image do not work, since
  the state disk is mounted nosuid.
- QEMU runs in a bubblewrap sandbox with no network, no environment and
  no writable file system. It sees only its own program, libraries and
  firmware and a copy of the kernel. Devices, the disks and the sockets
  reach it as open files from aibox, and it filters its own syscalls.
  `aibox run --no-sandbox` runs it without bubblewrap, for debugging.
- The root disk is read-only for QEMU and for the VM. The init puts an
  overlay in RAM over it only to make the mount points, then makes the
  root read-only again, so inside the VM only `/tmp`, `/run`, `/dev/shm`,
  the home, the project and the state disk take writes.
- Once QEMU runs, aibox restricts itself too: no new privileges, Landlock
  rules that let it read `/etc` for DNS and connect only to the ports of the
  allow list, and a seccomp filter that refuses ptrace, mount, namespace
  and similar syscalls. This needs Linux 6.7 or newer.
- The VM image (kernel and root disk) is built with
  [miso](https://github.com/The127/miso). The kernel has no PCI, no ACPI,
  no modules and no network drivers: `image/microvm.config` is a complete
  configuration for QEMU's microvm board, and `image/kernel.config` holds
  what aibox changes about it. `just image` rebuilds the image.

## Status

The first version works: `aibox run` in a project folder boots the VM in
about a second and starts Claude Code in it, with the folder at `/project`
and a home directory that keeps the login between runs. `aibox run --shell`
opens a shell in the VM instead.

```
just install   # build the image into ~/.aibox/image and aibox into ~/go/bin
aibox run
```

The proxy only lets through the hosts listed under `allow` in
`~/.aibox/projects/<escaped path>/config.yaml`, on port 443 unless an entry
names another port. `preset:go`, `preset:npm`, `preset:pypi`, `preset:cargo`,
`preset:github` and `preset:docker` stand for the hosts those need. The first
run writes that file with the hosts Claude Code needs. Refused hosts are
written to `proxy.log` next to it. A listed name that resolves into the host's
own networks, such as loopback, link-local or private addresses, is refused as
well, unless the allowlist lists that address. The file can also set `memory`
(in MiB) and `cpus` for the VM. The flags `--memory` and `--cpus` of `aibox
run` take precedence over the file. Without either, the VM gets 2048 MiB and 2
CPUs.

`aibox config edit` opens the file in the editor git would use, `$VISUAL`,
then `$EDITOR`, then `vi`, or in the one given with `--editor`. Afterwards
it checks the file and reports a mistake. An editor that returns at once,
such as `code` without `--wait`, is checked before you have edited.

`mounts` lists folders of the host the VM sees read-only, written
`host:guest` with guest the path in the VM, for example
`~/sdk/go1.26.8:/opt/go`. The guest path must stay clear of the folders the
VM needs, such as `/project`, `/usr` and `/etc`, and of the other mounts. A
mount inside `/home/user` is fine, because the home of the VM is a folder of
aibox, not your home on the host. Leave `/home/user/.claude` itself alone,
the login of the VM lives there. Each mount is shared over virtio-fs like
the project folder, with names and attributes cached for the whole run, so a
change to the folder on the host may not show in a running VM.

Your skills in `~/.claude/skills` are mounted read-only at the same place
in the home of the VM, so Claude Code finds them. A symlink in there that
points outside the folder does not resolve in the VM. Without that folder
nothing is mounted. Your settings, plugins and MCP servers are not shared,
the VM starts with its own. A mount of the config on or around that path
takes its place.

`path` lists folders in the VM that go in front of its `PATH`, for Claude
Code and the shell alike, for example `/opt/go/bin` from the mount above. An
entry that is a variable name, such as `PATH` or `DIRENV_PATH`, stands for
the folders in that variable of the host, less the relative and empty ones.
So the tools of a `nix develop` or `direnv` shell on the host are found in
the VM, if `/nix/store` is mounted at the same path. The folders are not
checked against the mounts. They are sent with the other variables when the
terminal session starts.

`env` lists variables for the command in the VM. `NAME=value` sets a value,
`NAME` alone passes the value the host has when the VM starts, which is how a
secret gets in without being written into the file. They travel over the
terminal session, not over the kernel command line. A variable aibox sets
itself, such as `HOME`, `PATH` or the proxy variables, is refused.

## macOS

On macOS aibox runs the VM with Apple's
[container](https://github.com/apple/container) tool, where each container is
a VM of its own, instead of QEMU. The commands, the config and the guest are
the same. What differs:

- The image is an OCI image built from `image/Containerfile` for arm64, with
  the kernel of the tool. `just install-container-image` builds it into the
  store of the tool as `aibox:latest` and names it in
  `~/.aibox/image/container-image`. The builder of the tool needs
  `rosetta = false` under `[build]` in `~/.config/container/config.toml`
  when Rosetta is not installed.
- The tool mounts the project, the home and the `mounts` of the config
  itself, and keeps the state of a project in a volume named after it.
- The container has no network. The tool publishes a socket of the guest on
  the host, and the terminal and the proxy share that one connection.
- The init runs as root of its own VM with every capability, as it does under
  QEMU. Claude Code runs as the user without them.
- aibox on the host is not sandboxed or confined. The VM of the tool is the
  boundary, and `--no-sandbox` changes nothing.

```
just install-container-image
go install ./cmd/aibox
aibox run
```


## Contributing

The tools the justfile needs, Go, golangci-lint, just, lefthook and reuse, are
in `devenv.nix`. With nix, devenv and direnv installed, `direnv allow` puts
them on `PATH` in this folder. aibox takes them into its VM with
`/nix/store:/nix/store` under `mounts` and `/project/.devenv/profile/bin`
under `path` in the project's config, since that profile links into
`/nix/store`.

Run `just setup` once after cloning. It installs git hooks with
[lefthook](https://lefthook.dev/) that run the linter, the architecture and
license checks and a prose check before each commit, and check the commit
message. Commits use [conventional commits](https://www.conventionalcommits.org/)
without a scope and need a sign-off (`git commit -s`). lefthook comes from
`go install github.com/evilmartians/lefthook@latest`.

## License

aibox is licensed under the [Apache License 2.0](LICENSE).
