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
	"slices"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// ErrBadHost is an allow entry that no host name can match.
var ErrBadHost = errors.New("not a host name")

// ErrNotPositive is a memory or cpus setting below one.
var ErrNotPositive = errors.New("must be at least 1")

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

// ErrMountsTooLong are mounts whose guest paths together do not fit on the
// kernel command line.
var ErrMountsTooLong = errors.New("the guest paths of the mounts are too long together")

// reservedPaths are what the VM mounts itself, the two shares and the
// kernel file systems, and the folders its programs live in. A mount on,
// above or inside one of them hides something the VM needs.
var reservedPaths = []string{
	"/project", "/home/user", "/dev", "/proc", "/sys", "/run", "/tmp",
	"/etc", "/bin", "/sbin", "/lib", "/lib64", "/usr", "/root",
}

// maxMountBytes is what the guest paths of all mounts may take up together.
// The kernel command line holds 2048 bytes, and the rest of it needs room.
const maxMountBytes = 1024

// presets maps a preset name to the hosts it allows. An allow entry of the
// form preset:name stands for them.
var presets = map[string][]string{
	"go":     {"proxy.golang.org", "sum.golang.org", "vuln.go.dev", "dl.google.com", "go.dev"},
	"npm":    {"registry.npmjs.org", "registry.yarnpkg.com", "nodejs.org"},
	"pypi":   {"pypi.org", "files.pythonhosted.org"},
	"cargo":  {"crates.io", "static.crates.io", "index.crates.io", "static.rust-lang.org"},
	"github": {"github.com", "api.github.com", "codeload.github.com", "*.githubusercontent.com"},
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
	// Mounts are folders of the host the VM sees read-only.
	Mounts []Mount `yaml:"mounts"`
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
	if slices.ContainsFunc(reservedPaths, mount.touches) {
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

// touches tells whether the mount is on the path, above it or inside it.
func (m Mount) touches(path string) bool {
	return m.Guest == path || isBelow(path, m.Guest) || isBelow(m.Guest, path)
}

func isBelow(path, folder string) bool {
	return strings.HasPrefix(path, strings.TrimSuffix(folder, "/")+"/")
}

// checkMounts fails when two mounts are on the same path or one is inside
// the other, and when the guest paths do not fit on the kernel command line.
func checkMounts(mounts []Mount) error {
	total := 0

	for i, a := range mounts {
		total += len(a.Guest)

		for _, b := range mounts[:i] {
			if a.touches(b.Guest) {
				return fmt.Errorf("mounts %s and %s: %w", b.Guest, a.Guest, ErrMountOverlap)
			}
		}
	}

	if total > maxMountBytes {
		return fmt.Errorf("%d mounts: %w", len(mounts), ErrMountsTooLong)
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

# Folders of the host the VM sees read-only, written host:guest, for example:
# mounts:
#   - ~/sdk/go:/opt/go
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
	if err := writeDefault(path); err != nil {
		return Config{}, fmt.Errorf("write %s: %w", path, err)
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

// writeDefault creates the default file unless one exists. Two aibox started
// at once for a new project then both read the same file.
func writeDefault(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the path is the project's config file
	if errors.Is(err, fs.ErrExist) {
		return nil
	}

	if err != nil {
		return err
	}

	text := strings.Replace(defaultFile, "PRESETS", strings.Join(presetNames(), ", "), 1)
	if _, err := io.WriteString(file, text); err != nil {
		_ = file.Close()
		_ = os.Remove(path)

		return err
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

	if err := checkMounts(cfg.Mounts); err != nil {
		return Config{}, err
	}

	return cfg, nil
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
