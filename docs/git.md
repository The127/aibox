# Git

The `git` list in the [project config](config.md) names the repositories git
in the VM may fetch from and push to with the git login of
your machine. The login stays on your machine.

```yaml
git:
  - remote: github.com/owner/repo
    fetch: true
    push:
      - aibox/*
      - feature/login
  - remote: github.com/owner/private-module
    fetch: true
```

- `remote` is the host and the path of the repository, without `https://`
  and without `.git`.
- `fetch: true` allows clone and fetch.
- `push` lists the branches a push may create or update. `*` stands for any
  part of a name without a `/`, so `aibox/*` allows `aibox/fix` but not
  `aibox/a/b`, and `*` alone allows `main` too. aibox refuses pushes to
  other branches, deletes, tags, and pushes from a shallow clone. When one
  branch of a push is refused, the whole push is.
- aibox refuses repositories the list does not name.

git in the VM uses the repositories as usual, by the addresses
`https://HOST/PATH`, `git@HOST:PATH` and `ssh://git@HOST/PATH`: `git clone`,
`git fetch` and `git push`. aibox sends these requests to a broker on your
machine, which adds the login and passes on what the list allows. When it
refuses, git in the VM shows why. The broker writes what it refused, and each
push it passed on, into `proxy.log`. The host does not need to be on the
allow list for git. `go get` of a private module works with `GOPRIVATE` set
in `env`. For a host other than GitHub, Go first asks the host over HTTPS
where the module is, so that host needs to be on the allow list too.

## SSH

Before the VM starts, aibox asks git on your machine for the login of each
repository, as `git push` over HTTPS would. When git has none, the broker
talks to the server over SSH instead, as `git@HOST`, and aibox says so when
it starts:

- aibox asks `ssh -G` how ssh would reach the host, so `HostName`, `Port`,
  `IdentityFile`, `IdentitiesOnly`, `IdentityAgent`, `UserKnownHostsFile`,
  `GlobalKnownHostsFile` and `HostKeyAlias` of your `~/.ssh/config` apply.
  It offers the keys ssh would offer, of those in your SSH agent and those
  in a key file without a passphrase. A key with a passphrase works through
  the agent, which has to keep running while aibox does. aibox refuses a
  host that the config reaches with `ProxyJump` or `ProxyCommand`.
- The known hosts check the server. A server that is not in them is
  refused, so connect once with `ssh` to add it.
- The broker logs in once and runs the requests of git in the VM over that
  connection, at most four at a time.

aibox stops when neither way works. It reads the logins and keys once, so
restart aibox when one expires. For SSH, aibox keeps a connection to your
SSH agent open while it runs, and may connect to the port of the SSH server.
A key of its own for aibox, or an agent that asks before it signs, such as
with `ssh-add -c`, limits what a fault in aibox could do with it.

## What the broker cannot stop

- A force push to a branch the list allows. The broker sees which branches a
  push updates, but not whether it drops commits. Protect the branches that
  matter on the server, for example with branch protection on GitHub.
- Code the VM pushes is code you have not read. A pushed branch can start
  the CI of the repository, with changes to its workflows and with its
  secrets.
- With `push` alone and no `fetch`, the VM still sees the names of all
  branches and tags and the commits they point to, since git needs them to
  push. It does not get the files.

With `git`, aibox sets `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_*` and
`GIT_CONFIG_VALUE_*` in the VM, so `env` cannot set them.

## How the broker works

- aibox asks git on your machine for the login of each repository before the
  VM starts. It runs git outside any repository and without a terminal, so
  the config of the project, which the VM can write, has no say.
- git in the VM gets a config that sends the HTTPS and SSH addresses of each
  repository to `http://127.0.0.1:3130`. The init forwards that port to the
  proxy as it does the ports of the allow list, and the proxy serves the
  broker there.
- The broker speaks the smart HTTP protocol of git. It accepts only the
  requests of git, only for the repositories of the list, and only without
  dot segments or escapes in the path.
- It sends each request to `https://HOST/PATH.git`, which the config fixes
  and not the request. It adds the login of the host and drops the
  credentials and cookies of the VM. An answer that refuses the login or
  redirects becomes an error for the VM, so git in the VM never asks for a
  login of its own.
- It refuses a fetch the config does not allow.
- Of a push the broker reads the commands at the start, which name each ref
  with its old and new commit. It passes the push on only when each command
  updates or creates a branch of the list. Otherwise it refuses all of them,
  since the pack belongs to the whole push. It also refuses deletes, tags,
  signed pushes, push options and pushes from a shallow clone, and answers
  in the format of git, so that git in the VM shows why. A push larger than
  the buffer of git starts with a probe that is a flush alone, which the
  broker passes on.

Over SSH:

- aibox asks `ssh -G` for the host name, the port, the key files, the agent
  and the known hosts ssh would use, and reads the known hosts and the keys
  before the VM starts. The port of the server joins the ports aibox may
  connect to.
- The broker asks the server only for the types of key the known hosts
  hold for it.
- It logs in once per repository and opens a session on that connection for
  each request, at most four at a time. It logs in again when the
  connection broke.
- HTTP is stateless and SSH is not, so for each request of git in the VM
  the broker runs the service on the server once. For the advertisement it
  passes on what the server advertises, with the line that names the
  service over HTTP, and then ends the service. For a request it skips the
  advertisement, sends the body, decompressed when git compressed it for
  HTTP, and passes on the answer.
- git in the VM sends `Git-Protocol`, which the broker passes on as
  `GIT_PROTOCOL`, so that a fetch runs in version 2.
- Logging in, opening and closing a session, the advertisement and each
  silence in an answer have a time limit. A request git in the VM gives up
  on ends its session. What the server writes to its standard error goes
  into the error and the log.
- The checks of a push are the same as over HTTPS.
- A path on the server that starts with `-` or `~` is refused in the
  config, since the server would read it as an option or a home folder.
