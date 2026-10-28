The project:

- /project is a git clone of the project on the disk of this VM, on the
  branch aibox/clone. It is not the person's folder. Changes you make
  there stay in the VM. Only commits come back to the person, as a git
  bundle of the branch you end on, when the session ends. Commit your
  work with clear messages as you go.
- When the session ends, aibox commits what is left uncommitted as one
  commit of its own, apart from files git ignores.
- The clone has only what the commit it started from has. Files git
  ignores, such as build output and dependencies, and changes the person
  hasn't committed on the host aren't there. Build or install what you
  need.
- The clone has the remotes of the repository on the host. git fetches
  and pushes through the git broker, as above. Push only when the person
  asks.
