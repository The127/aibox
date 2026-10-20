// Package config reads the settings of a project.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// ErrBadHost is an allow entry that no host name can match.
var ErrBadHost = errors.New("not a host name")

// ErrNotPositive is a memory, cpus or disk setting below one.
var ErrNotPositive = errors.New("must be at least 1")

// ErrDiskTooLarge is a disk setting beyond MaxDiskGiB.
var ErrDiskTooLarge = errors.New("must be at most 1048576 GiB")

// MaxDiskGiB is the largest disk the config accepts, 1 PiB: ext4 goes up
// to 1 EiB, and the number of bytes has to fit the size of a file.
const MaxDiskGiB = 1 << 20

// ErrProxyPort is an allow entry on the loopback with the port the proxy of
// the VM listens on, 127.0.0.1:3128 inside the VM.
var ErrProxyPort = errors.New("the VM has its proxy on this port of its loopback")

// ErrLocalhost is an allow entry for localhost, which stands for two
// addresses. A server listens on one of them, and anyone on the machine
// could listen on the other.
var ErrLocalhost = errors.New("write the address the server listens on, 127.0.0.1 or [::1]")

// ErrUnspecified is an allow entry for 0.0.0.0 or ::, which a server listens
// on but which leads to the loopback when dialed.
var ErrUnspecified = errors.New("not an address to connect to, write 127.0.0.1 or [::1] for this machine")

// ErrLoopbackTwice are two allow entries for the same port on both
// addresses of the loopback, which the VM has only one of.
var ErrLoopbackTwice = errors.New("the VM has one loopback address, so allow the port on one address only")

// proxyPort is the port of the proxy on the loopback of the VM. It has to
// match guestProxyAddress in internal/guest, which this package cannot
// import.
const proxyPort = "3128"

// ErrBadPort is a ports entry that is not a port from 1 to 65535, or two
// of them as host:vm.
var ErrBadPort = errors.New("must be a port from 1 to 65535, or host:vm with two of them")

// ErrPortTwice are two ports entries with the same port of the host.
var ErrPortTwice = errors.New("the port of the host is in the list twice")

// ErrNoAllowList is a config without a list of allow entries in the plain
// form, one per line, that AddPresets can add to.
var ErrNoAllowList = errors.New("the config has no allow list with one entry per line")

// ErrUnknownPreset is a preset entry that names no known preset.
var ErrUnknownPreset = errors.New("unknown preset")

// ErrBadMount is a mounts entry that is not host:guest with two absolute
// paths, or whose guest path the kernel command line cannot carry.
var ErrBadMount = errors.New("must be host:guest with two absolute paths")

// ErrReservedMount is a mounts entry whose guest path is one the VM needs
// for itself.
var ErrReservedMount = errors.New("the VM needs this path")

// ErrMountOverlap is a mounts entry whose guest path is inside, or
// contains, that of another entry.
var ErrMountOverlap = errors.New("mounts overlap")

// ErrBadShare is a setting of skills, agents, commands, claude-md or
// settings other than all or none.
var ErrBadShare = errors.New("must be all or none")

// ErrBadWatch is a watch entry that is not a path inside the project.
var ErrBadWatch = errors.New("must be a path inside the project, like Makefile or .github/workflows")

// ErrBadPath is a path entry that is neither an absolute folder without a
// colon nor the name of a variable.
var ErrBadPath = errors.New("must be an absolute folder without a colon, or the name of a variable")

// ErrBadVariable is an env entry that is not NAME or NAME=value with a
// name of letters, digits and underscores.
var ErrBadVariable = errors.New("must be NAME or NAME=value")

// ErrReservedVariable is an env entry with a name aibox sets itself.
var ErrReservedVariable = errors.New("aibox sets this variable itself")

// reservedVariables are the names the guest sets for the command, so that
// an env entry with one of them fails at load time. The guest keeps its own
// values for them anyway, except for PATH, which the path setting fills.
// The list has to match ownVariables in internal/guest, which this package
// cannot import.
var reservedVariables = []string{
	"AIBOX", "HOME", "USER", "LOGNAME", "SHELL", "PATH", "TERM", "LANG",
	"HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "GOMODCACHE",
	// the name setting fills this one
	"AIBOX_SESSION_NAME",
}

// NamePath is the name setting that names the session after the project's
// path on the host.
const NamePath = "path"

// ErrBadName is a session name with a control character, which the
// terminal would act on in its title.
var ErrBadName = errors.New("a session name must have no control characters")

// CheckName returns ErrBadName unless the name can be the name of a Claude
// Code session.
func CheckName(name string) error {
	if strings.ContainsFunc(name, unicode.IsControl) {
		return fmt.Errorf("%q: %w", name, ErrBadName)
	}

	return nil
}

