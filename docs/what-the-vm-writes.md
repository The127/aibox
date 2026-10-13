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

## What aibox checks for you

When an `aibox run` session ends, aibox tells you which of these files the
VM may have changed:

```
aibox: the VM may have changed files that programs on this machine run. Read them before you run anything here:
  changed  .git/config
  added    .git/hooks/pre-commit
```

It checks `.git/config`, `.git/hooks`, `.envrc`, `lefthook.yml`, `justfile`,
`package.json`, `.idea` and `.vscode`. Add the files your project runs on
top of these, like a `Makefile`, with [watch](config.md#watch).

The check doesn't stop anything, it only tells you. It also can't tell
whether the VM or a program on your machine changed a file. Your IDE
rewrites `.idea/workspace.xml` while the project is open, so that file
shows up often. Look at it anyway, it can hold run configurations.

It doesn't cover everything either. A file that isn't on the list, like a
script your build calls, isn't checked. And aibox only tells you when the
session ends. If you use git or run the project on your machine while the
session runs, nothing warns you.

### How it works

Right before the VM starts, aibox hashes every file under the paths on the
list. When the VM stops, it hashes them again and prints what was added,
changed or removed. It reads no link and follows none, so a link the VM
put there counts as a change. aibox prints a name with characters your
terminal would act on in quotes.

## What to do

- Only trust a project in your IDE when it's your own. GoLand, for example,
  asks about this when you first open a project.
- Read what aibox lists at the end of a session before you run anything.
- Read the diff of what the VM changed before you run a build, a test, a
  script or a run configuration of the project on your machine. Do the same
  before you commit with hooks the project defines.
- Check `.git/config` and `.git/hooks` for changes you didn't make.
