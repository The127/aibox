# Security

aibox runs Claude Code, which a prompt or the content of a repository can
steer. So aibox treats everything in the VM as untrusted: Claude Code, the
programs it runs and what they write.

## What aibox protects

- **Your files outside the project.** The VM sees the project folder, the
  folders `mounts` names, read-only, and your skills in `~/.claude/skills`,
  read-only. It sees no other files of yours. aibox refuses to start in your
  home folder or above it.
- **The network.** The VM has no network card. It reaches only the hosts on
  the allow list, through a proxy on your machine. A name that resolves into
  the host's own networks is refused, unless the list names that address
  itself. A port on the loopback of your machine is reached only when the
  list names it. See [allow](config.md#allow).
- **An API key.** `ANTHROPIC_API_KEY` stays on your machine. The VM gets a
  placeholder, and aibox adds the key to the requests that go to the Claude
  API. See [Login](tasks.md#login).
- **Your git login.** The login stays on your machine. A broker adds it to
  the requests of git in the VM, and only for the repositories and branches
  the config names. See [Git](git.md).
- **Your machine, from the programs that run the VM.** On Linux QEMU runs in
  a bubblewrap sandbox, and aibox confines itself with Landlock and seccomp.
  On macOS aibox confines itself with Seatbelt.
  [The host side](design.md#the-host-side) says how.
- **The VM image.** aibox takes only the image whose SHA-256 its release
  built into it.

## What aibox does not protect

- **What the VM writes into the project.** That includes `.git`, and files
  that tools on your machine run without asking. Read
  [What the VM writes](what-the-vm-writes.md).
- **What you allow.** The VM can reach every host of the allow list and
  every port of your machine the list names, use every secret of `env`, and
  push to every branch the `git` list names. A subscription token goes into
  the VM as it is, so the VM can read it. A task uses all of this with
  nobody there to stop it. See [Tasks](tasks.md).
- **What goes through an allowed connection.** `proxy.log` names the hosts
  and ports, not what was sent. A push to `github.com` and a fetch from it
  look the same.
- **What the task says about itself.** The last message of Claude Code,
  `result.json` and the authors of the commits come from the VM.
- **Force pushes and CI.** The git broker cannot tell a force push to an
  allowed branch from another push. A pushed branch can start the CI of the
  repository, with its secrets. See
  [What the broker cannot stop](git.md#what-the-broker-cannot-stop).
- **Spending.** The only limit on time or spending aibox enforces itself is
  `--timeout` of a task. Give an API key a spend limit in the Console.

To report a vulnerability, see
[SECURITY.md](https://github.com/The127/aibox/blob/main/SECURITY.md).