var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ErrCmdlineFull are mounts and loopback ports that together do not fit on
// the kernel command line.
var ErrCmdlineFull = errors.New("the mounts and the loopback ports do not fit on the kernel command line together")

// reservedPaths are what the VM mounts itself, the project share and the
// kernel file systems, and the folders its programs live in. A mount on,
// above or inside one of them hides something the VM needs.
var reservedPaths = []string{
	"/project", "/dev", "/proc", "/sys", "/run", "/tmp",
	"/etc", "/bin", "/sbin", "/lib", "/lib64", "/usr", "/root",
}

// homePath is the home share. A mount on or above it hides the home, a
// mount inside is fine, because the home of the VM is a folder of aibox.
const homePath = "/home/user"

// maxCmdlineBytes is what the mounts and the loopback ports may take up
// together on the kernel command line, which holds 2048 bytes and needs
// room for the rest. wordBytes is what aibox adds around each mount, and
// portBytes what a loopback port takes at most.
const (
	maxCmdlineBytes = 1024
	wordBytes       = 24
	portBytes       = 6
)

// presets maps a preset name to the hosts it allows. An allow entry of the
// form preset:name stands for them.
var presets = map[string][]string{
	// proxy.golang.org redirects module downloads to storage.googleapis.com
	"go":   {"proxy.golang.org", "sum.golang.org", "storage.googleapis.com", "vuln.go.dev", "dl.google.com", "go.dev"},
	"npm":  {"registry.npmjs.org", "registry.yarnpkg.com", "nodejs.org"},
	"pypi": {"pypi.org", "files.pythonhosted.org"},
	// rustup comes from sh.rustup.rs
	"cargo":  {"crates.io", "static.crates.io", "index.crates.io", "static.rust-lang.org", "sh.rustup.rs"},
	"github": {"github.com", "api.github.com", "codeload.github.com", "*.githubusercontent.com"},
	// mise looks up the versions of most tools on GitHub and downloads many
	// of them from its releases. The list of the builds of Python comes
	// from a host of mise, and the keys it checks their signatures with
	// from sigstore.
	"mise": {"github.com", "api.github.com", "*.githubusercontent.com", "mise-versions.jdx.dev", "tuf-repo-cdn.sigstore.dev"},
	// the layers of an image come from production.cloudfront.docker.com
	"docker": {"registry-1.docker.io", "auth.docker.io", "index.docker.io", "production.cloudfront.docker.com"},
}

// markers maps a preset name to the names of files that show a project
// needs it, in the project's top folder. github and docker have none: a
// git remote or a Dockerfile doesn't say that the VM needs them.
var markers = map[string][]string{
	"go":    {"go.mod", "go.work"},
	"npm":   {"package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml"},
	"pypi":  {"pyproject.toml", "requirements.txt", "setup.py", "setup.cfg", "Pipfile", "uv.lock", "poetry.lock"},
	"cargo": {"Cargo.toml", "Cargo.lock"},
	"mise":  {"mise.toml", ".tool-versions"},
}

// titles are the names of the tools of the presets markers knows, as
// people call them.
var titles = map[string]string{
	"go":    "Go",
	"npm":   "npm",
	"pypi":  "Python",
	"cargo": "Rust",
	"mise":  "mise",
}

func presetNames() []string {
	return slices.Sorted(maps.Keys(presets))
}

// Suggestion is a preset that files of a project show it needs: the name
// of its tool, the files that show it, by the names in markers, and the
// hosts the preset allows.
type Suggestion struct {
	Preset string
	Tool   string
	Files  []string
	Hosts  []string
}

// Suggest returns the presets the file names show a project needs, which
// allow doesn't allow yet. It compares the names exactly, and gives back
// only names and hosts of its own, none of the names it was given.
func Suggest(files []string, allow Hosts) []Suggestion {
	var suggestions []Suggestion

	for _, name := range slices.Sorted(maps.Keys(markers)) {
		found := slices.DeleteFunc(slices.Clone(markers[name]), func(marker string) bool {
			return !slices.Contains(files, marker)
		})

		if len(found) > 0 && !allowsAll(allow, presets[name]) {
			suggestions = append(suggestions, Suggestion{Preset: name, Tool: titles[name], Files: found, Hosts: presets[name]})
		}
	}

	return suggestions
}

// Markers are the names of all files Suggest looks for.
func Markers() []string {
	var names []string

	for _, name := range slices.Sorted(maps.Keys(markers)) {
		names = append(names, markers[name]...)
	}

	return names
}

func allowsAll(allow Hosts, hosts []string) bool {
	return !slices.ContainsFunc(hosts, func(host string) bool {
		e, err := parseEntry(host)

		return err != nil || !slices.Contains(allow, e.String())
	})
}

