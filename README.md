# aibox

Run Claude Code inside a microVM, with your project folder mounted into it.

- The VM runs on Cloud Hypervisor.
- The project folder is shared into the VM with virtio-fs.
- The VM has no network card. All traffic goes over vsock to a proxy on the
  host, which only lets through hosts on an allowlist.
- Settings for each project live outside the project, in
  `~/.aibox/projects/<escaped path>/config.yaml`, so the VM can't change them.

## Status

Early work in progress. Nothing runs yet.

## Contributing

Commits use [conventional commits](https://www.conventionalcommits.org/) and
need a sign-off (`git commit -s`).
