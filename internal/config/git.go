package config

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// ErrBadRemote is a git entry whose remote is not host/path.
var ErrBadRemote = errors.New("must be host/path, such as github.com/owner/repo, without https:// and without .git")

// ErrBadBranchPattern is a push pattern that is not a branch name with
// path.Match wildcards.
var ErrBadBranchPattern = errors.New("must be a branch name, such as aibox/*, without refs/heads/")

// ErrNothingAllowed is a git entry that allows neither fetch nor push.
var ErrNothingAllowed = errors.New("allows neither fetch nor push")

// ErrRemoteTwice is a remote that two git entries name.
var ErrRemoteTwice = errors.New("named twice")

// GitRemote is a repository git in the VM may use through aibox, with the
// git login of the host.
type GitRemote struct {
	// Remote is host/path, such as github.com/owner/repo.
	Remote string `yaml:"remote"`
	// Fetch allows clone and fetch.
	Fetch bool `yaml:"fetch"`
	// Push are the branches a push may create or update, as patterns
	// such as aibox/*, where * stands for any part of a name without a /.
	Push []string `yaml:"push"`
}

var (
	// a host name with a dot, whose last label is no number, so that no
	// address and no name of this machine passes
	remoteHost      = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$`)
	remoteComponent = regexp.MustCompile(`^[A-Za-z0-9_.~-]+$`)
)

// checkGit checks the git entries and puts each remote into its plain
// form: a lower case host and no .git at the end.
func checkGit(remotes []GitRemote) error {
	seen := map[string]bool{}

	for i := range remotes {
		remote := &remotes[i]

		name, err := parseRemote(remote.Remote)
		if err != nil {
			return fmt.Errorf("git entry %q: %w", remote.Remote, err)
		}

		if seen[name] {
			return fmt.Errorf("git entry %q: %w", remote.Remote, ErrRemoteTwice)
		}

		seen[name] = true
		remote.Remote = name

		if !remote.Fetch && len(remote.Push) == 0 {
			return fmt.Errorf("git entry %q: %w", name, ErrNothingAllowed)
		}

		for _, pattern := range remote.Push {
			if !isBranchPattern(pattern) {
				return fmt.Errorf("git entry %q, push %q: %w", name, pattern, ErrBadBranchPattern)
			}
		}
	}

	return nil
}

func parseRemote(text string) (string, error) {
	host, repository, ok := strings.Cut(strings.TrimSuffix(strings.TrimSuffix(text, "/"), ".git"), "/")
	host = strings.ToLower(host)

	if !ok || !remoteHost.MatchString(host) {
		return "", ErrBadRemote
	}

	for component := range strings.SplitSeq(repository, "/") {
		if !remoteComponent.MatchString(component) || component == "." || component == ".." {
			return "", ErrBadRemote
		}
	}

	// over SSH the path is an argument of the command on the server, which
	// would read a - as an option and a ~ as the home of a user
	if strings.HasPrefix(repository, "-") || strings.HasPrefix(repository, "~") {
		return "", ErrBadRemote
	}

	return host + "/" + repository, nil
}

// isBranchPattern tells whether a push pattern can match a branch: a
// valid pattern without the characters and components git refuses in
// branch names.
func isBranchPattern(pattern string) bool {
	if pattern == "" || strings.HasPrefix(pattern, "refs/") || strings.HasSuffix(pattern, "/") || strings.Contains(pattern, "..") {
		return false
	}

	if _, err := path.Match(pattern, ""); err != nil {
		return false
	}

	for component := range strings.SplitSeq(pattern, "/") {
		if component == "" || strings.HasPrefix(component, ".") {
			return false
		}
	}

	return !strings.ContainsFunc(pattern, func(r rune) bool {
		return r <= ' ' || r == 0x7f || strings.ContainsRune(`~^:\`, r)
	})
}