// PresetFor returns the preset that allows host on port, if one does.
func PresetFor(host, port string) (string, bool) {
	host = normalize(host)

	for _, name := range presetNames() {
		if slices.ContainsFunc(presets[name], func(text string) bool {
			e, err := parseEntry(text)

			return err == nil && e.matches(host, port)
		}) {
			return name, true
		}
	}

	return "", false
}

// AllowHint says where to allow a refused target, and which preset
// allows it, when one does.
func AllowHint(configFile string) func(target string) string {
	return func(target string) string {
		hint := "Add it to " + configFile + " to allow it."

		host, port, err := net.SplitHostPort(target)
		if err != nil {
			return hint
		}

		if preset, ok := PresetFor(host, port); ok {
			hint += " preset:" + preset + " allows it."
		}

		return hint
	}
}

// Config holds the settings of one project. Memory and CPUs are nil when
// the file does not set them.
type Config struct {
	// Allow lists the hosts the VM may reach.
	Allow Hosts `yaml:"allow"`
	// Memory is the memory of the VM in MiB.
	Memory *int `yaml:"memory"`
	// CPUs is the number of CPUs of the VM.
	CPUs *int `yaml:"cpus"`
	// Disk is the size of the state disk of the project in GiB.
	Disk *int `yaml:"disk"`
	// MaxConnections is how many connections the proxy serves at once.
	MaxConnections *int `yaml:"max-connections"`
	// Mounts are folders of the host the VM sees read-only.
	Mounts []Mount `yaml:"mounts"`
	// Skills, Agents and Commands say whether the VM sees those folders of
	// the person's ~/.claude, ClaudeMD whether it gets their CLAUDE.md, and
	// Settings whether it gets the settings of theirs that start no program
	// and hold no secret.
	Skills   Share `yaml:"skills"`
	Agents   Share `yaml:"agents"`
	Commands Share `yaml:"commands"`
	ClaudeMD Share `yaml:"claude-md"`
	Settings Share `yaml:"settings"`
	// IDE says whether Claude Code in the VM may use the IDE that has the
	// project open.
	IDE bool `yaml:"ide"`
	// Mise says whether the VM installs the tools the project's mise.toml
	// and .tool-versions name, with mise, before the command starts.
	Mise bool `yaml:"mise"`
	// Clipboard says whether the person pastes from the clipboard of the
	// host with Ctrl+V: an image Claude Code in the VM may read, or text
	// aibox types. Nil means they do.
	Clipboard *bool `yaml:"clipboard"`
	// PasteAltV says whether Alt+V pastes too. Nil means it does on
	// Windows, whose terminals paste on Ctrl+V themselves, and not
	// elsewhere.
	PasteAltV *bool `yaml:"paste-alt-v"`
	// Name is the name of the Claude Code session: NamePath for the
	// project's path on the host, any other text as it is, or empty for
	// none.
	Name string `yaml:"name"`
	// Path are folders in the VM that go in front of its PATH, or names of
	// variables of the host whose folders do.
	Path []string `yaml:"path"`
	// Env are variables for the command in the VM.
	Env []Variable `yaml:"env"`
	// Git are the repositories git in the VM may fetch from and push to
	// with the git login of the host.
	Git []GitRemote `yaml:"git"`
	// Ports are servers in the VM that the host reaches on its loopback.
	Ports []Port `yaml:"ports"`
	// Watch are files and folders of the project, with slashes, that aibox
	// run checks for changes besides the ones it always checks.
	Watch []string `yaml:"watch"`
}

// Port is a port on the loopback of the host whose connections go to a
// port on the loopback of the VM. In the file it is written as host:vm, or
// as one port for both.
type Port struct {
	Host uint16
	VM   uint16
}

// UnmarshalYAML reads a port from its host:vm or port form.
func (p *Port) UnmarshalYAML(value *yaml.Node) error {
	var text string
	if err := value.Decode(&text); err != nil {
		return err
	}

	port, err := parsePort(text)
	if err != nil {
		return err
	}

	*p = port

	return nil
}

func parsePort(text string) (Port, error) {
	hostText, vmText, ok := strings.Cut(text, ":")
	if !ok {
		vmText = hostText
	}

	host, hostErr := strconv.ParseUint(hostText, 10, 16)
	vm, vmErr := strconv.ParseUint(vmText, 10, 16)

	if hostErr != nil || vmErr != nil || host == 0 || vm == 0 {
		return Port{}, fmt.Errorf("ports entry %q: %w", text, ErrBadPort)
	}

	return Port{Host: uint16(host), VM: uint16(vm)}, nil
}

// checkPorts fails when two entries take the same port of the host.
func checkPorts(ports []Port) error {
	seen := map[uint16]bool{}

	for _, port := range ports {
		if seen[port.Host] {
			return fmt.Errorf("ports entry %d: %w", port.Host, ErrPortTwice)
		}

		seen[port.Host] = true
	}

	return nil
}

