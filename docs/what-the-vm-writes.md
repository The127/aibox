# What the VM writes

The project folder is shared writable on purpose. So whatever the VM writes
into the project is untrusted until you have read it, like a pull request
from a stranger. That includes files that tools on the host run without
asking. lefthook runs the commands of `lefthook.yml`, direnv runs `.envrc`,
just reads the `justfile`, npm runs the scripts of `package.json`, and an IDE
starts the run configurations under `.idea/`, all from the working tree.

This includes `.git`. A key in `.git/config`, such as `core.fsmonitor` or
`core.hooksPath`, or a hook in `.git/hooks` runs the next time you use git on
the host, and neither `git status` nor `git diff` shows it. aibox does not
protect them. A read-only mount in the VM does not hold: git and IDEs
replace `.git/config` whenever they write it, for example on `git push -u`,
and the mount goes with the old file.

So:

- Trust a project in your IDE, which GoLand asks about when you first open
  it, only when the project is your own.
- Read the diff of what the VM changed before you run a build, a test, a
  script or a run configuration of the project on the host, and before you
  commit with hooks the project defines.
- Check `.git/config` and `.git/hooks` for changes you did not make.
