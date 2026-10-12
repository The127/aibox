# Running Claude Code

Go to your project folder, let aibox suggest the hosts its tools need, and
start it:

```
cd ~/projects/my-app
aibox config suggest
aibox run
```

The VM only reaches the hosts you allow. `aibox config suggest` looks at the
files in your project, like `go.mod` or `package.json`, and offers the
[presets](config.md#suggesting-presets) for them. It asks before it adds
them.

`aibox run` boots the VM, mounts the project at `/project` and starts Claude
Code there. Log in like you would on your machine. aibox keeps the login for the
next run in this project.

Each project has a VM home of its own, so you log in once for each project.
aibox doesn't copy the login from one project to another. That would mean
aibox handles your login, and Anthropic's terms don't allow anyone but you to
do that.

aibox won't start in your home folder or above it, because the VM would see
all your files.

| Flag | What it does |
|---|---|
| `--shell` | Opens a shell in the VM instead of Claude Code. |
| `--resume <id>` | Goes on with the Claude Code session that has this ID. Claude Code prints the ID when you quit it. |
| `--memory` | Sets the VM's memory in MiB. The default is 2048, or what the config says. |
| `--cpus` | Sets the VM's number of CPUs. The default is 2, or what the config says. |
| `--image` | Boots a folder with `vmlinuz` and `os.ext4` instead of the image your package installed or aibox downloaded. aibox doesn't check its kernel. |
| `--no-sandbox` | Runs QEMU without its bubblewrap sandbox, for debugging. Linux only. |

## What aibox keeps for a project

Each project gets a folder in `~/.aibox/projects`, named after the project's
path. Letters and digits stay, each `/` becomes `-`, and every other
character becomes `_` and a code, so `/work/my-app` gets
`-work-my_2dapp`. A very long name is cut and ends in a hash of the path.
The folder holds:

| File | What's in it |
|---|---|
| `config.yaml` | The [project config](config.md). |
| `home/` | The VM's home folder, so Claude Code's login and `~/.claude` survive restarts. Other projects don't see them. |
| `state.ext4` | A disk for the VM's `/usr/local` and `~/.cache`. |
| `console.log` | The kernel and init messages of the last run. |
| `proxy.log` | Every host and port the proxy connected to or refused, the first time in each run. When the run ends, a summary says how often and when last. A killed aibox writes no summary. |
| `tasks/` | The project's [tasks](tasks.md). |
| `path` | The project's path, for `aibox projects list`. |
| `run.lock`, `tasks.lock` | Locked while the project runs or a task runs, so that a reset or a remove leaves it alone. |

Tools you install into `/usr/local` in the VM stay on the state disk. They
survive restarts and new images. Caches with lots of small files, like Go's
build cache or pip's, live there too. The shared home folder would be slow
for them.

The disk only takes up space on your machine as it fills, up to the size you
set with [`disk`](config.md#disk). The VM formats it on the first boot. It
won't format a disk that has data on it but no ext4 file system, so a broken
disk never gets wiped.

## Starting over

The VM's home folder and state disk stay between runs. That's how your
login and your tools survive. But it also means a session can leave
something behind for the next one. That might be a broken tool or a full
disk, or a line in `~/.claude/CLAUDE.md` that something in the project
slipped in. To start the VM over, run this in the project folder:

```
aibox projects reset
```

This clears the VM's home folder and deletes its state disk. The next run
makes a new disk, at the size [`disk`](config.md#disk) says. Your config,
the logs and the tasks stay, and so does Claude Code's login. Claude Code
asks for your color theme again, since its other settings are gone. Your
old sessions are gone too, so `--resume` can't go back to them.

If you think a session was steered, add `--logout`. Then the login goes
too. The VM can write the login file, so a steered session could have put
someone else's login there, and your next sessions would work in their
account.

aibox asks before it changes anything. Without a terminal, pass `--yes`.
It refuses while the project runs.

## Removing a project

aibox never removes a project's folder on its own. To see the projects it
keeps a folder for, run:

```
aibox projects list
```

To remove everything aibox keeps for a project, run this in the project
folder, or pass the project's path:

```
aibox projects remove
aibox projects remove ~/projects/old-app
```

That removes the config, the VM's home folder with the login, the state
disk, the logs and the tasks. Your project folder itself stays as it is.
This works for a project whose folder you already moved or deleted too.
aibox asks first, and refuses while the project or one of its tasks runs.

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

## Pasting images

Press Ctrl+V in Claude Code to paste the image on your clipboard, like a
screenshot. Pasting text works like in any terminal, and so does copying.

On Windows, press Alt+V, as in Claude Code on Windows. Windows Terminal
and the Windows console paste on Ctrl+V themselves, so the key never
reaches aibox. aibox passes Alt+V on to the VM as Ctrl+V, so Alt+V in
another program in the VM is Ctrl+V there too.

The VM can't read your clipboard whenever it likes. If it could, it would
see every password you copy. aibox lets it read the clipboard only right
after you press Ctrl+V, for 5 seconds, and only one image. If you don't want
that either, set `clipboard: false` in the
[project config](config.md#clipboard).

Note that aibox watches for the key, not for Claude Code. Ctrl+V in another
program in the VM, like bash or vim, lets the VM read the clipboard too. If
you bind image paste in Claude Code to another key, aibox doesn't see it and
pasting images stops working.

On Linux aibox reads the clipboard with `wl-paste` under Wayland and with
`xclip` under X. Install the one you need on your machine. On macOS it uses
`osascript`.

### How it works

aibox reads what you type before it goes to the VM. It looks for Ctrl+V as
the terminal sends it: as a control character, in the kitty keyboard
protocol or with xterm's modifyOtherKeys. Text you paste into the terminal
doesn't count. On Windows it looks for Alt+V the same ways, and sends the
VM Ctrl+V in its place. It does that only when Alt+V comes in one read,
as it does from a terminal: holding back the escape it starts with until
the next read would hold back the Escape key too.

In the VM, `xclip` and `wl-paste` are aibox's init under another name. When
Claude Code runs them for Ctrl+V, they ask aibox on port 3132 of the
loopback. The first asks which types the clipboard holds, the second asks
for the image. They answer nothing else, so Claude Code can't read text from
your clipboard.

Once the VM runs, aibox can't start programs any more. So before the VM
starts, aibox starts a helper, a second aibox. The helper reads the
clipboard when aibox asks and does nothing else. It ends with aibox.

## On macOS

The VM runs arm64 Linux, so Mac programs don't run in it. If you mount a
tool from your Mac, like a Go SDK or `/nix/store`, the VM can't run it. Get
arm64 Linux builds of your tools instead, or install them into `/usr/local`
in the VM, where they stay.

You can only run one `aibox run` per project at a time on macOS.
Virtualization.framework locks the state disk while the VM runs.

## On Windows

The VM runs on Hyper-V. The [service](installing.md#windows) of aibox
starts it for you, because Hyper-V only lets administrators do that; `aibox
run` itself needs no administrator rights. Windows programs don't run in
the VM, which runs amd64 Linux.

You can only run one `aibox run` per project at a time on Windows too.
Hyper-V keeps the state disk open while the VM runs.

The project's folder in `~/.aibox/projects` is named after the path as
Windows spells it, so `cd c:\users\me\repo` and `cd C:\Users\me\repo` are
the same project. A junction, a symlink, a `subst` drive or a mapped
drive leads to the project it points to; on a mapped drive `Z:\repo` that's
`\\server\share\repo`. That's unlike Linux and macOS, where a project you
reach through a symlink is a project of its own.

The state disk, `state.ext4`, is a dynamic VHD on Windows, the kind of
disk Hyper-V attaches, which takes up space as the VM fills it. The VM
sees the same disk as on Linux.

### How it works

aibox asks the service over a named pipe to make a VM with the memory,
the processors and the kernel command line it wants. The service checks
them against what the machine has and boots its own copy of the kernel
and the root disk. It never opens a folder or a file you name: it has
Windows tell it who you are, and gives the VM the state disk you hold
open, and nothing else of yours.

Your project, home and mounts are served to the VM by a helper, a copy of
aibox, over 9p on Hyper-V sockets, as you: the VM can do with them what
you can, and no more. The terminal and the proxy go over the same sockets.
aibox serves the terminal itself, and the proxy from another helper in an
AppContainer. That helper hands the requests for the services of the
proxy, such as the Claude API forwarder and the git broker, to aibox,
which holds their keys, over a port of the loopback. The service lets
the helper reach only that port and those of the allow list. Both
helpers end with aibox. The console
comes over a pipe the service makes for the VM and hands to aibox open:
Hyper-V connects the VM to it, and no one can open it by name. When
aibox ends, or is killed, the service ends the VM.

## How it works

The terminal is an SSH session over vsock. Inside the VM, the init runs
Claude Code on a pseudo terminal. aibox connects to it, puts your terminal
into raw mode and attaches it. When you resize your terminal, the VM gets
the new size. When Claude Code exits, aibox exits with the same code.