// Variable is an environment variable for the command in the VM. With
// FromHost the value is the one the host has when the VM starts. In the
// file it is written as NAME=value or NAME.
type Variable struct {
	Name     string
	Value    string
	FromHost bool
}

// UnmarshalYAML reads a variable from its NAME=value or NAME form.
func (v *Variable) UnmarshalYAML(value *yaml.Node) error {
	var text string
	if err := value.Decode(&text); err != nil {
		return err
	}

	variable, err := parseVariable(text)
	if err != nil {
		return err
	}

	*v = variable

	return nil
}

func parseVariable(text string) (Variable, error) {
	name, val, hasValue := strings.Cut(text, "=")
	if !variableName.MatchString(name) || strings.ContainsFunc(val, unicode.IsControl) {
		return Variable{}, fmt.Errorf("env entry %q: %w", text, ErrBadVariable)
	}

	if slices.Contains(reservedVariables, name) {
		return Variable{}, fmt.Errorf("env entry %q: %w", text, ErrReservedVariable)
	}

	return Variable{Name: name, Value: val, FromHost: !hasValue}, nil
}

// Share says whether the VM gets a part of the person's Claude Code setup.
// Empty is all.
type Share string

// ShareAll shares the part, ShareNone doesn't.
const (
	ShareAll  Share = "all"
	ShareNone Share = "none"
)

// UnmarshalYAML reads all or none. Anything else, also a list, is
// ErrBadShare.
func (s *Share) UnmarshalYAML(value *yaml.Node) error {
	if share := Share(value.Value); value.Kind == yaml.ScalarNode && (share == ShareAll || share == ShareNone) {
		*s = share

		return nil
	}

	return fmt.Errorf("line %d: %q: %w", value.Line, value.Value, ErrBadShare)
}

// Mount is a folder of the host that the VM sees read-only at Guest. In the
// file it is written as host:guest.
type Mount struct {
	Host  string
	Guest string
}

// UnmarshalYAML reads a mount from its host:guest form.
func (m *Mount) UnmarshalYAML(value *yaml.Node) error {
	var text string
	if err := value.Decode(&text); err != nil {
		return err
	}

	mount, err := parseMount(text)
	if err != nil {
		return err
	}

	*m = mount

	return nil
}

func parseMount(text string) (Mount, error) {
	// a volume, C: or \\server\share on Windows, is the only colon a host
	// folder has; a guest path has none
	volume := filepath.VolumeName(text)
	host, guest, ok := strings.Cut(text[len(volume):], ":")
	host = volume + host
	if !ok || strings.Contains(guest, ":") || !cmdlineSafe(guest) {
		return Mount{}, fmt.Errorf("mounts entry %q: %w", text, ErrBadMount)
	}

	host, err := expandHome(host)
	if err != nil {
		return Mount{}, fmt.Errorf("mounts entry %q: %w", text, err)
	}

	// the guest path is one of the VM, a Linux path, whatever the host
	if !filepath.IsAbs(host) || !path.IsAbs(guest) {
		return Mount{}, fmt.Errorf("mounts entry %q: %w", text, ErrBadMount)
	}

	mount := Mount{Host: filepath.Clean(host), Guest: path.Clean(guest)}
	if mount.reserved() {
		return Mount{}, fmt.Errorf("mounts entry %q: %w", text, ErrReservedMount)
	}

	return mount, nil
}

// cmdlineSafe tells whether the kernel command line carries the text as one
// word: it is split at whitespace and quoted with double quotes.
func cmdlineSafe(text string) bool {
	return !strings.ContainsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '"'
	})
}

func expandHome(folder string) (string, error) {
	// ~\ on Windows too, where \ parts folders as / does
	if len(folder) < 2 || folder[0] != '~' || folder[1] != '/' && folder[1] != filepath.Separator {
		return folder, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, folder[2:]), nil
}

// reserved tells whether the mount hides something the VM needs.
func (m Mount) reserved() bool {
	return slices.ContainsFunc(reservedPaths, m.Touches) || m.covers(homePath)
}

// Touches tells whether the mount is on the path, above it or inside it.
func (m Mount) Touches(path string) bool {
	return m.covers(path) || isBelow(m.Guest, path)
}

// covers tells whether the mount is on the path or above it.
func (m Mount) covers(path string) bool {
	return m.Guest == path || isBelow(path, m.Guest)
}

func isBelow(path, folder string) bool {
	return strings.HasPrefix(path, strings.TrimSuffix(folder, "/")+"/")
}

// checkMounts fails when two mounts are on the same path or one is inside
// the other.
func checkMounts(mounts []Mount) error {
	for i, a := range mounts {
		for _, b := range mounts[:i] {
			if a.Touches(b.Guest) {
				return fmt.Errorf("mounts %s and %s: %w", b.Guest, a.Guest, ErrMountOverlap)
			}
		}
	}

	return nil
}

