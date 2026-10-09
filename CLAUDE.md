# aibox

aibox runs Claude Code inside a microVM: QEMU on Linux,
Virtualization.framework on macOS. `aibox run` shares the project folder
into the VM over virtio-fs. `aibox task` gives the VM a clone of a commit
instead and hands back its commits as a bundle. The docs in docs/ say how it
works, in the "How it works" section of each page.

## Docs

- Check the docs for every issue you work on. Change `docs/` and the README
  where the work makes them wrong or leaves something out, in the same pull
  request. Say in the pull request that you checked, even when nothing
  needed a change.

## Commits

- Use conventional commits (`feat:`, `fix:`, `docs:`, `chore:`, ...).
- Sign off every commit with `git commit -s`.
- Don't put a link to the Claude Code session in commit messages or PR
  descriptions, and don't add a `Co-Authored-By: Claude` line.

@CLAUDE.local.md
