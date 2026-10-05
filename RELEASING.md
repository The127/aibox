# Releasing

Releases are made from the commit messages on `main`. Nobody tags by hand.

1. [release-please](https://github.com/googleapis/release-please) keeps one
   release pull request open. It holds the next version and the
   `CHANGELOG.md` entry, worked out from the Conventional Commits since the
   last release. `feat` bumps the minor version and `fix` the patch.
   Before 1.0.0, a breaking change also bumps only the minor version.
2. Merging that pull request makes the tag and the GitHub release.
3. In the same workflow, goreleaser builds `aibox` on a Mac and uploads to
   that release:
   - a `tar.gz`, a `deb` and an `rpm` for linux amd64
   - a `tar.gz` for macOS arm64, signed ad hoc with the entitlement
     Virtualization.framework asks for
   - `checksums.txt`

   Tags with a suffix such as `-rc.1` are marked as pre-releases.
4. goreleaser also writes the Homebrew cask `aibox` to
   [The127/homebrew-tap](https://github.com/The127/homebrew-tap). The cask
   removes the quarantine from the binary, since macOS refuses to start a
   downloaded program that is signed ad hoc and not notarized.
5. cosign signs `checksums.txt` without a key. The workflow's own identity
   signs it, so there is no key or secret to keep. The signature is
   uploaded as `checksums.txt.sigstore.json`.

The binary reports its version from the Go build info stamp. A clean
checkout of the tag is enough, there are no ldflags.

## What the packages hold

Only the `aibox` binary and the license. The VM image is not shipped. The
`deb` and the `rpm` depend on QEMU, virtiofsd and bubblewrap
(`qemu-system-x86` for deb, `qemu-system-x86-core` for rpm).

## One-time setup

The release pull request and the tag must start other workflows, and the
cask goes to another repository. The default `GITHUB_TOKEN` can do neither,
so the workflow uses a GitHub App:

1. Create a GitHub App with read and write access to contents and pull
   requests.
2. Create the repository `The127/homebrew-tap` with a first commit on
   `main`, for example a README. goreleaser cannot push to an empty
   repository.
3. Install the app on this repository and on `homebrew-tap`.
4. Add its ID as the repository variable `RELEASE_APP_ID`.
5. Add its private key as the repository secret `RELEASE_APP_PRIVATE_KEY`.

## Forcing a version

To release a version the commits would not give, add `"release-as"` with
that version to `release-please-config.json`. Remove it again once that
release is out, or the same version is proposed every time.

## Checking the configuration

```
just release-check
just release-snapshot
```

The first validates `.goreleaser.yaml`. The second builds every artifact
into `dist/` without publishing or signing anything. It needs a Mac, for
the macOS binary.
