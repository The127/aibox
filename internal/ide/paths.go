package ide

import (
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/the127/aibox/internal/realpath"
)

// vmProject is where the project is in the VM.
const vmProject = "/project"

// paths translates between the paths of the project on the host and in
// the VM. A path outside the project has no translation, so that the VM
// can make the IDE touch nothing else, and learns of nothing else.
type paths struct {
	host string
}

// toHost is the host path of a path in the VM.
func (p paths) toHost(vm string) (string, bool) {
	if !path.IsAbs(vm) {
		return "", false
	}

	vm = path.Clean(vm)
	if vm == vmProject {
		return p.host, true
	}

	rest, ok := strings.CutPrefix(vm, vmProject+"/")
	if !ok {
		return "", false
	}

	host := filepath.Join(p.host, filepath.FromSlash(rest))

	// the VM can put a symlink into the project that leads out of it, and
	// the IDE would follow it
	if !p.inside(host) {
		return "", false
	}

	return host, true
}

// inside tells whether the path stays inside the project once symlinks
// are followed. A path that does not exist yet stands for the first
// folder above it that does. A symlink that leads nowhere is refused,
// since the IDE would create the file it leads to.
func (p paths) inside(host string) bool {
	project, err := realpath.Of(p.host)
	if err != nil {
		return false
	}

	for name := host; ; name = filepath.Dir(name) {
		if resolved, err := realpath.Of(name); err == nil {
			rest, err := filepath.Rel(project, resolved)

			return err == nil && rest != ".." && !strings.HasPrefix(rest, ".."+string(filepath.Separator))
		}

		if _, err := os.Lstat(name); err == nil {
			return false
		}

		if name == filepath.Dir(name) {
			return false
		}
	}
}

// toVM is the VM path of a path on the host.
func (p paths) toVM(host string) (string, bool) {
	if !filepath.IsAbs(host) {
		return "", false
	}

	// the IDE may name the project by the path its symlinks lead to
	rest, ok := below(p.host, host)
	if !ok {
		resolved, err := realpath.Of(p.host)
		if rest, ok = below(resolved, host); err != nil || !ok {
			return "", false
		}
	}

	// a symlink in the project that leads out of it would show the VM what
	// the person selects in a file it cannot read
	if !p.inside(host) {
		return "", false
	}

	return path.Join(vmProject, filepath.ToSlash(rest)), true
}

// below is the path of name relative to folder, if name is in it.
func below(folder, name string) (string, bool) {
	rest, err := filepath.Rel(folder, name)
	if err != nil || rest == ".." || strings.HasPrefix(rest, ".."+string(filepath.Separator)) {
		return "", false
	}

	return rest, true
}

// uriToHost is the file URI on the host of a file URI in the VM.
func (p paths) uriToHost(vm string) (string, bool) {
	u, ok := fileURI(vm)
	if !ok {
		return "", false
	}

	host, ok := p.toHost(u.Path)
	if !ok {
		return "", false
	}

	return uriOf(host), true
}

// uriToVM is the file URI in the VM of a file URI on the host.
func (p paths) uriToVM(host string) (string, bool) {
	u, ok := fileURI(host)
	if !ok {
		return "", false
	}

	// on Windows a URI holds /C:/x for C:\x
	name := u.Path
	if len(name) > 2 && name[0] == '/' && name[2] == ':' {
		name = name[1:]
	}

	vm, ok := p.toVM(filepath.FromSlash(name))
	if !ok {
		return "", false
	}

	return uriOf(vm), true
}

// fileURI reads a URI of a file on this machine. Any other URI has no
// translation.
func fileURI(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
		return nil, false
	}

	return u, true
}

func uriOf(name string) string {
	name = filepath.ToSlash(name)
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}

	return (&url.URL{Scheme: "file", Path: name}).String()
}
