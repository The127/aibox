# What the VM writes

With `aibox run`, the VM can write to your project folder. That's on purpose,
it's how you see Claude Code's changes right away. But it also means that
anything in the project folder can come from the VM. Treat it like a pull
request from a stranger until you've read it.

This matters most for files that tools on your machine run without asking:

- lefthook runs the commands in `lefthook.yml`
- direnv runs `.envrc`
- just reads the `justfile`
- npm runs the scripts in `package.json`
- your IDE starts the run configurations in `.idea/`

All of them run straight from the working tree.

## .git

The same goes for `.git`. A setting in `.git/config`, like `core.fsmonitor`
or `core.hooksPath`, or a hook in `.git/hooks`, runs the next time you use
git on your machine. Neither `git status` nor `git diff` shows it.

aibox does not protect these files. Mounting them read-only in the VM
doesn't help. git and IDEs replace `.git/config` every time they write it,
for example on `git push -u`, and the mount stays with the old file.

## What to do

- Only trust a project in your IDE when it's your own. GoLand, for example,
  asks about this when you first open a project.
- Read the diff of what the VM changed before you run a build, a test, a
  script or a run configuration of the project on your machine. Do the same
  before you commit with hooks the project defines.
- Check `.git/config` and `.git/hooks` for changes you didn't make.