// Hosts are host names, each with an optional port. An entry without a port
// allows port 443. An entry of the form *.example.com matches every
// subdomain of example.com, but not example.com itself.
type Hosts []string

const defaultPort = "443"

// defaultFile is written for a project that has no config yet. It is kept as
// text, so that the person editing it sees why each host is there.
const defaultFile = `# The hosts the VM may reach. Everything else is refused by the proxy on
# the host. An entry allows port 443, host:port allows another port, and
# *.example.com matches every subdomain of example.com. preset:NAME stands for
# the hosts a tool needs, with NAME one of PRESETS. 127.0.0.1:PORT or
# [::1]:PORT, the address a server listens on, is a port on the loopback of
# this machine, which the VM reaches at 127.0.0.1:PORT and localhost:PORT.
# Whatever listens there runs outside the VM, so allow only servers you
# would let do what the VM asks of them.
allow:
  - api.anthropic.com         # the Claude API
  - claude.ai                 # login with a claude.ai account
  - claude.com                # the sign-in page redirects through here
  - platform.claude.com       # login tokens
  - mcp-proxy.anthropic.com   # MCP connectors of a claude.ai account
  - code.claude.com           # documentation lookups

# The size of the VM, for example:
# memory: 4096   # MiB
# cpus: 4

# The size of the disk that keeps /usr/local and ~/.cache of the VM between
# runs. It takes up space on the host only as it fills. The size is fixed
# when the disk is created on the first run; changing it later has no
# effect. For example:
# disk: 16   # GiB

# How many connections the VM may have open through the proxy at once. Each
# one takes up a file handle of aibox. Beyond it the proxy turns connections away.
# For example:
# max-connections: 256

# Folders of the host the VM sees read-only, written host:guest, for example:
# mounts:
#   - ~/sdk/go:/opt/go

# Whether the VM sees your skills in ~/.claude/skills, read-only: all or
# none. A mount inside /home/user/.claude/skills takes their place, so that
# the VM sees only the skills you mount there. For example:
# skills: none

# Whether the VM gets more of your Claude Code setup: your agents and
# commands in ~/.claude, read-only, and with aibox run your
# ~/.claude/CLAUDE.md and the settings of ~/.claude/settings.json that start
# no program and hold no secret, like model and theme. Each is all or none.
# For example:
# agents: none
# commands: none
# claude-md: none
# settings: none

# Whether Claude Code in the VM may use the IDE that has the project open,
# like VS Code: the code you select, diffs in the editor and the errors it
# reports, for files in the project. It is off unless you turn it on:
# ide: true

# Whether the VM installs the tools your project's mise.toml and
# .tool-versions name, like Go or Node, with mise, before Claude Code
# starts. aibox shows you the files and asks before it uses new or changed
# ones. The downloads need their hosts on the allow list. It is off unless
# you turn it on:
# mise: true

# Whether Ctrl+V pastes from your clipboard: an image, which the VM can
# read only right after you press the key, or text, which aibox types as
# your terminal pastes it. It is on unless you turn it off:
# clipboard: false

# Whether Alt+V pastes too, as Ctrl+V does. The terminals of Windows paste
# on Ctrl+V themselves, so on Windows it is on unless you turn it off, and
# elsewhere off unless you turn it on:
# paste-alt-v: true

# The name of the Claude Code session, which it shows in the prompt box and
# the terminal title. In the VM the project is always /project, so without a
# name every session looks the same. path names it after the project's path
# on this machine, which the VM otherwise doesn't know. Any other text is
# the name. For example:
# name: path

# Folders in the VM that go in front of its PATH. A name such as PATH stands
# for the folders in that variable of the host, which then need a mount at
# the same path in the VM. For example:
# path:
#   - /opt/go/bin
#   - PATH

# Variables for the command in the VM, as NAME=value, or as NAME for the
# value the host has when the VM starts, for example:
# env:
#   - GOFLAGS=-mod=mod
#   - GITHUB_TOKEN

# Repositories git in the VM may use with the git login of this machine,
# which stays here. fetch allows clone and fetch, push the branches a push
# may create or update. aibox refuses deletes, tags and other repositories.
# For example:
# git:
#   - remote: github.com/owner/repo
#     fetch: true
#     push:
#       - aibox/*

# Servers in the VM you open from this machine, like a dev server in your
# browser. aibox listens on 127.0.0.1 here and passes each connection to the
# port on 127.0.0.1 in the VM. Write host:vm for another port here than in
# the VM. Any program on this machine can connect, and a page from the VM
# shares the cookies of the other servers on 127.0.0.1. For example:
# ports:
#   - 5173
#   - 8080:3000

# Files and folders of the project that programs on this machine run, which
# aibox run checks besides the ones it always checks, like .git/config,
# .git/hooks, .envrc, package.json and .idea. When the session ends, aibox
# tells you which of them the VM may have changed. For example:
# watch:
#   - Makefile
#   - .github/workflows
`

