package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *fixture) suggest(answer string) error {
	f.deps.stdin = strings.NewReader(answer)

	return newRootCommand(f.deps).Run(context.Background(), []string{"aibox", "config", "suggest"})
}

func (f *fixture) projectFile(t *testing.T, name string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(f.cwd, name), []byte("x"), 0o600))
}

func (f *fixture) config(t *testing.T) string {
	t.Helper()

	content, err := os.ReadFile(f.project(t).Config)
	require.NoError(t, err)

	return string(content)
}

func TestSuggestAddsTheToolsSaidYesTo(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.projectFile(t, "go.mod")
	f.projectFile(t, "package.json")
	f.projectFile(t, "yarn.lock")
	f.projectFile(t, "uv.lock")
	f.projectFile(t, "README.md")

	// act: Go and npm get the default yes, Python a no
	err := f.suggest("\ny\nn\n")

	// assert
	require.NoError(t, err)

	out := f.stderr.String()
	assert.Contains(t, out, "aibox found these tools in "+filepath.Base(f.cwd)+". The VM can't reach their package registries yet:\n\n")
	assert.Contains(t, out, "  Go       because of go.mod\n           preset:go would allow:\n             proxy.golang.org  sum.golang.org")
	assert.Contains(t, out, "  npm      because of package.json and yarn.lock\n")
	assert.Contains(t, out, "  Python   because of uv.lock\n           preset:pypi would allow:\n             pypi.org  files.pythonhosted.org\n")
	assert.Contains(t, out, "Anyone can upload to package registries")
	assert.Contains(t, out, "Allow the preset for Go? [Y/n] Allow the preset for npm? [Y/n] Allow the preset for Python? [Y/n] ")
	assert.Contains(t, out, "Added the presets for Go and npm.\nStart aibox again for it to apply.\n")
	assert.NotContains(t, out, "\x1b[", "no colors without a terminal")

	config := f.config(t)
	assert.Contains(t, config, "  - code.claude.com           # documentation lookups\n  - preset:go\n  - preset:npm\n")
	assert.NotContains(t, config, "preset:pypi")

	// and suggests only Python after
	f.stderr.Reset()
	require.NoError(t, f.suggest("n\n"))
	assert.Contains(t, f.stderr.String(), "  Python   because of uv.lock\n")
	assert.NotContains(t, f.stderr.String(), "  Go ")
}

func TestSuggestWrapsTheHostsAndPaintsOnATerminal(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.projectFile(t, "go.mod")
	f.deps.stderrWidth = func() (int, bool) { return 50, true }

	// act
	err := f.suggest("n\n")

	// assert
	require.NoError(t, err)

	out := f.stderr.String()
	assert.Contains(t, out, "\x1b[1;36mGo\x1b[0m")
	assert.Contains(t, out, "because of \x1b[1mgo.mod\x1b[0m")
	assert.Contains(t, out, "  \x1b[2mproxy.golang.org  sum.golang.org\x1b[0m\n")
	assert.Contains(t, out, "  \x1b[2mstorage.googleapis.com  vuln.go.dev\x1b[0m\n")
	assert.Contains(t, out, "Nothing changed.")
}

func TestSuggestPaintsNothingWithNoColor(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.projectFile(t, "go.mod")
	f.deps.stderrWidth = func() (int, bool) { return 80, true }
	f.deps.lookupEnv = func(name string) (string, bool) { return "", name == "NO_COLOR" }

	// act
	err := f.suggest("n\n")

	// assert
	require.NoError(t, err)
	assert.NotContains(t, f.stderr.String(), "\x1b[")
}

func TestSuggestChangesNothingWhenToldNo(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.projectFile(t, "package.json")

	// act
	err := f.suggest("no\n")

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "Nothing changed.")
	assert.NotContains(t, f.config(t), "preset:npm")
}

func TestSuggestWithoutATerminalOnlyPrints(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.projectFile(t, "Cargo.toml")
	f.deps.stdinIsTerminal = func() bool { return false }

	// act
	err := f.suggest("y\n")

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "  Rust   because of Cargo.toml\n")
	assert.Contains(t, f.stderr.String(), "Add them to allow with aibox config edit:\n  - preset:cargo\n")
	assert.NotContains(t, f.config(t), "preset:cargo")
}

func TestSuggestPrintsWhatItCannotAdd(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.projectFile(t, "go.mod")
	f.writeConfig(t, "allow: [api.anthropic.com]\n")

	// act
	err := f.suggest("y\n")

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "Add them to allow with aibox config edit:\n  - preset:go\n")
	assert.Equal(t, "allow: [api.anthropic.com]\n", f.config(t))
}

func TestSuggestTakesOnlyPlainFiles(t *testing.T) {
	// arrange
	f := newFixture(t)
	require.NoError(t, os.Mkdir(filepath.Join(f.cwd, "go.mod"), 0o700))
	outside := filepath.Join(t.TempDir(), "package.json")
	require.NoError(t, os.WriteFile(outside, []byte("{}"), 0o600))
	symlink(t, outside, filepath.Join(f.cwd, "package.json"))

	// act
	err := f.suggest("y\n")

	// assert
	require.NoError(t, err)
	assert.Contains(t, f.stderr.String(), "Nothing to suggest for "+filepath.Base(f.cwd)+". aibox looks for these files:\n\n  Cargo.toml  Cargo.lock  go.mod  go.work")
	assert.Contains(t, f.stderr.String(), "It found none of them, or the config allows their tools already.\n")
}

func TestSuggestKeepsAConfigThatIsALink(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.projectFile(t, "go.mod")
	config := f.project(t).Config
	dotfiles := filepath.Join(t.TempDir(), "aibox.yaml")
	require.NoError(t, os.WriteFile(dotfiles, []byte("allow:\n  - api.anthropic.com\n"), 0o600))
	symlink(t, dotfiles, config)

	// act
	err := f.suggest("y\n")

	// assert
	require.NoError(t, err)
	info, err := os.Lstat(config)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "the link stays")
	written, err := os.ReadFile(dotfiles) //nolint:gosec // a temp file of the test
	require.NoError(t, err)
	assert.Equal(t, "allow:\n  - api.anthropic.com\n  - preset:go\n", string(written))
}

// waitingStdin is a terminal no one types into.
func waitingStdin(t *testing.T) io.Reader {
	t.Helper()

	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })

	return r
}

func TestCtrlCEndsTheQuestions(t *testing.T) {
	for name, args := range map[string][]string{
		"suggest": {"aibox", "config", "suggest"},
		"reset":   {"aibox", "projects", "reset"},
		"remove":  {"aibox", "projects", "remove"},
	} {
		t.Run(name, func(t *testing.T) {
			// arrange
			f := newFixture(t)
			f.projectFile(t, "go.mod")
			f.project(t)
			require.NoError(t, f.suggest("n\n"))
			before := f.config(t)
			f.deps.stdin = waitingStdin(t)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)

			// act
			go func() { done <- newRootCommand(f.deps).Run(ctx, args) }()
			cancel()

			// assert
			require.ErrorIs(t, <-done, context.Canceled)
			assert.Equal(t, before, f.config(t))
		})
	}
}
