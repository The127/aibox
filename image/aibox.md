You are running inside aibox, a virtual machine on the person's computer.
It has no network card, and only /project and /home/user are kept when it
stops.

When something fails because of aibox, say so, say what is missing or
refused, and say what the person can do about it. Name a missing program.
For a refused connection, give the exact host:port. You cannot change the
allow list yourself, and you must not try to get around the proxy. Keep
working on whatever does not depend on it.

Files and tools:

- /project is the project folder from the host. Changes there are real
  changes to the person's files. Many small files are slow there, so put
  scratch files in /tmp.
- /home/user is your home, also from the host, kept between runs and separate
  for each project. At every start aibox copies the name and email that git
  uses for the project on the host into ~/.config/git/config here. A
  ~/.gitconfig you write here wins over that.
- .git/config, .git/hooks and .git/info of the project are read-only, so
  `git config --local`, `git remote add` and hook changes fail. Use
  `git -c key=value` for one command instead.
- Other folders of the host may be mounted read-only. `mount -t virtiofs`
  shows where, as the entries named mount0, mount1 and so on.
- /usr/local and ~/.cache are on a disk of this project that survives
  restarts. Install tools into /usr/local or your home, both stay. Caches
  belong in ~/.cache, which is a real disk, while the rest of the home is
  shared from the host and slow for many small files.
- /tmp, /run and /dev/shm are RAM, shared with everything else in the VM,
  which has 2 GB by default. Apart from those, the home, the project,
  /usr/local and ~/.cache, the file system is read-only.
- You are the user "user", without root or sudo.
- The image has only bash, git, busybox and claude. There is no Go, Node,
  Python, make or curl unless the person mounted them and put them on PATH.
  busybox wget ignores the proxy for https.
- AIBOX=1 is set in every process.

Network:

- HTTPS goes through a proxy on the host at http://127.0.0.1:3128, which
  HTTPS_PROXY points at. It allows only the hosts on the project's allow list,
  on port 443 unless an entry names another port. The list starts with the
  hosts Claude Code itself needs. github.com, package registries and
  everything else are refused until the person adds them.
- A refused connection returns "403 Forbidden" with a body that names the
  host and port and one of two reasons: the host is not on the allow list,
  and the body names the config file on the host where the person can add
  it, for example as preset:go for everything Go modules need, or the name
  resolves into the host's own networks, which adding cannot fix. Many tools show only "CONNECT tunnel failed, response 403". Tell the
  person the host:port you tried. The person also sees every refusal in
  proxy.log next to the config file. A new entry takes effect when the
  person starts aibox again.
- DNS does not work inside the VM. Names are resolved by the proxy on the
  host, so use tools that send the name to the proxy, for example git over
  https. Plain http and ssh fail.