// DefaultMaxConnections is how many connections the proxy serves at once
// when the config doesn't say. A browser opens about 6 to a host and npm
// about 15, so a session stays well below it, and aibox can keep twice as
// many files open.
const DefaultMaxConnections = 256

// ConnectionLimit is how many connections the proxy serves at once.
func (c Config) ConnectionLimit() int {
	if c.MaxConnections != nil {
		return *c.MaxConnections
	}

	return DefaultMaxConnections
}

// Default is the config of a project that has no config file yet.
func Default() Config {
	return Config{Allow: Hosts{
		"api.anthropic.com",
		"claude.ai",
		"claude.com",
		"platform.claude.com",
		"mcp-proxy.anthropic.com",
		"code.claude.com",
	}}
}

// Load returns the config in the file at path. When there is no file, it
// writes one first, as EnsureDefault does.
func Load(path, template string) (Config, error) {
	if err := EnsureDefault(path, template); err != nil {
		return Config{}, err
	}

	content, err := os.ReadFile(path) //nolint:gosec // the path is the project's config file
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	cfg, err := parse(content)
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	return cfg, nil
}

// EnsureDefault creates the file at path unless one exists: a copy of the
// file at template, the person's own config for new projects, or the
// default one when there is no template. It refuses a template with a
// mistake, so that no project starts from it. Two aibox started at once
// for a new project then both read the same file.
func EnsureDefault(path, template string) error {
	// a project that has its config needs no template, and one with a
	// mistake must not keep it from starting
	if _, err := os.Lstat(path); err == nil {
		return nil
	}

	text, err := startingText(template)
	if err != nil {
		return err
	}

	// written beside path and linked to it whole, so that an aibox that
	// starts meanwhile reads it complete or not at all, and the first link
	// wins
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	_, err = io.WriteString(tmp, text)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}

	if err == nil {
		err = os.Link(tmp.Name(), path)
	}

	if err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

// startingText is the text a new config starts from: the template's, or
// the default one when template is empty or names no file.
func startingText(template string) (string, error) {
	if template != "" {
		content, err := os.ReadFile(template) //nolint:gosec // the person's template
		if err == nil {
			if _, err := parse(content); err != nil {
				return "", fmt.Errorf("the template %s: %w", template, err)
			}

			return string(content), nil
		}

		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("read the template: %w", err)
		}
	}

	return strings.Replace(defaultFile, "PRESETS", strings.Join(presetNames(), ", "), 1), nil
}

