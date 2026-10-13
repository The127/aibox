# Changelog

## [2.0.1](https://github.com/The127/aibox/compare/v2.0.0...v2.0.1) (2026-10-13)


### Bug Fixes

* give the release binaries the version of their tag ([#180](https://github.com/The127/aibox/issues/180)) ([bdc782b](https://github.com/The127/aibox/commit/bdc782b9300070ff454c723fefb805d557f923c6))

## [2.0.0](https://github.com/The127/aibox/compare/v1.6.1...v2.0.0) (2026-10-13)


### ⚠ BREAKING CHANGES

* give each project path a folder of its own ([#144](https://github.com/The127/aibox/issues/144))

### Features

* bring agents, commands, CLAUDE.md and safe settings into the VM ([#149](https://github.com/The127/aibox/issues/149)) ([9172396](https://github.com/The127/aibox/commit/91723969886e89306a571fbbd8b57ad177ff3577))
* paste an image with Alt+V on Windows ([#154](https://github.com/The127/aibox/issues/154)) ([01a6369](https://github.com/The127/aibox/commit/01a6369ae973f73b614bd36784804c8657c910f7))
* paste the text on the clipboard on Ctrl+V ([#175](https://github.com/The127/aibox/issues/175)) ([24d79dc](https://github.com/The127/aibox/commit/24d79dcbca8795b6b7722461a849a0e1c4df5efa))
* put aibox for Windows into each release ([#177](https://github.com/The127/aibox/issues/177)) ([15bbd8f](https://github.com/The127/aibox/commit/15bbd8f78170e82ab700c20d38dae1cb17a90286))
* reach the VM over a virtio serial port without vsock namespaces ([#162](https://github.com/The127/aibox/issues/162)) ([52db71d](https://github.com/The127/aibox/commit/52db71d76cf4eddef8da85d6bf26073c259278ef))
* reset, list and remove what aibox keeps for a project ([#143](https://github.com/The127/aibox/issues/143)) ([df993b5](https://github.com/The127/aibox/commit/df993b5f944c5c99ab3f57ffbbf0aaaf702fafaf))
* resume a Claude Code session with aibox run --resume &lt;id&gt; ([#153](https://github.com/The127/aibox/issues/153)) ([e6ab48a](https://github.com/The127/aibox/commit/e6ab48a41efd019ae8312cb18526fde77c273fc6))
* serve a VM its proxy from an AppContainer on Windows ([#146](https://github.com/The127/aibox/issues/146)) ([e01d8cf](https://github.com/The127/aibox/commit/e01d8cf99d76468d8469e96627dc44a096413fff))
* start new projects from a config of your own ([#147](https://github.com/The127/aibox/issues/147)) ([f94616c](https://github.com/The127/aibox/commit/f94616cfd0d4e56712f39f0e1631607d02bdb2e8))
* suggest presets from the files of a project ([#145](https://github.com/The127/aibox/issues/145)) ([ba58728](https://github.com/The127/aibox/commit/ba587288e637c755ba060a496a99c7de5e8ed37d))
* take Windows paths in mounts, tasks, the editor and SSH ([1e76aac](https://github.com/The127/aibox/commit/1e76aac133aa18b472b27d20082273c75b9e252f))
* tell Claude Code in the VM which system its host runs ([#159](https://github.com/The127/aibox/issues/159)) ([8f81b90](https://github.com/The127/aibox/commit/8f81b90fcb3e865444bd66c01926c7f903a51991))
* tell how to go on with the session when aibox run ends ([#155](https://github.com/The127/aibox/issues/155)) ([326a291](https://github.com/The127/aibox/commit/326a2917b57cd296a1eb997abc011a3850a88102))


### Bug Fixes

* add the state disk once the VM runs on Hyper-V ([#173](https://github.com/The127/aibox/issues/173)) ([0c649ca](https://github.com/The127/aibox/commit/0c649ca4e41d559b6a52e0335cf533e2864f2f1b))
* give a task on Windows a host part of its own ([#166](https://github.com/The127/aibox/issues/166)) ([e3bf055](https://github.com/The127/aibox/commit/e3bf0550f315792997b5202bff68c7eb8b0e6a29))
* give a Windows task a fetch command and a prompt it can take ([#163](https://github.com/The127/aibox/issues/163)) ([4a75932](https://github.com/The127/aibox/commit/4a75932c206e7bd8adb59c9a626b273a84028f48))
* give each project path a folder of its own ([ca6c76c](https://github.com/The127/aibox/commit/ca6c76c63636c800613ea7a47c1260b9df44c5e5)), closes [#142](https://github.com/The127/aibox/issues/142)
* give each project path a folder of its own ([#144](https://github.com/The127/aibox/issues/144)) ([ca6c76c](https://github.com/The127/aibox/commit/ca6c76c63636c800613ea7a47c1260b9df44c5e5))
* give git in the VM the line endings of the host ([#158](https://github.com/The127/aibox/issues/158)) ([076ae4f](https://github.com/The127/aibox/commit/076ae4f4237a963c47386b3b84cccf0c7f397af5))
* keep deadlines off the tunnel of a local service of the proxy ([#174](https://github.com/The127/aibox/issues/174)) ([62befaf](https://github.com/The127/aibox/commit/62befafed7576b033e9e65eb811da6defea872e4))
* keep the clock of the VM in UTC on Hyper-V ([#157](https://github.com/The127/aibox/issues/157)) ([9a13c11](https://github.com/The127/aibox/commit/9a13c117e01fb3094ab3ca1880179cda33492a59))
* let a task write in the folders of its home that hold mounts ([#167](https://github.com/The127/aibox/issues/167)) ([50926b8](https://github.com/The127/aibox/commit/50926b88cacbeb236420722faaed62a0d5ccf28e))
* let the guest rename and link to a name of one character on Windows ([#156](https://github.com/The127/aibox/issues/156)) ([1db5074](https://github.com/The127/aibox/commit/1db50747f0fc05d60c3c6fed489936df6f6d908f))
* open no login window of a credential helper when aibox starts ([#176](https://github.com/The127/aibox/issues/176)) ([ec46507](https://github.com/The127/aibox/commit/ec46507d8782bffdba912bd5a3fc8ff7b93a48ed))
* refuse a link in the same step as the open on Windows ([#178](https://github.com/The127/aibox/issues/178)) ([ad35422](https://github.com/The127/aibox/commit/ad35422e17f8a445786078d53501af0ab265596c)), closes [#90](https://github.com/The127/aibox/issues/90)
* refuse a service of another version than aibox ([#165](https://github.com/The127/aibox/issues/165)) ([fa335bf](https://github.com/The127/aibox/commit/fa335bf3b07463ea64262a9cc0c5e564c65f4e86))
* remove the AppContainer profiles that killed runs left behind on Windows ([#152](https://github.com/The127/aibox/issues/152)) ([7a1293f](https://github.com/The127/aibox/commit/7a1293ff0f25d8740d7ed9d89248d18fa8d7c2af))
* say that Hyper-V is off instead of crashing the service ([#164](https://github.com/The127/aibox/issues/164)) ([d72309d](https://github.com/The127/aibox/commit/d72309d8b4702de443150107dfeb8bbb9d1a8224))
* say that the VM ended when it ends under its command ([#160](https://github.com/The127/aibox/issues/160)) ([4b23a8f](https://github.com/The127/aibox/commit/4b23a8f9671b7dbf3bb18b5092861b7b6b242a3c))
* tell Claude Code in the VM how git reaches a remote ([#170](https://github.com/The127/aibox/issues/170)) ([89364a2](https://github.com/The127/aibox/commit/89364a25d5852de805e9a4dfa57881fb5a73b91f)), closes [#169](https://github.com/The127/aibox/issues/169)
* try the Plan 9 server again when a signal interrupts the connect ([#172](https://github.com/The127/aibox/issues/172)) ([e4a3cda](https://github.com/The127/aibox/commit/e4a3cdaeff45a451a6ff77dc071f2c59db029d0b))

## [1.6.1](https://github.com/The127/aibox/compare/v1.6.0...v1.6.1) (2026-10-12)


### Bug Fixes

* bind the libraries QEMU loads, wherever the distribution puts them ([#127](https://github.com/The127/aibox/issues/127)) ([38aae58](https://github.com/The127/aibox/commit/38aae58d1572f2e990c767ab36b9f5fabd085451))

## [1.6.0](https://github.com/The127/aibox/compare/v1.5.0...v1.6.0) (2026-10-11)


### Features

* install aibox with Homebrew on Linux ([#117](https://github.com/The127/aibox/issues/117)) ([338965d](https://github.com/The127/aibox/commit/338965dcb500f549d42bfbe65668de114e0e3a35)), closes [#104](https://github.com/The127/aibox/issues/104)
* paste images from the host clipboard with Ctrl+V ([#100](https://github.com/The127/aibox/issues/100)) ([d83ae7b](https://github.com/The127/aibox/commit/d83ae7b701f3cabbe584ec1cc043f3fb606e1627)), closes [#97](https://github.com/The127/aibox/issues/97)
* run the VM on Windows, on Hyper-V, through the service ([6d97aa0](https://github.com/The127/aibox/commit/6d97aa039d0756831a3d8c69ce8401528e4f5e9c))


### Bug Fixes

* confine the host's work in folders the VM can write ([#123](https://github.com/The127/aibox/issues/123)) ([b95ba87](https://github.com/The127/aibox/commit/b95ba87fd0b21b4ad222bf29456d181c562ac00d))
* forget the tabs of the VM when all diff tabs close ([#103](https://github.com/The127/aibox/issues/103)) ([7119705](https://github.com/The127/aibox/commit/7119705febad35cbe6d094ee02ae5a9f2a724818))
* keep the SSH sessions of a stalled connection and fit long report lines ([#98](https://github.com/The127/aibox/issues/98)) ([f696eb8](https://github.com/The127/aibox/commit/f696eb8343dc08fd5f1ac7221ffe16a3060152b3))
* refuse a mount of the home folder or ~/.aibox ([#115](https://github.com/The127/aibox/issues/115)) ([57e04ac](https://github.com/The127/aibox/commit/57e04acc3fe505a6ab4364b174403e8ccb352330)), closes [#109](https://github.com/The127/aibox/issues/109)

## [1.5.0](https://github.com/The127/aibox/compare/v1.4.1...v1.5.0) (2026-10-11)


### Features

* let the project config turn off the skills mount ([#74](https://github.com/The127/aibox/issues/74)) ([52f182f](https://github.com/The127/aibox/commit/52f182f2e7a903acaf5a88921b8375f24b4071a0)), closes [#59](https://github.com/The127/aibox/issues/59)
* link Claude Code in the VM to your IDE ([#91](https://github.com/The127/aibox/issues/91)) ([c1f2ef1](https://github.com/The127/aibox/commit/c1f2ef196db01ed4afd42864d19c7fa385db74f4))
* refuse to boot a kernel other than the one of the release ([#73](https://github.com/The127/aibox/issues/73)) ([75bc717](https://github.com/The127/aibox/commit/75bc71785514e2714410736d68b7c92534bf0d03)), closes [#70](https://github.com/The127/aibox/issues/70)
* summarize how often and when last each proxy target was used ([#86](https://github.com/The127/aibox/issues/86)) ([60130a2](https://github.com/The127/aibox/commit/60130a245e629d55c4d680186d865abadfa823e5)), closes [#35](https://github.com/The127/aibox/issues/35)
* take the first steps towards Windows support, aibox doesn't run on Windows yet ([1eed772](https://github.com/The127/aibox/commit/1eed77215067e82a849027a4955108688599a742))
* tell why vsock namespaces are missing and load vsock first ([ac195e3](https://github.com/The127/aibox/commit/ac195e320e039afde2558985edca66c8ff8baa71))
* use a VM image that a package installed ([#69](https://github.com/The127/aibox/issues/69)) ([d6e5a6c](https://github.com/The127/aibox/commit/d6e5a6c9599f303286b1747599479cb40e63ba09)), closes [#68](https://github.com/The127/aibox/issues/68)


### Bug Fixes

* say that aibox needs Linux 7.0, not 6.7 ([ac195e3](https://github.com/The127/aibox/commit/ac195e320e039afde2558985edca66c8ff8baa71))

## [1.4.1](https://github.com/The127/aibox/compare/v1.4.0...v1.4.1) (2026-10-09)


### Bug Fixes

* drop limits of the git broker and the proxy log ([#56](https://github.com/The127/aibox/issues/56)) ([b2d9977](https://github.com/The127/aibox/commit/b2d9977db07f5232160d8fa23480be603e6e827a))
* end the line of the download progress once the download is over ([#51](https://github.com/The127/aibox/issues/51)) ([96cf376](https://github.com/The127/aibox/commit/96cf376b8f7827eeb3fdb0b6d71e6c232b14080a))

## [1.4.0](https://github.com/The127/aibox/compare/v1.3.0...v1.4.0) (2026-10-08)


### Features

* a git broker with rules per repository ([#41](https://github.com/The127/aibox/issues/41)) ([39c1300](https://github.com/The127/aibox/commit/39c1300e25a868ce827e352cb11e60a8bedd079d)), closes [#17](https://github.com/The127/aibox/issues/17)
* install aibox with Nix on macOS ([#43](https://github.com/The127/aibox/issues/43)) ([8c722d6](https://github.com/The127/aibox/commit/8c722d61425b00ba0ece258db882edf59e6a0375))
* reach git servers over SSH when git has no HTTPS login ([#46](https://github.com/The127/aibox/issues/46)) ([fb4b2cd](https://github.com/The127/aibox/commit/fb4b2cd1ff7aa080bf55e8d56eb9afb28da371cb))

## [1.3.0](https://github.com/The127/aibox/compare/v1.2.0...v1.3.0) (2026-10-08)


### Features

* install aibox with Nix ([#40](https://github.com/The127/aibox/issues/40)) ([fc478fe](https://github.com/The127/aibox/commit/fc478fea5c036965ffae0e7c76a598919962bda3))
* keep an API key on the host ([#37](https://github.com/The127/aibox/issues/37)) ([0d754d8](https://github.com/The127/aibox/commit/0d754d8ae124e28278a939faf701588cf4c49280)), closes [#27](https://github.com/The127/aibox/issues/27)
* log the hosts the proxy connected to ([#34](https://github.com/The127/aibox/issues/34)) ([2c5cf1f](https://github.com/The127/aibox/commit/2c5cf1f896c91ae8ec7fe93a9006a61c7e0d6130)), closes [#20](https://github.com/The127/aibox/issues/20)
* mark the progress lines that come from the VM ([#31](https://github.com/The127/aibox/issues/31)) ([1d22099](https://github.com/The127/aibox/commit/1d22099de2c780c01f1cd2c234f87ec3db891483)), closes [#29](https://github.com/The127/aibox/issues/29)
* put the task ID and the time in every line of aibox task ([#32](https://github.com/The127/aibox/issues/32)) ([570e546](https://github.com/The127/aibox/commit/570e546d87e0b1c6015416e066fe923c50b14ca1)), closes [#30](https://github.com/The127/aibox/issues/30)

## [1.2.0](https://github.com/The127/aibox/compare/v1.1.0...v1.2.0) (2026-10-08)


### Features

* remove old tasks with aibox tasks clean ([#22](https://github.com/The127/aibox/issues/22)) ([e1bfe03](https://github.com/The127/aibox/commit/e1bfe031f6692161aa052254b23d7fa83692f60e))
* run a task unattended in the guest ([#15](https://github.com/The127/aibox/issues/15)) ([031ad5b](https://github.com/The127/aibox/commit/031ad5be190f00f7e54f09a47015d315e1264dc5))
* run a task unattended with aibox task ([#16](https://github.com/The127/aibox/issues/16)) ([5cfc269](https://github.com/The127/aibox/commit/5cfc26954cd9ce25259f4144a5bbaf61dfb1dac5))
* serve a session without a terminal ([#13](https://github.com/The127/aibox/issues/13)) ([c3b5e54](https://github.com/The127/aibox/commit/c3b5e54487d7ab24cacd95a6b13418b0628110fc))
* start a task from another commit with --from ([b07edeb](https://github.com/The127/aibox/commit/b07edebbe47011f1ed954e80eee358897fafa467))
* take the prompt of a task from a file or stdin ([#19](https://github.com/The127/aibox/issues/19)) ([6b56291](https://github.com/The127/aibox/commit/6b56291542f84540d81f76f88d70acbe0ca2b009))


### Bug Fixes

* give git in the VM a pager it has ([#24](https://github.com/The127/aibox/issues/24)) ([d240bf0](https://github.com/The127/aibox/commit/d240bf0672410b5ee349022fb1ff66cb1608e89f)), closes [#12](https://github.com/The127/aibox/issues/12)
* run no filter of the project's .git/config when a task starts ([b07edeb](https://github.com/The127/aibox/commit/b07edebbe47011f1ed954e80eee358897fafa467))
* stop virtiofsd and QEMU when aibox is killed ([#25](https://github.com/The127/aibox/issues/25)) ([400b662](https://github.com/The127/aibox/commit/400b662c9c1583bc04984d1e403b5fc8f9df96ba))

## [1.1.0](https://github.com/The127/aibox/compare/v1.0.0...v1.1.0) (2026-10-06)


### Features

* reach ports on the loopback of the host from the VM ([#9](https://github.com/The127/aibox/issues/9)) ([f8a783a](https://github.com/The127/aibox/commit/f8a783a4e3931a326ca8502b7275a58b89175a97))

## 1.0.0 (2026-10-06)


### Features

* add aibox config edit ([f85ab03](https://github.com/The127/aibox/commit/f85ab030dc8ca855a63f7d60471180eeefc5e68b))
* add preset:docker for the hosts a pull from Docker Hub needs ([70850a0](https://github.com/The127/aibox/commit/70850a06e0508e6dfd43f0b289d890915b8c3a3c))
* add presets to the allow list ([364e888](https://github.com/The127/aibox/commit/364e88867ddfba875c191cd0c6413ec56118d600))
* add the aibox run command ([25a951e](https://github.com/The127/aibox/commit/25a951e8c367793c6e283a2e09b455ee0089d007))
* add the HTTP CONNECT proxy ([bca3fe0](https://github.com/The127/aibox/commit/bca3fe0e94209afc7b440f8bb7c125e88db61651))
* add the init of the VM ([6e6c282](https://github.com/The127/aibox/commit/6e6c282eb5d4ddeee0792f5f4c68da9e90cefc8c))
* add the session package ([aa41726](https://github.com/The127/aibox/commit/aa4172627d3f30acfa647c84fe5ea543e1a858c2))
* add the tunnel package ([4b21f23](https://github.com/The127/aibox/commit/4b21f23f4905f0e9a9e6b75c68dcd9c4ba0f52df))
* add urfave/cli with version output ([9c3d454](https://github.com/The127/aibox/commit/9c3d4544b773c1004bf798dd65112be084153321))
* attach the terminal of aibox to the session of the VM ([45d19ac](https://github.com/The127/aibox/commit/45d19ac28857b83d766e7ca3c1ad9317e4a159c4))
* boot the VM into Claude Code or a shell ([b24f5c2](https://github.com/The127/aibox/commit/b24f5c245471e8ddd78b719110c17664dfdfd54c))
* build the kernel without PCI and ACPI ([450940f](https://github.com/The127/aibox/commit/450940f8a02be603c4f68692e3cbe39c0ae1b8a0))
* build the QEMU and virtiofsd command lines ([e9a1690](https://github.com/The127/aibox/commit/e9a1690f1b9885df3bd7e9c1a8ee402958e0f08d))
* build the VM image on macOS ([cb6297b](https://github.com/The127/aibox/commit/cb6297b92884758a41a8b0b219374897c388a110))
* build the VM image with miso ([6b7bf88](https://github.com/The127/aibox/commit/6b7bf88f4f484180949fbf5c68a9a1a567665ed5))
* confine aibox itself once the VM runs ([1603453](https://github.com/The127/aibox/commit/1603453d40c63e441449771c27062790fc192e11))
* confine aibox on macOS once the VM runs ([ac370c0](https://github.com/The127/aibox/commit/ac370c03b7230d0e8992054e95a6ca0f8d22e8b9))
* download the VM image of the release on the first run ([4191af4](https://github.com/The127/aibox/commit/4191af4ef98607f5a36608128eec1276e27111f0))
* drop CPU frequency scaling from the kernel ([2ec722f](https://github.com/The127/aibox/commit/2ec722f7a4c276f8725c775732db927c05372040))
* end aibox with the exit code of the command in the VM ([4adeac4](https://github.com/The127/aibox/commit/4adeac4bcedf2788d24f802ffa4f2a066acbec52))
* forward the proxy from the VM to the host ([bdb22e4](https://github.com/The127/aibox/commit/bdb22e4825dfe9857e1f8eefe08fa570034817d2))
* give each VM a vsock namespace of its own ([13a6a38](https://github.com/The127/aibox/commit/13a6a38e460e9a6bdcd884f4f22edce611c42f33))
* give git in the VM the identity of the host ([fe947ca](https://github.com/The127/aibox/commit/fe947ca950289de7deb685064410f7b13a223140))
* give the user in the VM a cgroup so container limits apply ([5f4ae4d](https://github.com/The127/aibox/commit/5f4ae4d746bec6b9b12bb7889712810840e7c9d9))
* hand QEMU every file as a descriptor ([9866733](https://github.com/The127/aibox/commit/98667338f5389e6050c0ce8a103a1169a528b3a3))
* keep /usr/local and ~/.cache of a project on a disk of its own ([7d1a871](https://github.com/The127/aibox/commit/7d1a871c7e69a10a6afe31e7a4da2acceff733d1))
* keep a folder for each project ([e49a55f](https://github.com/The127/aibox/commit/e49a55f4d9641c982435b7d0a0e36bcf908d026b))
* keep Claude Code's nonessential traffic out of the VM ([ff76768](https://github.com/The127/aibox/commit/ff767682dc780e5d4560efbced1055f50778c637))
* keep the git config and hooks of the project read-only in the VM ([8ecc5c4](https://github.com/The127/aibox/commit/8ecc5c4ab835c48b9445a700e1fe9550397d37e5))
* let the VM on macOS run VMs of its own with hardware support ([2dcb9ca](https://github.com/The127/aibox/commit/2dcb9ca7cc40f79ceda7155a94038e9600b31c19))
* make /usr/local/bin and keep the Go module cache on the state disk ([c8c5ec2](https://github.com/The127/aibox/commit/c8c5ec286526a99bff6e56aeaf4564e0f579b1f7))
* make the root of the VM read-only ([a4ce1b6](https://github.com/The127/aibox/commit/a4ce1b6e4e5939f6882d7f42cf0bb5a3735ef06d))
* map the host user to the VM user in the shares ([b66a751](https://github.com/The127/aibox/commit/b66a751a4860a12064af1ce616b3806331deaf1d))
* mount the shared folders of the host in the VM ([749cc44](https://github.com/The127/aibox/commit/749cc44a916360dc60b8f7595accfac5fd5ef679))
* pass environment variables into the VM ([f555065](https://github.com/The127/aibox/commit/f555065bd3f2254d0844694c36ef1b233abad944))
* power the VM off on arm64 instead of resetting it ([f517996](https://github.com/The127/aibox/commit/f5179961386a29232a628a4e41c53e64bac35a41))
* print hello world ([39eace3](https://github.com/The127/aibox/commit/39eace3a62a417b4e8b39695d8fa3286449500d1))
* put folders of the config in front of the PATH of the VM ([6e12a4a](https://github.com/The127/aibox/commit/6e12a4ac103de6ba1c045fb009df293902dab42d))
* reduce the kernel to the options the VM needs ([0cf6fb5](https://github.com/The127/aibox/commit/0cf6fb57fcb4437b75e50ad16ff3eb02e0000bc0))
* refuse hosts that are not on the project's allowlist ([53844ca](https://github.com/The127/aibox/commit/53844ca7ae7d37b269526d87ebb3be34135aaf65))
* refuse names that resolve into the host's own networks ([221b08a](https://github.com/The127/aibox/commit/221b08a94c3c4e7bfe112b729c45448471cd2c86))
* refuse to run as root ([ea996ab](https://github.com/The127/aibox/commit/ea996abed885caf095b484f30f610c37e7160679))
* refuse to run from the home, the root or above the aibox folder ([b0b4f75](https://github.com/The127/aibox/commit/b0b4f75268ded511a082a4952e127464e6830cd0))
* refuse to run without a terminal on stdin ([4efea31](https://github.com/The127/aibox/commit/4efea31276c227bd2f1d3b8238cfe49753b99915))
* run aibox on macOS with Virtualization.framework ([72209c9](https://github.com/The127/aibox/commit/72209c9ea6af17a4c6ece6cba5f620bcce78b286))
* run aibox on macOS with Virtualization.framework ([e232e99](https://github.com/The127/aibox/commit/e232e9916b1b32a042ad14eed9f35003320dbe11))
* run containers and VMs inside the VM ([89f9941](https://github.com/The127/aibox/commit/89f9941b110544b7e3a0c686e0ee96a22224fff1))
* run QEMU in a bubblewrap sandbox ([07dba47](https://github.com/The127/aibox/commit/07dba4773397676d45859435c7a8e7a5b8008f9f))
* run the VM with Virtualization.framework of macOS ([2d1afdd](https://github.com/The127/aibox/commit/2d1afddd7db1613ef956eb91787801905f579a97))
* serve the proxy to the VM over vsock ([ad1de45](https://github.com/The127/aibox/commit/ad1de45f7d2eb1efcc63ac56e935f0d6362aaaef))
* serve the terminal of the VM to the host over vsock ([7313351](https://github.com/The127/aibox/commit/731335122e7e6860df03c85a42bc4432219f917a))
* set the VM's memory and CPUs in config.yaml ([9b09734](https://github.com/The127/aibox/commit/9b09734dc3b5c2d8aba33f5aa69d624f4c88b1df))
* share folders of the host read-only into the VM ([76a5911](https://github.com/The127/aibox/commit/76a591198867d3999413084de3a43f214157b29b))
* share the skills of the person into the VM ([fece3d8](https://github.com/The127/aibox/commit/fece3d8ef7e0dcce21b873542be5a60e4605a963))
* start virtiofsd and QEMU for the VM ([da76adf](https://github.com/The127/aibox/commit/da76adf7166b92db02be2d571281fd68ec1db0cf))
* take the PATH of the VM from a variable of the host ([5031876](https://github.com/The127/aibox/commit/5031876d21eb0399d264401e8d349df7b64de556))
* tell Claude Code that it runs inside aibox ([d815ef1](https://github.com/The127/aibox/commit/d815ef16649f7558fbfe7cd8386b4e2196369db5))
* write the console of the VM into console.log ([5dae7e5](https://github.com/The127/aibox/commit/5dae7e59503fd8d986bedc86f60ef0ebd712cf97))


### Bug Fixes

* do not report the proxy as stopped when the VM went away first ([62df3b4](https://github.com/The127/aibox/commit/62df3b4e0e62ceb7fd58015172f1bfe2b7521835))
* drop the read-only .git, which did not hold ([fc1e020](https://github.com/The127/aibox/commit/fc1e02099dc62c3d361a219d6184bc717a30549c))
* end a run cleanly when the VM stopped before a timer did ([b96bcad](https://github.com/The127/aibox/commit/b96bcad7f238c79b0d37021e281428150c67ee4e))
* harden the kernel for arm64 as the one for x86 is ([d29482b](https://github.com/The127/aibox/commit/d29482b0870fd0d36984b507b9837b3eb59806d3))
* keep the confined aibox from reading other processes on macOS ([110ab41](https://github.com/The127/aibox/commit/110ab4121741d98a301666934d0fe843bb9691ff))
* keep the console log of a running VM when a second run is refused ([3625f5d](https://github.com/The127/aibox/commit/3625f5d73fded97891b2b1be30319f6adfda1860))
* leave .git/info writable in the VM so commit hooks can save changes ([821c322](https://github.com/The127/aibox/commit/821c322c90904ed037af5e1c57dfa668bb882bb0))
* let the Go preset reach the module downloads ([bc171f5](https://github.com/The127/aibox/commit/bc171f5f902ded17444088bb68a65d6573364588))
* let the session own the process and keep relaying until the client has the output ([86969ef](https://github.com/The127/aibox/commit/86969efb49b0dbda422a85996d506c6ad0ef9336))
* lint a checkout whose path has spaces ([7ee875a](https://github.com/The127/aibox/commit/7ee875a7bafee6f1084bd4ded1dda35acd0dc5af))
* lint the code for every host aibox builds on ([a35e417](https://github.com/The127/aibox/commit/a35e4175c2008c6287aeb2ab8f8ac8b7c27ef4d6))
* make .git/config read-only under every name the Mac finds it ([d241363](https://github.com/The127/aibox/commit/d241363911bd897c0080f9a1cf7fc206338a56d5))
* make the launch tests independent of the host and faster ([c23406b](https://github.com/The127/aibox/commit/c23406b99720e03e78f8548370d0cb72f29abaae))
* never format a state disk that has data on it ([c3b15f8](https://github.com/The127/aibox/commit/c3b15f8550856b3013c951551b58cd3b8b7c9d0a))
* never remove the console log of a VM that started ([527ed2d](https://github.com/The127/aibox/commit/527ed2dd2d146c1a31cd09e9e4ae3a43b1b23805))
* port rules, refusal reasons, the CONNECT reply and the updater ([912bfea](https://github.com/The127/aibox/commit/912bfeab277e22d6222cbd7893f59a11e34f5b14))
* refuse the data volume of macOS, which holds the home ([2d14e3a](https://github.com/The127/aibox/commit/2d14e3a4fadf8e2c33ebaf429f7f51766bad92f9))
* report an error of the init from before the console was open ([8303d54](https://github.com/The127/aibox/commit/8303d549d85d55db88e304f46a26a609b3d2e3ed))
* skip a symlink under another name of the git config ([01d47d2](https://github.com/The127/aibox/commit/01d47d2c0a9d2fe1bf7314e9e584bc82aa7469f2))
* stop a guest that started over and close its extra connections ([39dbd41](https://github.com/The127/aibox/commit/39dbd4158b1cf11a9a312565a4e9dfc69479e3f2))
* take only the image a release was built with ([a88fab1](https://github.com/The127/aibox/commit/a88fab1dcf2925709ff68242412d8eb2259309ce))
* tell any kind of connection from the nil one of vz ([99e8c87](https://github.com/The127/aibox/commit/99e8c872cf9c3fbe2274a5ee3ca96a87fa24a1a1))
* tell the project folder from the home by identity, not by name ([00c5eea](https://github.com/The127/aibox/commit/00c5eeaed8998d59a6c7a72817cb8fd2277a01d3))
* wait for the output of the VM before checking it in the terminal test ([1b0e807](https://github.com/The127/aibox/commit/1b0e8078c130eb121ecc1804a47af966b403b690))
