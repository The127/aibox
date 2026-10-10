# Tasks

A task is a job Claude Code does on its own while you do something else. It
runs in a fresh VM on a clone of the last commit. When it's done, you get its
commits back as a git bundle to review, and the VM is deleted.

```
aibox task --model sonnet "fix the flaky test in internal/proxy"
```

## What a task gets

The VM gets a clone of `HEAD` with its full history, not your project
folder. Use `--from` to start from another branch, tag or commit instead.

Note that whatever `--from` names goes in with its full history. That
includes a stash or a commit you reset away. A file you deleted in an
earlier commit is still in that history.

Changes you haven't committed, files git ignores, and branches and stashes
that `--from` doesn't name stay out of the task. aibox refuses a shallow
clone, because it lacks the history.

Claude Code works on the branch `aibox/task`. It has all permissions,
because nobody is there to approve them. Git uses your name and email from
your git config. If Claude Code leaves changes uncommitted, aibox commits
them in one more commit by `aibox <aibox@localhost>`. Files git ignores are
left out.

The VM starts with an empty home folder and an empty disk, and both are
deleted at the end. Your skills in `~/.claude/skills` are mounted read-only,
like with `aibox run`, unless the config says `skills: none`. Other tasks
and `aibox run` never see the home or the disk, so you can run several tasks
at once.

The project config applies like it does for `aibox run`: `allow`, `mounts`,
`skills`, `path`, `env`, `git`, `memory`, `cpus` and `disk`. The task gets
its own `proxy.log`. It doesn't write to the project's.

## Before you run a task

A task can use everything the config gives it, and nobody stops it. It can
reach every host on the allow list and every port of your machine the list
names. It can use every secret in `env`. With `GITHUB_TOKEN` and
`preset:github`, for example, it can push to your repositories.

Claude Code is told to push or open a pull request only when your prompt
asks for it, only to a repository your prompt names by URL, and only to a
new branch. Nothing enforces that. A pushed branch or pull request can start
the repository's CI, with its secrets.

Text Claude Code reads, in the project or on the internet, can steer it.
When it says what it published, that's only its word. aibox lists the hosts
the proxy connected to, but not what went through those connections. A push
to `github.com` and a fetch from it look the same.

So before you run a task, remove the hosts, ports and secrets it doesn't
need from the config. To let a task fetch and push without a token in the
VM, and only to branches you name, use [git](git.md) in the config.

## Flags