func parse(content []byte) (Config, error) {
	var cfg Config

	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)

	// a file with no document, such as one with only comments, is an empty
	// config
	if err := decoder.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, err
	}

	allow, err := expand(cfg.Allow)
	if err != nil {
		return Config{}, err
	}

	cfg.Allow = allow

	if err := atLeastOne("memory", cfg.Memory); err != nil {
		return Config{}, err
	}

	if err := atLeastOne("cpus", cfg.CPUs); err != nil {
		return Config{}, err
	}

	if err := atLeastOne("disk", cfg.Disk); err != nil {
		return Config{}, err
	}

	if err := atLeastOne("max-connections", cfg.MaxConnections); err != nil {
		return Config{}, err
	}

	if cfg.Disk != nil && *cfg.Disk > MaxDiskGiB {
		return Config{}, fmt.Errorf("disk %d: %w", *cfg.Disk, ErrDiskTooLarge)
	}

	if err := checkMounts(cfg.Mounts); err != nil {
		return Config{}, err
	}

	if err := checkLoopback(cfg.Allow); err != nil {
		return Config{}, err
	}

	if err := checkGit(cfg.Git); err != nil {
		return Config{}, err
	}

	if err := checkPorts(cfg.Ports); err != nil {
		return Config{}, err
	}

	if err := CheckName(cfg.Name); err != nil {
		return Config{}, fmt.Errorf("name %w", err)
	}

	for i, entry := range cfg.Path {
		cleaned, err := parsePathEntry(entry)
		if err != nil {
			return Config{}, err
		}

		cfg.Path[i] = cleaned
	}

	for i, entry := range cfg.Watch {
		cleaned, err := parseWatchEntry(entry)
		if err != nil {
			return Config{}, err
		}

		cfg.Watch[i] = cleaned
	}

	if err := fitsCmdline(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func parseWatchEntry(text string) (string, error) {
	cleaned := path.Clean(filepath.ToSlash(text))

	// . would be the whole project, .git/objects included
	if !filepath.IsLocal(filepath.FromSlash(text)) || cleaned == "." || strings.ContainsFunc(text, unicode.IsControl) {
		return "", fmt.Errorf("watch entry %q: %w", text, ErrBadWatch)
	}

	return cleaned, nil
}

func parsePathEntry(text string) (string, error) {
	if variableName.MatchString(text) {
		return text, nil
	}

	if !path.IsAbs(text) || path.Clean(text) == "/" || strings.Contains(text, ":") || strings.ContainsFunc(text, unicode.IsControl) {
		return "", fmt.Errorf("path entry %q: %w", text, ErrBadPath)
	}

	return path.Clean(text), nil
}

// fitsCmdline fails when the mounts together are too much for the kernel
// command line.
func fitsCmdline(cfg Config) error {
	total := 0

	if ports := cfg.Allow.LoopbackPorts(); len(ports) > 0 {
		total = wordBytes + portBytes*len(ports)
	}

	for _, m := range cfg.Mounts {
		total += wordBytes + len(m.Guest)
	}

	if total > maxCmdlineBytes {
		return ErrCmdlineFull
	}

	return nil
}

func atLeastOne(name string, value *int) error {
	if value != nil && *value < 1 {
		return fmt.Errorf("%s %d: %w", name, *value, ErrNotPositive)
	}

	return nil
}

// expand replaces each preset entry with its hosts and puts every entry into
// its stored form.
func expand(entries Hosts) (Hosts, error) {
	var allow Hosts

	for _, text := range entries {
		hosts := []string{text}

		if name, ok := strings.CutPrefix(text, "preset:"); ok {
			hosts, ok = presets[name]
			if !ok {
				return nil, fmt.Errorf("allow entry %q: %w, known: %s", text, ErrUnknownPreset, strings.Join(presetNames(), ", "))
			}
		}

		for _, host := range hosts {
			e, err := parseEntry(host)
			if err != nil {
				return nil, err
			}

			allow = append(allow, e.String())
		}
	}

	return allow, nil
}

// entry is one allow entry taken apart.
type entry struct {
	host, port string
}

// parseEntry reads an entry. A colon in the text means host:port, so an
// IPv6 address needs brackets and a port.
func parseEntry(text string) (entry, error) {
	host, port := text, defaultPort

	if strings.Contains(text, ":") {
		var err error

		host, port, err = net.SplitHostPort(text)
		if err != nil {
			return entry{}, fmt.Errorf("allow entry %q: %w", text, ErrBadHost)
		}
	}

	e := entry{host: normalize(host), port: port}
	if !isHost(e.host) || !isPort(e.port) {
		return entry{}, fmt.Errorf("allow entry %q: %w", text, ErrBadHost)
	}

	if e.host == localhost {
		return entry{}, fmt.Errorf("allow entry %q: %w", text, ErrLocalhost)
	}

	if ip := net.ParseIP(e.host); ip != nil && ip.IsUnspecified() {
		return entry{}, fmt.Errorf("allow entry %q: %w", text, ErrUnspecified)
	}

	if e.port == proxyPort && e.loopback() != nil {
		return entry{}, fmt.Errorf("allow entry %q: %w", text, ErrProxyPort)
	}

	return e, nil
}

// String is the stored form, with the port only when it is not the default.
func (e entry) String() string {
	if e.port == defaultPort {
		return e.host
	}

	return net.JoinHostPort(e.host, e.port)
}

func (e entry) matches(host, port string) bool {
	if port != e.port {
		return false
	}

	if below, ok := strings.CutPrefix(e.host, "*."); ok {
		// an address can end like a name, as ::ffff:1.2.3.4 ends in .3.4
		if _, err := netip.ParseAddr(host); err == nil {
			return false
		}

		return strings.HasSuffix(host, "."+below)
	}

	return host == e.host
}

// isHost says whether a normalized name can match the host name of a
// request. A URL or a stray character would match nothing.
func isHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}

	name := strings.TrimPrefix(host, "*.")

	return name != "" && !strings.ContainsAny(name, "*/:[] ")
}

// isPort accepts the digits of a port and nothing else, because ports are
// compared as text.
func isPort(port string) bool {
	n, err := strconv.Atoi(port)

	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == port
}

// Ports are the distinct ports of the allow list, ascending. Load has
// checked every entry, so one that does not parse is left out.
func (h Hosts) Ports() []uint16 {
	return h.ports(func(entry) bool { return true })
}

// ports are the distinct ports of the entries keep accepts, ascending.
func (h Hosts) ports(keep func(entry) bool) []uint16 {
	var ports []uint16

	for _, text := range h {
		e, err := parseEntry(text)
		if err != nil || !keep(e) {
			continue
		}

		if port, err := strconv.ParseUint(e.port, 10, 16); err == nil {
			ports = append(ports, uint16(port))
		}
	}

	slices.Sort(ports)

	return slices.Compact(ports)
}

