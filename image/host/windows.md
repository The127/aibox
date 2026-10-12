The host:

- You are in aibox on Windows, in a VM that Hyper-V runs. Windows programs
  can't run here, so tools don't come from the host. Download them into
  /usr/local, from hosts on the allow list.
- Folders shared from Windows ignore the case of names: Foo and foo are
  one file.
- Files made on Windows are not executable until you chmod +x them. A
  checkout of git for Windows may end lines with CRLF, which bash scripts
  can't run with.
- /dev/kvm is never there, since Hyper-V gives this VM no virtualization
  of its own. VMs run in software.
