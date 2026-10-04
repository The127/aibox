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
	"os"
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
}

var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ErrCmdlineFull are mounts that together do not fit on the kernel command
// line.
var ErrCmdlineFull = errors.New("the mounts do not fit on the kernel command line together")

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

// maxCmdlineBytes is what the mounts may take up together on the kernel
// command line, which holds 2048 bytes and needs room for the rest.
// wordBytes is what aibox adds around each entry.
const (
	maxCmdlineBytes = 1024
	wordBytes       = 24
)

// presets maps a preset name to the hosts it allows. An allow entry of the
// form preset:name stands for them.
var presets = map[string][]string{
	// proxy.golang.org redirects module downloads to storage.googleapis.com
	"go":     {"proxy.golang.org", "sum.golang.org", "storage.googleapis.com", "vuln.go.dev", "dl.google.com", "go.dev"},
	"npm":    {"registry.npmjs.org", "registry.yarnpkg.com", "nodejs.org"},
	"pypi":   {"pypi.org", "files.pythonhosted.org"},
	"cargo":  {"crates.io", "static.crates.io", "index.crates.io", "static.rust-lang.org"},
	"github": {"github.com", "api.github.com", "codeload.github.com", "*.githubusercontent.com"},
	// the layers of an image come from production.cloudfront.docker.com
	"docker": {"registry-1.docker.io", "auth.docker.io", "index.docker.io", "production.cloudfront.docker.com"},
}

func presetNames() []string {
	return slices.Sorted(maps.Keys(presets))
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
	// Mounts are folders of the host the VM sees read-only.
	Mounts []Mount `yaml:"mounts"`
	// Path are folders in the VM that go in front of its PATH, or names of
	// variables of the host whose folders do.
	Path []string `yaml:"path"`
	// Env are variables for the command in the VM.
	Env []Variable `yaml:"env"`
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
	host, guest, ok := strings.Cut(text, ":")
	if !ok || strings.Contains(guest, ":") || !cmdlineSafe(guest) {
		return Mount{}, fmt.Errorf("mounts entry %q: %w", text, ErrBadMount)
	}

	host, err := expandHome(host)
	if err != nil {
		return Mount{}, fmt.Errorf("mounts entry %q: %w", text, err)
	}

	if !filepath.IsAbs(host) || !filepath.IsAbs(guest) {
		return Mount{}, fmt.Errorf("mounts entry %q: %w", text, ErrBadMount)
	}

	mount := Mount{Host: filepath.Clean(host), Guest: filepath.Clean(guest)}
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

func expandHome(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, "~/")
	if !ok {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, rest), nil
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
# the hosts a tool needs, with NAME one of PRESETS.
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

# Folders of the host the VM sees read-only, written host:guest, for example:
# mounts:
#   - ~/sdk/go:/opt/go

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
`

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
// writes the default one first.
func Load(path string) (Config, error) {
	if err := EnsureDefault(path); err != nil {
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

// EnsureDefault creates the default file at path unless one exists. Two
// aibox started at once for a new project then both read the same file.
func EnsureDefault(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the path is the project's config file
	if errors.Is(err, fs.ErrExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	text := strings.Replace(defaultFile, "PRESETS", strings.Join(presetNames(), ", "), 1)
	if _, err := io.WriteString(file, text); err != nil {
		_ = file.Close()
		_ = os.Remove(path)

		return fmt.Errorf("write %s: %w", path, err)
	}

	return file.Close()
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

	if cfg.Disk != nil && *cfg.Disk > MaxDiskGiB {
		return Config{}, fmt.Errorf("disk %d: %w", *cfg.Disk, ErrDiskTooLarge)
	}

	if err := checkMounts(cfg.Mounts); err != nil {
		return Config{}, err
	}

	for i, entry := range cfg.Path {
		cleaned, err := parsePathEntry(entry)
		if err != nil {
			return Config{}, err
		}

		cfg.Path[i] = cleaned
	}

	if err := fitsCmdline(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func parsePathEntry(text string) (string, error) {
	if variableName.MatchString(text) {
		return text, nil
	}

	if !filepath.IsAbs(text) || filepath.Clean(text) == "/" || strings.Contains(text, ":") || strings.ContainsFunc(text, unicode.IsControl) {
		return "", fmt.Errorf("path entry %q: %w", text, ErrBadPath)
	}

	return filepath.Clean(text), nil
}

// fitsCmdline fails when the mounts together are too much for the kernel
// command line.
func fitsCmdline(cfg Config) error {
	total := 0

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
	var ports []uint16

	for _, text := range h {
		e, err := parseEntry(text)
		if err != nil {
			continue
		}

		if port, err := strconv.ParseUint(e.port, 10, 16); err == nil {
			ports = append(ports, uint16(port))
		}
	}

	slices.Sort(ports)

	return slices.Compact(ports)
}

// Allows reports whether the VM may reach host on port.
func (h Hosts) Allows(host, port string) bool {
	host = normalize(host)
	if host == "" {
		return false
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