| Flag | What it does |
|---|---|
| `--from` | The branch, tag or commit to start from. The default is `HEAD`. |
| `--file`, `-f` | Adds the text of a file to the prompt, after the arguments. `-` reads stdin. |
| `--model` | The model Claude Code uses. Without it, Claude Code uses its default. |
| `--max-turns` | The most turns Claude Code takes. |
| `--max-budget-usd` | The most Claude Code may spend by its own estimate, in US dollars at API prices. See [Cost](#cost). |
| `--timeout` | How long Claude Code may work before it's stopped. 1h by default, up to 30 days. |

`--memory`, `--cpus`, `--image` and `--no-sandbox` work like they do for
`aibox run`.

## Prompt from a file

You can use a plan you wrote as the prompt. `--file` adds the text of a file
after the arguments, and `--file -` reads stdin. Without any arguments, aibox
reads the prompt from stdin, unless stdin is a terminal.

```
aibox task --file plan.md
aibox task --file plan.md "do only step 2"
cat plan.md | aibox task
```

A few rules apply:

- A relative path, and every link in it, has to stay inside the folder you
  run aibox in. That way a link in a repository can't make a task read some
  other file of yours. For a plan somewhere else, give an absolute path. An
  absolute path must not end in a link.
- The file has to be a plain file. For a pipe, use `--file -`.
- The prompt can be up to 1 MiB of UTF-8 text, the arguments and the file
  together.

A plan you haven't committed only reaches the task as its prompt.
It's not a file in `/project`.

## Login

A task starts with an empty home folder, so Claude Code is not logged in.
You give it a login through an environment variable instead. Add one of
these to [`env`](config.md#env) in the project config:

- `ANTHROPIC_API_KEY` for an API key from the Claude Console. Usage is billed
  to the key.
- `CLAUDE_CODE_OAUTH_TOKEN` for your claude.ai subscription. Run
  `claude setup-token` to make one.

```yaml
env:
  - CLAUDE_CODE_OAUTH_TOKEN
```

aibox never stores the token or the key. It takes the value from your shell
when the task starts.

### API key

Your API key never enters the VM. Claude Code gets a placeholder key and an
`ANTHROPIC_BASE_URL` that points to a local port. aibox listens there, swaps
in the real key and forwards the request to `api.anthropic.com`. So nothing
in the VM can read the key, send it somewhere or put it into a commit.

But while the task runs, it can still spend the key through aibox, on any
request to the Claude API. Give the key a spend limit in the Console.

This only works for programs that use `ANTHROPIC_BASE_URL`. Other
programs in the VM only get the placeholder. aibox refuses a config that
sets `ANTHROPIC_BASE_URL` itself next to the key.

An interactive Claude Code still connects to `api.anthropic.com` and
`platform.claude.com` directly when it starts, without the key. Keep both on
the allow list, like a new config has them.

### Subscription token

A subscription token goes into the VM as it is, because Anthropic's terms
don't allow anyone but you to handle it. In the VM, Claude Code and every
program the task runs can read it. If something steers the task, it can send
the token to a host on the allow list, or write it into a commit or the
transcript. Use a separate token or key just for tasks, so you can revoke it
on its own.

Whether running unattended tasks is fine on your subscription is your own
risk. See Anthropic's
[Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance)
page. For lots of tasks, or tasks for a team, use an API key.

### How the key forwarding works

The port is `127.0.0.1:3129`. The proxy answers there itself and doesn't
connect anywhere for it. It forwards each request under `/v1/`, and the
check Claude Code makes at `/api/hello`, to `https://api.anthropic.com`, no
matter which host the request names. It refuses paths with dot segments or
escapes. It drops any credentials the VM sent and adds the key. `proxy.log`
shows these requests as `localhost:3129`.

aibox reads the root certificates before it locks itself down. After that,
its sandbox stops it from reading them, and on macOS from asking the system
to check a certificate.

## Results

When the task ends, aibox prints the hosts the VM connected to, how the task
went, Claude Code's last message and the command that fetches the changes
into a branch:

```
17:09:25 aibox[259e07]: the VM connected to api.anthropic.com:443
17:09:25 aibox[259e07]: Claude Code exited with 0
17:09:25 aibox[259e07]: 4 turns in 8.321s, about 0.05 USD at API prices, ended by completed
17:09:25 vm[259e07]: | I added a "Purpose" section to the README and committed it as f3572af.
17:09:25 aibox[259e07]: the changes end at f3572af8c6df, fetch them with
17:09:25 aibox[259e07]:   git -c transfer.fsckObjects=true fetch .../changes.bundle aibox/task:aibox/task-20261007-170916-259e07
```

Each line starts with the time, where the line comes from and the end of the
task's ID. That way you can tell tasks apart when they share a log. `vm`
lines come from the VM and are only what the task claims. `aibox` lines come
from aibox on your machine.

### Reviewing the commits

Treat the commits like a pull request from a stranger until you've read
them. The authors on them prove nothing, because the task can set any
author.

1. Fetch them with the command aibox prints. aibox only prints it after
   checking that the bundle has `aibox/task` and nothing else, and needs no
   commit other than the one the task started from. The fetch only adds
   that one branch.
2. Read the whole change with `git diff <commit> aibox/task-…`, no matter
   what the single commits look like. Take `<commit>` from the line
   `aibox[…]: task … starts from …` that aibox prints. Don't take it from
   `result.json`, because the VM writes that file. The printed commit is the
   one aibox checked the bundle against.

Checking out, merging or rebasing the branch puts its files into
your project folder. From then on, everything in
[What the VM writes](what-the-vm-writes.md) applies.

### Cost

The cost aibox prints is Claude Code's own estimate, from the tokens it used
at the API's list prices. With an API key it's close to what you're billed.
With a subscription you aren't billed this amount. The task counts against
your plan's limits instead.

`--max-budget-usd` caps the same estimate, for Claude Code alone. Programs
the task starts, like another `claude`, don't count. The only limit on time
or spending that aibox enforces itself is `--timeout`. With an API key, give
the key a spend limit in the Console.

### The task folder

Each task has a folder `tasks/<start time>-<id>` in the project's folder in
`~/.aibox/projects`. aibox prints the path on stdout, for scripts.

| File | What's in it |
|---|---|
| `changes.bundle` | The commits. Empty when the task made none. |
| `result.json` | What the VM reports: the commit the task started from and the one it ended at, Claude Code's exit code, whether it ran out of time, whether aibox committed leftovers, the files that were cut, warnings, the error of a failed step, and Claude Code's last line with the cost and the turns. |
| `transcript.jsonl` | Everything Claude Code did, as `stream-json`, cut at 256 MiB. |
| `claude.log` | What Claude Code wrote to standard error, cut at 1 MiB. |
| `console.log` | The kernel and init messages of the VM. |
| `proxy.log` | Every host and port the proxy connected to or refused for the task, once, with the time it first happened. aibox writes this file, not the VM. It shows the name the VM asked for, not what went through. |
| `share/` | The prompt, the settings and the bundle of the start commit with its full history, until `aibox tasks clean` removes it. |
| `lock` | Locked while the task runs, so `aibox tasks clean` leaves the task alone. |

aibox exits with 1 when the task didn't finish. That happens when Claude
Code failed or ran out of time, a step in the VM failed, the VM was stopped,
or the VM sent results aibox doesn't accept. The folder then has whatever
the task left behind.

## Removing old tasks

Every task keeps its folder, and with it a bundle of the project's history.
aibox never removes anything on its own. To clean up, run:

```
aibox tasks clean
```

This removes the bundles of the tasks that ended and keeps their results.
Running tasks are left alone.

```
aibox tasks clean --all --older-than 720h
```

`--all` removes the whole folders of ended tasks, results included.
`--older-than` only cleans tasks that started longer ago than that. The
command also removes what's left of tasks that failed while starting. If it
can't check or clean a folder, it names it and exits with 1.

A task counts as ended once its aibox process ends. The VM ends with aibox,
even when aibox is killed.

## How a task runs

`aibox task` boots the same VM as `aibox run`, but without a terminal. The
kernel command line tells the init to run a task.

First, aibox writes the prompt, the settings and a bundle of the start
commit into the task's folder. The VM gets that folder as a read-only share.
git can only bundle refs, so aibox makes the bundle from an empty repository
that borrows the project's objects and has the commit as its `HEAD`. Your
project gets no new ref.

Your project folder and the project's home folder are not shared with the
task. The mounts from the config and your skills are, read-only like with
`aibox run`. `skills: none` keeps your skills out.

Each task gets a new state disk, which the init formats. The home folder and
the project are folders on that disk. aibox deletes the disk's file as soon
as the VM has opened it, so the disk is gone when the VM is. Tasks don't
lock the project, so they can run next to each other and next to
`aibox run`.

Then the init clones the bundle, removes the remote and switches to the
branch `aibox/task`. It runs `claude --print` as the user, with
`bypassPermissions`, `stream-json` output and
[`image/task.md`](https://github.com/The127/aibox/blob/main/image/task.md)
as an extra system prompt. Finally it commits whatever is left and bundles
the branch.

aibox runs its git commands with hooks and fsmonitor turned off, because the
repository came from the task. Filters the task sets up still run, but only
inside the VM.

Each step runs in its own cgroup. When the step ends, the init kills the
cgroup, so nothing a step started outlives it. The git steps before Claude
Code get 10 minutes together, and the ones after get another 10. Claude Code
gets the task's timeout. aibox stops the VM at the latest 25 minutes after
the timeout.

The results come back over the SSH session, as a tar on its standard output.
The init sends each file once it's complete, and `result.json` last. Progress
goes over standard error.

aibox checks what comes back:

- It only accepts files it knows by name. Each one has to be a plain file,
  sent once, and within a size limit. If aibox refuses the results, it stops
  the VM.
- It reads the bundle's header without git. It checks that the bundle has
  `aibox/task` and nothing else, and needs no commit other than the start
  commit.
- It strips control characters from text the VM sends before it reaches
  your terminal. It adds the time, the source and the end of the ID to each
  line from the VM itself, so a line from the VM can't pretend to be from
  aibox. `aibox run` passes the VM's terminal straight through, so this only
  applies to tasks.

aibox opens the result files before the VM starts. Once the VM runs, aibox
locks itself down and can't open files anymore. For the same reason a task
can't remove its input bundle when it ends, so `aibox tasks clean` does that
later. Each task locks a file in its folder while it runs, and the folder
only gets its name once it holds the lock. That's how `aibox tasks clean`
knows to leave a running task alone.
