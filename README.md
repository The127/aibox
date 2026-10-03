# aibox

Run Claude Code inside a microVM, with your project folder mounted into it.

- The VM runs on QEMU's microvm machine type.
- The project folder is shared into the VM with virtio-fs.
- The VM has no network card. All traffic goes over vsock to a proxy on the
  host, which only lets through hosts on an allowlist.
- Settings for each project live outside the project, in
  `~/.aibox/projects/<escaped path>/config.yaml`, so the VM can't change them.
- The VM's home directory is `~/.aibox/projects/<escaped path>/home/`, shared
  into the VM over virtio-fs, so `~/.claude` and the login survive restarts.
- The VM image (kernel, initrd and root disk) is built with
  [miso](https://github.com/The127/miso).

## Status

Early work in progress. Nothing runs yet.

The first version will:

- let all traffic through the proxy, as if the allowlist allowed every host;
- have no way to pass secrets into the VM. You log in by running
  `claude /login` inside the VM.

The allowlist and secrets come later.

## Contributing

Commits use [conventional commits](https://www.conventionalcommits.org/) and
need a sign-off (`git commit -s`).

## License

aibox is licensed under the [Apache License 2.0](LICENSE).
