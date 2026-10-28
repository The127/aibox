package task

import (
	"fmt"
	"regexp"
	"strings"
)

// A session on a clone is aibox run with the project as a clone on the
// disk of the VM instead of the project folder of the host. The host
// shares a folder read-only with CloneFile and, for a new clone,
// InputBundle. When the session on the terminal has ended, a second
// session without a terminal sends the results, as a task does, without
// TranscriptFile and LogFile. ChangesFile is a bundle of CloneBranch from
// Clone.Base on.
const (
	// CloneFile holds the Clone as JSON.
	CloneFile = "clone.json"
	// CloneBranch is the branch a session on a clone works on.
	CloneBranch = "refs/heads/aibox/clone"
)

// Clone is what the VM needs to make the clone and send what was done on
// it. Base is the commit the clone started from, which InputBundle has as
// HEAD for a new clone. Remotes are those of the repository on the host,
// so that git in the VM can fetch and push through the git broker.
type Clone struct {
	Base    string   `json:"base"`
	Remotes []Remote `json:"remotes,omitempty"`
}

// Remote is a remote of the repository on the host, by its name and URL.
type Remote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

var (
	commitPattern     = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	remoteNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	// the addresses the git broker serves, with no login in an address of
	// HTTPS, which would put it into the VM, and no local path, which is
	// of no use there
	remoteURLPattern = regexp.MustCompile(`^(https?://[^/@]+/|ssh://([A-Za-z0-9._-]+@)?[^/@:]+(:[0-9]+)?/|[A-Za-z0-9._-]+@[^/@:]+:)[^-]`)
)

// Check returns ErrBadSettings for a base that is no full commit ID, and
// for a remote git could take for an option or whose name it could not
// keep under refs/remotes.
func (c Clone) Check() error {
	if !commitPattern.MatchString(c.Base) {
		return fmt.Errorf("%w: base is %q", ErrBadSettings, c.Base)
	}

	for _, remote := range c.Remotes {
		if !CheckRemote(remote) {
			return fmt.Errorf("%w: remote %q is %q", ErrBadSettings, remote.Name, remote.URL)
		}
	}

	return nil
}

// CheckRemote says whether the remote can go into the clone as it is: an
// address of HTTPS without a login, or of SSH.
func CheckRemote(remote Remote) bool {
	return remoteNamePattern.MatchString(remote.Name) && !strings.HasSuffix(remote.Name, ".lock") && !strings.Contains(remote.Name, "..") &&
		remoteURLPattern.MatchString(remote.URL) && !strings.ContainsFunc(remote.URL, isSpaceOrControl)
}
