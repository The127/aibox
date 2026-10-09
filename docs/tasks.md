# Tasks

`aibox task` runs Claude Code unattended on the last commit of the project,
in a VM that is thrown away afterwards. The commits it makes come back as a
git bundle, for you to review.

```
aibox task --model sonnet "fix the flaky test in internal/proxy"
```

- The VM gets a clone of `HEAD` with all its history, not your folder.
  `--from` names another branch, tag or commit to start from. Whatever it
  names goes into the VM with all its history, a stash or a commit you reset
  away too. A file you deleted in an earlier commit is still in that
  history. Changes you have not committed, files git ignores, and other
  branches and stashes that `--from` does not name are not part of the task.
  A shallow clone is refused, since it lacks the history.
- Claude Code works on the branch `aibox/task` with all permissions, since
  nobody is there to approve them. Git has your name and email from your git
  config. What Claude Code leaves uncommitted, apart from files git ignores,
  aibox commits in one more commit by `aibox <aibox@localhost>`.
- The VM starts with an empty home and an empty disk, and both are thrown
  away at the end. Your skills in `~/.claude/skills` are mounted read-only,
  as for `aibox run`. Other tasks and `aibox run` never see the home or the
  disk, so several tasks can run at once.
- The project config applies as for `aibox run`: `allow`, `mounts`, `path`,
  `env`, `git`, `memory`, `cpus` and `disk`. The hosts the proxy connected to or
  refused go into a `proxy.log` of the task, not into the one of the
  project.

A task can use everything the config gives it, and nobody stops it. It can
reach every host on the allow list, and every port of your machine that the
allow list names, and it can use every secret of `env`. With `GITHUB_TOKEN`
and `preset:github`, for example, it can push to your repositories. Claude
Code is told to push or open a pull request only when your prompt asks for
it, only to a repository whose URL your prompt names, and only to a new
branch, but nothing enforces that. A pushed branch or pull request can start
the CI of the repository, with its secrets. What Claude Code says it
published is its own word. aibox names the hosts the proxy connected to, but
not what went through those connections, so a push to `github.com` and a
fetch from it look the same. Text it reads, in the project or from the
network, can steer it. So before you run a task, take out of the config the
hosts, ports and secrets the task does not need. [git](git.md) in the config
lets a task fetch and push without a token in the VM, and only to the
branches you name.

| Flag | Meaning |
|---|---|
| `--from` | The branch, tag or commit the task starts from, `HEAD` by default. |
| `--file`, `-f` | A file whose text follows the arguments in the prompt, `-` for stdin. |
| `--model` | The model Claude Code uses. Without it, Claude Code picks its default. |
| `--max-turns` | The most turns Claude Code takes. |
| `--max-budget-usd` | The most Claude Code may spend by its own estimate, in US dollars at API prices. See below. |
| `--timeout` | How long Claude Code may work before it is stopped, 1h by default, at most 30 days. |

`--memory`, `--cpus`, `--image` and `--no-sandbox` work as for `aibox run`.

## Prompt from a file

A plan you wrote can be the prompt. `--file` puts the text of a file after
the arguments, and `--file -` reads stdin. Without arguments, aibox reads
the prompt from stdin, unless stdin is a terminal.

```
aibox task --file plan.md
aibox task --file plan.md "do only step 2"
cat plan.md | aibox task
```

- A relative path, and every link in it, must stay inside the folder you
  run aibox in, so that a link in a repository cannot make a task read
  another file of yours. For a plan elsewhere, give an absolute path. An
  absolute path must not end in a link.
- The file must be a plain file. For a pipe, use `--file -`.
- The prompt may be 1 MiB of UTF-8 text, the arguments and the file
  together.
- A plan that is not committed reaches the task only as its prompt, not as
  a file in `/project`.

## Login

