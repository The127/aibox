# aibox

Run Claude Code inside a microVM, with your project folder mounted into it.

- The VM runs on QEMU's microvm machine type.
- The project folder is shared into the VM with virtio-fs. Your user on the
  host is the user inside the VM.
- The VM has no network card. All traffic goes over vsock to a proxy on the
  host, which only lets through hosts on an allowlist.
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
names another port. `preset:go`, `preset:npm`, `preset:pypi`, `preset:cargo`
and `preset:github` stand for the hosts those need. The first run writes that
file with the hosts Claude Code needs. Refused hosts are written to
`proxy.log` next to it. A listed name that resolves into the host's own
networks, such as loopback, link-local or private addresses, is refused as
well, unless the allowlist lists that address. The file can also set `memory` (in MiB) and `cpus`
for the VM. The flags `--memory` and `--cpus` of `aibox run` take precedence
over the file. Without either, the VM gets 2048 MiB and 2 CPUs.

`mounts` lists folders of the host the VM sees read-only, written
`host:guest` with guest the path in the VM, for example
`~/sdk/go1.26.8:/opt/go`. The guest path must stay clear of the folders the
VM needs, such as `/project`, `/home/user`, `/usr` and `/etc`, and of the
other mounts. Each mount is shared over virtio-fs like the project folder,
with names and attributes cached for the whole run, so a change to the
folder on the host may not show in a running VM.

Not there yet:

- There is no way to pass secrets into the VM. You log in with
  `claude /login` inside it.

## Contributing

Run `just setup` once after cloning. It installs git hooks with
[lefthook](https://lefthook.dev/) that run the linter, the architecture and
license checks and a prose check before each commit, and check the commit
message. Commits use [conventional commits](https://www.conventionalcommits.org/)
without a scope and need a sign-off (`git commit -s`). lefthook comes from
`go install github.com/evilmartians/lefthook@latest`.

## License

aibox is licensed under the [Apache License 2.0](LICENSE).
