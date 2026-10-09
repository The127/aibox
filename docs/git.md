# Git

Claude Code often needs to fetch from or push to a repository. You could put
a token into `env`, but then everything in the VM can read it and use it for
anything the token allows.

The `git` list in the [project config](config.md) is the safer way. It names
the repositories git in the VM can fetch from and push to, using the git
login on your machine. The login never enters the VM.

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

- `remote` is the host and path of the repository, without `https://` and
  without `.git`.
- `fetch: true` allows clone and fetch.
- `push` lists the branches a push can create or update. `*` matches any
  part of a name without a `/`. So `aibox/*` allows `aibox/fix` but not
  `aibox/a/b`, and `*` on its own allows `main` too.

aibox refuses pushes to other branches, deletes, tags, and pushes from a
shallow clone. If one branch of a push is refused, the whole push is. aibox
also refuses repositories the list doesn't name.

## Using it

In the VM, use git like you normally would. `git clone`, `git fetch` and
`git push` work with the addresses `https://HOST/PATH`, `git@HOST:PATH` and
`ssh://git@HOST/PATH`.

aibox sends these requests to a broker on your machine. The broker adds your
login and passes on what the list allows. When it refuses something, git in
the VM shows you why. The broker writes what it refused, and every push it
passed on, into `proxy.log`.

The host doesn't need to be on the allow list for git.

To `go get` a private module, set `GOPRIVATE` in `env`. For hosts other than
GitHub, Go first asks the host over HTTPS where the module is, so that host
needs to be on the allow list too.

## SSH

Before the VM starts, aibox asks git on your machine for the login of each
repository, the same way `git push` over HTTPS would. If git has none, the
broker talks to the server over SSH instead, as `git@HOST`. aibox tells you
when it does that.

aibox asks `ssh -G` how ssh would reach the host. So these settings from
your `~/.ssh/config` apply: `HostName`, `Port`, `IdentityFile`,
`IdentitiesOnly`, `IdentityAgent`, `UserKnownHostsFile`,
`GlobalKnownHostsFile` and `HostKeyAlias`.

It offers the same keys ssh would, from your SSH agent and from key files
without a passphrase. A key with a passphrase works through the agent, which
has to keep running while aibox does. aibox refuses a host that your config
reaches through `ProxyJump` or `ProxyCommand`.

Your known hosts check the server. aibox refuses a server that isn't in
them, so connect to it with `ssh` once to add it.

The broker logs in once and runs git's requests from the VM over that
connection, at most four at a time.

If neither HTTPS nor SSH works, aibox stops. It reads the logins and keys
only once, so restart aibox when one expires.

For SSH, aibox keeps a connection to your SSH agent open while it runs, and
can connect to the SSH server's port. To limit what a bug in aibox could do
with that, give aibox its own key, or use an agent that asks before it
signs, like with `ssh-add -c`.

## What the broker can't stop

The broker can't stop a force push to a branch you allowed. It sees which
branches a push updates, but not whether the push drops commits. Protect the
branches that matter on the server, for example with branch protection on
GitHub.

What the VM pushes is code you haven't read. A pushed branch can start the
repository's CI, with changes to its workflows and with its secrets.

With `push` but no `fetch`, the VM still sees the names of all branches and
tags and the commits they point to, because git needs them to push. It
doesn't get the files.

With `git`, aibox sets `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_*` and
`GIT_CONFIG_VALUE_*` in the VM, so you can't set them in `env`.

## How the broker works

aibox asks git on your machine for each repository's login before the VM
starts. It runs git outside any repository and without a terminal. That way
the project's git config, which the VM can write, has no say.

git in the VM gets a config that sends the HTTPS and SSH addresses of each
repository to `http://127.0.0.1:3130`. The init forwards that port to the
proxy like the ports on the allow list, and the proxy serves the broker
there.

The broker speaks git's smart HTTP protocol. It only accepts git's requests,
only for the repositories on the list, and only without dot segments or
escapes in the path. It sends each request to `https://HOST/PATH.git`, where
the address comes from the config and not from the request. It adds the
host's login and drops the VM's credentials and cookies. When the server
refuses the login or redirects, the VM gets an error. That way git in the VM
never asks for a login of its own. The broker also refuses a fetch the
config doesn't allow.

For a push, the broker reads the commands at the start. They name each ref
with its old and new commit. The broker only passes the push on when every
command updates or creates a branch on the list. Otherwise it refuses all of
them, because the pack belongs to the whole push. It also refuses deletes,
tags, signed pushes, push options and pushes from a shallow clone. It
answers in git's format, so git in the VM shows why. A push larger than
git's buffer starts with a probe that is just a flush, which the broker
passes on.

### Over SSH

Before the VM starts, aibox asks `ssh -G` for the host name, port, key
files, agent and known hosts ssh would use. It reads the known hosts and the
keys. The server's port gets added to the ports aibox may connect to.

The broker only asks the server for the key types your known hosts have for
it. It logs in once per repository and opens a session on that connection
for each request, at most four at a time. If the connection breaks, it logs
in again.

HTTP is stateless and SSH isn't. So for each request from git in the VM, the
broker runs the service on the server once:

- For the advertisement, it passes on what the server advertises, with the
  line that names the service over HTTP, and then ends the service.
- For a request, it skips the advertisement, sends the body and passes on
  the answer. If git compressed the body for HTTP, the broker decompresses
  it first.

git in the VM sends `Git-Protocol`, which the broker passes on as
`GIT_PROTOCOL`, so a fetch runs in protocol version 2. The checks of a push
are the same as over HTTPS.

Logging in, opening and closing a session, the advertisement and every
pause in an answer have a time limit. When git in the VM gives up on a
request, the broker ends its session. What the server writes to standard
error goes into the error and the log.

A path on the server that starts with `-` or `~` isn't allowed in the
config, because the server would read it as an option or a home folder.