// localhost is the name the VM asks the proxy for a port on the loopback
// of the host with.
const localhost = "localhost"

// loopback returns the address of an entry for 127.0.0.1 or ::1, the
// addresses of the loopback the VM has a port of, and nil for any other.
func (e entry) loopback() net.IP {
	ip := net.ParseIP(e.host)
	if ip != nil && (ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback)) {
		return ip
	}

	return nil
}

// checkLoopback fails when two entries name the same port on both
// addresses of the loopback.
func checkLoopback(hosts Hosts) error {
	seen := map[string]entry{}

	for _, text := range hosts {
		e, err := parseEntry(text)
		if err != nil || e.loopback() == nil {
			continue
		}

		if other, ok := seen[e.port]; ok && !other.loopback().Equal(e.loopback()) {
			return fmt.Errorf("allow entries %s and %s: %w", other, e, ErrLoopbackTwice)
		}

		seen[e.port] = e
	}

	return nil
}

// LoopbackPorts are the distinct ports of the entries on the loopback of
// the host, ascending. The VM has them on its own loopback.
func (h Hosts) LoopbackPorts() []uint16 {
	return h.ports(func(e entry) bool { return e.loopback() != nil })
}

// RemotePorts are the distinct ports of the entries off the loopback of
// the host, ascending.
func (h Hosts) RemotePorts() []uint16 {
	return h.ports(func(e entry) bool { return e.loopback() == nil })
}

// Pinned returns the addresses a name stands for without asking DNS. For
// localhost it is the loopback address an entry names with the port, so
// that the proxy dials that one address and no other. Any other name gets
// nil.
func (h Hosts) Pinned(host, port string) []net.IP {
	if normalize(host) != localhost {
		return nil
	}

	for _, text := range h {
		e, err := parseEntry(text)
		if err == nil && e.port == port && e.loopback() != nil {
			return []net.IP{e.loopback()}
		}
	}

	return nil
}

// Allows reports whether the VM may reach host on port. localhost is
// allowed on every port of an entry on the loopback.
func (h Hosts) Allows(host, port string) bool {
	host = normalize(host)
	if host == "" {
		return false
	}

	if host == localhost {
		return len(h.Pinned(host, port)) > 0
	}

	return slices.ContainsFunc(h, func(text string) bool {
		e, err := parseEntry(text)

		return err == nil && e.matches(host, port)
	})
}

// normalize lowercases a name and removes a trailing dot, because DNS names
// are case-insensitive and example.com. is the same name as example.com.
func normalize(host string) string {
	return strings.ToLower(strings.TrimRight(host, "."))
}

// AddPresets adds an entry for each preset to the allow list of the config
// in content, after its last entry, with its indent and its line ending. It
// changes no other line, so that the comments stay as they are. It returns
// ErrNoAllowList when the list isn't one entry per line, which it sees
// when the result doesn't allow exactly what content did and the presets.
func AddPresets(content []byte, names []string) ([]byte, error) {
	before, err := parse(content)
	if err != nil {
		return nil, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, err
	}

	last, err := lastAllowEntry(&doc)
	if err != nil {
		return nil, err
	}

	lines := strings.SplitAfter(string(content), "\n")
	if last.Line > len(lines) {
		return nil, ErrNoAllowList
	}

	at := lines[last.Line-1]
	indent := at[:len(at)-len(strings.TrimLeft(at, " "))]

	ending := "\n"
	if strings.HasSuffix(at, "\r\n") {
		ending = "\r\n"
	}

	if !strings.HasSuffix(at, "\n") {
		lines[last.Line-1] += ending
	}

	added := make([]string, 0, len(names))
	entries := slices.Clone(before.Allow)

	for _, name := range names {
		added = append(added, indent+"- preset:"+name+ending)
		entries = append(entries, "preset:"+name)
	}

	want, err := expand(entries)
	if err != nil {
		return nil, err
	}

	text := strings.Join(slices.Insert(lines, last.Line, added...), "")

	after, err := parse([]byte(text))
	if err != nil || !slices.Equal(after.Allow, want) {
		return nil, ErrNoAllowList
	}

	return []byte(text), nil
}

// lastAllowEntry is the last entry of the allow list of the document, when
// the list is one entry per line.
func lastAllowEntry(doc *yaml.Node) (*yaml.Node, error) {
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, ErrNoAllowList
	}

	fields := doc.Content[0].Content
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i].Value != "allow" {
			continue
		}

		list := fields[i+1]
		if list.Kind != yaml.SequenceNode || list.Style&yaml.FlowStyle != 0 || len(list.Content) == 0 {
			return nil, ErrNoAllowList
		}

		last := list.Content[len(list.Content)-1]
		if last.Kind != yaml.ScalarNode || last.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			return nil, ErrNoAllowList
		}

		return last, nil
	}

	return nil, ErrNoAllowList
}
