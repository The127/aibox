// Package config reads the settings of a project.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ErrBadHost is an allow entry that no host name can match.
var ErrBadHost = errors.New("not a host name")

// Config holds the settings of one project.
type Config struct {
	// Allow lists the hosts the VM may reach.
	Allow Hosts `yaml:"allow"`
}

// Hosts are host names. An entry of the form *.example.com matches every
// subdomain of example.com, but not example.com itself.
type Hosts []string

// defaultFile is written for a project that has no config yet. It is kept as
// text, so that the person editing it sees why each host is there.
const defaultFile = `# The hosts the VM may reach. Everything else is refused by the proxy on
# the host. An entry of the form *.example.com matches every subdomain of
# example.com.
allow:
  - api.anthropic.com         # the Claude API
  - claude.ai                 # login with a claude.ai account
  - claude.com                # the sign-in page redirects through here
  - platform.claude.com       # login tokens
  - mcp-proxy.anthropic.com   # MCP connectors of a claude.ai account
  - downloads.claude.ai       # update checks
  - code.claude.com           # documentation lookups
`

// Default is the config of a project that has no config file yet.
func Default() Config {
	return Config{Allow: Hosts{
		"api.anthropic.com",
		"claude.ai",
		"claude.com",
		"platform.claude.com",
		"mcp-proxy.anthropic.com",
		"downloads.claude.ai",
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

	if _, err := io.WriteString(file, defaultFile); err != nil {
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

	for i, entry := range cfg.Allow {
		cfg.Allow[i] = normalize(entry)

		if !isHost(cfg.Allow[i]) {
			return Config{}, fmt.Errorf("allow entry %q: %w", entry, ErrBadHost)
		}
	}

	return cfg, nil
}

// isHost says whether a normalized entry can match a host name. The proxy
// asks for a name without a port, so a port or a URL in an entry would
// match nothing.
func isHost(entry string) bool {
	name := strings.TrimPrefix(entry, "*.")

	return name != "" && !strings.ContainsAny(name, "*/:[] ")
}

// Allows reports whether the VM may reach host.
func (h Hosts) Allows(host string) bool {
	host = normalize(host)
	if host == "" {
		return false
	}

	return slices.ContainsFunc(h, func(entry string) bool { return matches(entry, host) })
}

func matches(entry, host string) bool {
	if below, ok := strings.CutPrefix(entry, "*."); ok {
		return strings.HasSuffix(host, "."+below)
	}

	return host == entry
}

// normalize lowercases a name and removes a trailing dot, because DNS names
// are case-insensitive and example.com. is the same name as example.com.
func normalize(host string) string {
	return strings.ToLower(strings.TrimRight(host, "."))
}
