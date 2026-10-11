# Running Claude Code

Go to your project folder and start aibox:

```
cd ~/projects/my-app
aibox run
```

aibox boots the VM, mounts the project at `/project` and starts Claude Code
there. Log in like you would on your machine. aibox keeps the login for the
next run.

aibox won't start in your home folder or above it, because the VM would see
all your files.

| Flag | What it does |
|---|---|
| `--shell` | Opens a shell in the VM instead of Claude Code. |
| `--memory` | Sets the VM's memory in MiB. The default is 2048, or what the config says. |
| `--cpus` | Sets the VM's number of CPUs. The default is 2, or what the config says. |
| `--image` | Boots a folder with `vmlinuz` and `os.ext4` instead of the image your package installed or aibox downloaded. aibox doesn't check its kernel. |
| `--no-sandbox` | Runs QEMU without its bubblewrap sandbox, for debugging. Linux only. |

## What aibox keeps for a project

Each project gets a folder in `~/.aibox/projects`, named after the project's
path:

| File | What's in it |
|---|---|
| `config.yaml` | The [project config](config.md). |
| `home/` | The VM's home folder, so Claude Code's login and `~/.claude` survive restarts. |
| `state.ext4` | A disk for the VM's `/usr/local` and `~/.cache`. |
| `console.log` | The kernel and init messages of the last run. |
| `proxy.log` | Every host and port the proxy connected to or refused, the first time in each run. When the run ends, a summary says how often and when last. A killed aibox writes no summary. |
| `tasks/` | The project's [tasks](tasks.md). |

Tools you install into `/usr/local` in the VM stay on the state disk. They
survive restarts and new images. Caches with lots of small files, like Go's
build cache or pip's, live there too. The shared home folder would be slow
for them.

The disk only takes up space on your machine as it fills, up to the size you
set with [`disk`](config.md#disk). The VM formats it on the first boot. It
won't format a disk that has data on it but no ext4 file system, so a broken
disk never gets wiped.

## In your IDE

Claude Code can work with your IDE, like VS Code or GoLand: it sees the
code you select, shows its changes as a diff in the editor and reads the
errors the IDE reports. On your machine the IDE's Claude Code extension or
plugin does this through a server that only listens on your machine. The VM can't reach it, and it
shouldn't, because that server also runs code and opens files anywhere.

aibox can link the two for you. It's off until you turn it on in the
[project config](config.md#ide):

```yaml
ide: true
```

Then open the project in your IDE and start `aibox run` in its terminal.
Claude Code in the VM connects to the IDE by itself. `/ide` shows it as
connected. aibox only passes on what the IDE integration needs, and only for
files in the project:

- the code you select in a project file
- diffs of project files in the editor
- the errors the IDE reports for project files

Everything else stays out. Claude Code can't run code through the IDE, and
it doesn't see what you select in files outside the project.

Note that aibox looks for the IDE when the VM starts. If you open the IDE
later or restart it, start `aibox run` again. Also, the IDE talks to one
Claude Code at a time. Claude Code on your machine and the one in the VM
take the link from each other.

Approve Claude Code's changes in the terminal, and don't type into its diff
in the IDE. When you accept or edit a diff in the IDE, the IDE writes the
file itself, and the VM could have turned it into a link to a file outside
the project. See [What aibox does not protect](security.md#what-aibox-does-not-protect).

aibox has been tested with VS Code and GoLand. Other JetBrains IDEs use the
same plugin and should work too.

### How it works

The IDE extension writes a file to `~/.claude/ide` with its port and a
token. aibox looks there for an IDE that has the project open. It writes a
file like that into the VM's `~/.claude/ide`, with a token of its own and
port 3131, and sets `CLAUDE_CODE_SSE_PORT` so that Claude Code connects by
itself. The init forwards port 3131 to the proxy, and the proxy hands the
connection to aibox's IDE bridge. The bridge connects to the IDE with the
IDE's token, which never gets into the VM.

The bridge reads every message and passes on only the ones it knows, with
`/project` in paths turned into the project's path on your machine and back.
It refuses paths outside the project, also through symlinks, and gives each
request an ID of its own. `proxy.log` shows what it held back.

## On macOS

The VM runs arm64 Linux, so Mac programs don't run in it. If you mount a
tool from your Mac, like a Go SDK or `/nix/store`, the VM can't run it. Get
arm64 Linux builds of your tools instead, or install them into `/usr/local`
in the VM, where they stay.

You can only run one `aibox run` per project at a time on macOS.
Virtualization.framework locks the state disk while the VM runs.

## How it works

The terminal is an SSH session over vsock. Inside the VM, the init runs
Claude Code on a pseudo terminal. aibox connects to it, puts your terminal
into raw mode and attaches it. When you resize your terminal, the VM gets
the new size. When Claude Code exits, aibox exits with the same code.