The empty home has no login, so Claude Code logs in with a variable that the
[`env`](config.md#env) of the project config passes from your shell:

- `ANTHROPIC_API_KEY`, a key of the Claude Console. Usage is billed to it.
- `CLAUDE_CODE_OAUTH_TOKEN`, a token of your claude.ai subscription that
  `claude setup-token` makes.

```yaml
env:
  - CLAUDE_CODE_OAUTH_TOKEN
```

aibox never stores the token or the key. It takes the value your shell has
when the task starts.

An API key stays on your machine. The VM gets a placeholder in its place,
and `ANTHROPIC_BASE_URL` points Claude Code at a port on the loopback of the
VM. Behind that port, aibox sends each request to `api.anthropic.com` with
the real key. So nothing in the VM can read the key, send it away or write
it into a commit. But while the task runs, it can spend the key through
aibox, for any request to the Claude API. Give the key a spend limit in the
Console. An interactive Claude Code still reaches `api.anthropic.com` and
`platform.claude.com` directly when it starts, without the key, so keep them
on the allow list as a new config has them. Programs other than Claude Code
get the placeholder too, and work only if they follow `ANTHROPIC_BASE_URL`.
aibox refuses a config that sets `ANTHROPIC_BASE_URL` itself next to the
key.

The port is `127.0.0.1:3129`. The proxy answers on it itself and dials
nothing for it. It sends each request under `/v1/`, and the check Claude
Code makes at `/api/hello`, to `https://api.anthropic.com`, whatever host the
request names. It refuses a path with dot segments or escapes, drops the
credentials the VM sent and adds the key. `proxy.log` shows these requests
as `localhost:3129`. aibox reads the root certificates for this before it
confines itself.

A subscription token goes into the VM as it is, since Anthropic's terms let
no one but you handle it. In the VM, Claude Code and every program the task
runs can read it. A task that text has steered can send it to a host of the
allow list, or write it into a commit or the transcript. Use a token or key
for tasks only, so that you can revoke it on its own.

Whether unattended tasks are fine on your subscription is your own risk. See
Anthropic's [Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance)
page. For many tasks, or tasks for a team, use an API key.

## Results

At the end aibox prints the hosts the VM connected to, how the task went,
the last message of Claude Code and the command that fetches the changes
into a branch of your repository:

```
17:09:25 aibox[259e07]: the VM connected to api.anthropic.com:443
17:09:25 aibox[259e07]: Claude Code exited with 0
17:09:25 aibox[259e07]: 4 turns in 8.321s, about 0.05 USD at API prices, ended by completed
17:09:25 vm[259e07]: | I added a "Purpose" section to the README and committed it as f3572af.
17:09:25 aibox[259e07]: the changes end at f3572af8c6df, fetch them with
17:09:25 aibox[259e07]:   git -c transfer.fsckObjects=true fetch .../changes.bundle aibox/task:aibox/task-20261007-170916-259e07
```

Every line of a task starts with the time, where the line comes from and the
end of the ID of the task, so that tasks that share a log can be told apart.
`vm` lines come from the VM and say what the task claims. `aibox` lines come
from the host.

The commits are untrusted until you have read them, like a pull request from
a stranger. Their authors prove nothing, since the task can set any author.

- Fetch them with the command aibox prints. aibox prints it only after it
  checked that the bundle carries `aibox/task` and nothing else, and needs no
  commit but the one the task started from. The fetch only adds that branch.
- Read the whole change with `git diff <commit> aibox/task-…`, whatever
  the single commits look like. Take `<commit>` from the line aibox
  prints, `aibox[…]: task … starts from …`, not from `result.json`, which the
  VM writes. That is the commit aibox checked the bundle against.
- Checking out, merging or rebasing the branch puts its files into your
  folder. Then everything in [What the VM writes](what-the-vm-writes.md)
  applies.

The cost is Claude Code's own estimate, from the tokens it used at the list
prices of the API. With an API key it is close to what you are billed. With
a subscription you are not billed this amount, and the task counts against
the limits of your plan instead. `--max-budget-usd` caps the same estimate,
for Claude Code alone. Programs the task starts, such as another `claude`,
do not count. The only limit on time or spending that aibox enforces itself is `--timeout`. With an API key,
give the key a spend limit in the Console.

Each task has a folder `tasks/<start time>-<id>` in the folder of the
project below `~/.aibox/projects`. aibox prints the folder on stdout, for
scripts.

| File | What it holds |
|---|---|
| `changes.bundle` | The commits, empty when the task made none. |
| `result.json` | What the VM reports: the commit the task started from and the one it ended at, the exit code of Claude Code, whether it ran out of time, whether aibox committed leftovers, the files that were cut, warnings, the error of a failed step, and the last line of Claude Code with the cost and the turns. |
| `transcript.jsonl` | Everything Claude Code did, as `stream-json`, cut at 256 MiB. |
| `claude.log` | What Claude Code wrote to standard error, cut at 1 MiB. |
| `console.log` | The messages of the kernel and the init of the VM. |
| `proxy.log` | Each host and port the proxy connected to or refused for the task, once, with the time it first did. aibox writes it, not the VM. It shows the name the VM asked for, not what went through. |
| `share/` | The prompt, the settings and the bundle of the commit the task started from, with all its history, until `aibox tasks clean` removes it. |
| `lock` | Locked while the task runs, so that `aibox tasks clean` leaves the task alone. |

aibox exits with 1 when the task did not finish. Claude Code failed or ran
out of time, a step in the VM failed, the VM was stopped, or the VM sent
results aibox does not take. The folder then holds what the task left.

## Removing old tasks

Every task keeps its folder, and with it a bundle of the history of the
project. aibox removes nothing on its own. `aibox tasks clean` removes the
bundles of the tasks that ended and keeps their results. Tasks that run stay
as they are.

```
aibox tasks clean
aibox tasks clean --all --older-than 720h
```

`--all` removes the whole folders of the tasks that ended, results
included. `--older-than` limits it to the tasks that started longer ago.
The command also removes what is left of tasks that failed as they started.
It names every folder it could not check or clean and then exits with 1.
A task counts as ended once its aibox ends. On macOS the VM ends with
aibox, even when aibox is killed. On Linux it should too, but that is not
yet tested with the real virtiofsd.

## How a task runs

`aibox task` boots the same VM as `aibox run`, without a terminal. The kernel
command line tells the init that it runs a task.

- aibox writes the prompt, the settings and a bundle of the commit the task
  starts from into the folder of the task. The VM gets that folder as a
  read-only share. Since git bundles refs only, the bundle comes from an
  empty repository that borrows the objects of the project and has the
  commit as its `HEAD`. The project gets no new ref.
- The project folder and the home of the project are not shared. The mounts
  of the config and your skills are, read-only as for `aibox run`.
- Each task gets a new state disk, which the init formats, and the home and
  the project are folders on it. aibox removes the file of the disk as soon
  as the VM has it open, so the disk is gone when the VM is. Tasks do not
  lock the project, so they run next to each other and next to `aibox run`.
- The init clones the bundle, removes the remote and switches to the branch
  `aibox/task`. It runs `claude --print` as the user, with
  `bypassPermissions`, `stream-json` output and
  [`image/task.md`](https://github.com/The127/aibox/blob/main/image/task.md)
  as an extra system prompt. Then it commits what is left and makes a bundle
  of the branch.
- The git steps of aibox run with hooks and fsmonitor off, since the
  repository came from the task. Filters the task sets up still run, inside
  the VM.
- Each step runs in a new cgroup, and the init kills that cgroup when the
  step ends, so nothing a step started outlives it. The git steps before
  Claude Code have 10 minutes together, and the ones after it have 10 more.
  Claude Code has the timeout of the task. aibox stops the VM 25 minutes
  after the timeout at the latest.
- The results come back over the SSH session, as a tar on its standard
  output. The init sends each file once it is complete, and `result.json`
  last. Progress goes over its standard error.
- aibox accepts only files it knows by name. Each must be a plain file, sent
  once, and within a size limit. When aibox refuses the results, it stops
  the VM.
- aibox reads the header of the bundle without git, and checks that it
  carries `aibox/task` and nothing else and needs no commit but the one the
  task started from.
- aibox cleans the text from the VM of control characters before it reaches
  your terminal. It puts the time, the source and the end of the ID in front
  of each line from the VM itself, so a line from the VM cannot pass for a
  line of aibox. `aibox run` passes the terminal of the VM through, so this
  holds only for tasks.
- aibox opens the files of the results before the VM starts, since it
  confines itself once the VM runs and can open no file then. For the same
  reason a task cannot remove the bundle of its input when it ends, and
  `aibox tasks clean` does it later. Each task locks a file in its folder
  while it runs, and its folder gets its name only once it holds the lock,
  so `aibox tasks clean` leaves a running task alone.
