# aibox

Run Claude Code inside a microVM, with your project folder mounted into it.

- The VM runs on QEMU's microvm machine type.
- The project folder is shared into the VM with virtio-fs. Your user on the
  host is the user inside the VM.
- The VM has no network card. All traffic goes over vsock to a proxy on the
  host, which only lets through hosts on an allowlist.
- Settings for each project live outside the project, in
  `~/.aibox/projects/<escaped path>/config.yaml`, so the VM can't change them.
- The VM's home directory is `~/.aibox/projects/<escaped path>/home/`, shared
  into the VM over virtio-fs, so `~/.claude` and the login survive restarts.
- The VM image (kernel and root disk) is built with
  [miso](https://github.com/The127/miso).

## Status

The first version works: `aibox run` in a project folder boots the VM in
about a second and starts Claude Code in it, with the folder at `/project`
and a home directory that keeps the login between runs. `aibox run --shell`
opens a shell in the VM instead.

```
just install-image   # build the image and copy it to ~/.aibox/image
aibox run
```

The proxy only lets through the hosts listed under `allow` in
`~/.aibox/projects/<escaped path>/config.yaml`. The first run writes that
file with the hosts Claude Code needs. Refused hosts are written to
`proxy.log` next to it. The file can also set `memory` (in MiB) and `cpus`
for the VM. The flags `--memory` and `--cpus` of `aibox run` take precedence
over the file. Without either, the VM gets 2048 MiB and 2 CPUs.

Not there yet:

- The allowlist checks names, not addresses. A listed name that resolves to
  the host's own loopback or LAN is reached.
- There is no way to pass secrets into the VM. You log in with
  `claude /login` inside it.

## Contributing

Commits use [conventional commits](https://www.conventionalcommits.org/) and
need a sign-off (`git commit -s`).

## License

aibox is licensed under the [Apache License 2.0](LICENSE).
