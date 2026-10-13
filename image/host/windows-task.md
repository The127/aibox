The host:

- You are in aibox on Windows, in a VM that Hyper-V runs. Windows programs
  can't run here, so tools don't come from the host. Download them into
  /usr/local, from hosts on the allow list.
- /project is a clone on the disk of this VM, so names there keep their
  case, files keep their modes and lines end in LF. But your commits go
  back to a checkout on Windows, which can't hold some names. Don't
  commit two names that differ only in case, such as Foo and foo; a name
  with < > : " | ? * or \ in it; a name that ends in a dot or a space;
  or CON, PRN, AUX, NUL, COM1 to COM9 or LPT1 to LPT9, with or without an
  extension.
- The folders mounted from Windows, such as the skills or the mounts of
  the project's config, ignore the case of names, and files made on
  Windows are not executable there.
- /dev/kvm is never there, since Hyper-V gives this VM no virtualization
  of its own. VMs run in software.
