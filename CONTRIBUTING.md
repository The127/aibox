# Contributing to aibox

## Development setup

Required tooling:

- [Go](https://go.dev/), the version `go.mod` names.
- [golangci-lint](https://golangci-lint.run/) v2: linting, configured in
  `.golangci.yml`.
- [just](https://just.systems/): task runner. `just` lists the recipes,
  `just ci` runs everything that must pass.
- [lefthook](https://lefthook.dev/): git hooks. They run the linter, the
  architecture, license and prose checks before each commit, and check the
  commit message.
- [reuse](https://reuse.software/): checks that every file has license and
  copyright information.
- [mdBook](https://rust-lang.github.io/mdBook/): builds the documentation
  site in `docs/`, run by `just docs`.

All of them are in `devenv.nix`. With nix, devenv and direnv installed,
`direnv allow` puts them on `PATH` in this folder. aibox takes them into its
VM with `/nix/store:/nix/store` under `mounts` and
`/project/.devenv/profile/bin` under `path` in the project's config, since
that profile links into `/nix/store`.

The package dependency rules in `arch-go.yml` are checked by
[arch-go](https://github.com/arch-go/arch-go), which the Go toolchain
fetches on its own (`go tool`). It is pinned in a module of its own,
`hack/tools/go.mod`, so its dependencies stay out of aibox's.

After cloning, run the one-time setup. It activates the git hooks:

```
just setup
```

On macOS, building aibox also needs the Xcode command line tools for cgo.
`just build` signs the binary with the entitlement Virtualization.framework
asks for. A build without cgo runs no VM on macOS and says so.

On Windows, aibox builds and the tests of the host side run with `go build`
and `go test`, as the ci does, but it runs no VM there yet and says so.

## The VM image

aibox built from a checkout has no release, so it does not download an
image. Build one:

```
just image           # into out/
just install-image   # and copy it to ~/.aibox/image, where aibox run looks
just aibox           # build aibox and run it on this repo with the image in out/
```

On Linux, `just image` builds the image for amd64 with
[miso](https://github.com/The127/miso), which needs `/dev/kvm`.

On macOS, miso does not run yet, so `just image` builds the image for arm64
with Apple's [container](https://github.com/apple/container) tool from
`image/Containerfile`. Its builder gets 8 CPUs and 8 GiB of memory, and needs
`rosetta = false` under `[build]` in `~/.config/container/config.toml` when
Rosetta is not installed. `just image-docker-arm64` builds the same image with
docker, as the release does.

The kernels are configured in `image/`:

- `microvm.config` is a complete configuration for the microvm board of
  QEMU on amd64, and `kernel.config` holds what aibox changes about it.
- `vz-arm64.config` is the configuration of the kernel Apple's container
  tool boots on Virtualization.framework, and `kernel-arm64.config` holds
  what aibox changes about it.

`check-kernel-config` fails the build when an option of a fragment did not
stick, since the kernel's own merge only warns about it.

`image/Containerfile` builds the same image as the Imagefile, and its stages
follow the stages of the Imagefile.

## Developer Certificate of Origin

Contributions must be signed off. By adding a `Signed-off-by` line to your
commit you certify the [Developer Certificate of Origin 1.1](https://developercertificate.org/):
that you wrote the contribution or otherwise have the right to submit it
under the project's license.

Sign off with git's built-in flag:

```
git commit -s
```

Commits without a sign-off are rejected by the commit-msg hook.

## Commit messages and pull requests

Plain [Conventional Commits](https://www.conventionalcommits.org/): a first
line of the form `type: description`, no scopes. Allowed types: `feat`,
`fix`, `chore`, `docs`, `refactor`, `test`, `perf`, `build`, `ci`, `revert`.

Pull requests are squash merged, and the commit takes the title of the pull
request, so the title follows the same rule. CI checks it.

## Releases

[RELEASING.md](RELEASING.md) describes how releases are made.
