package task_test

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/task"
)

type entry struct {
	name     string
	content  string
	typeflag byte
}

func archive(t *testing.T, entries ...entry) io.Reader {
	t.Helper()

	var b bytes.Buffer

	w := tar.NewWriter(&b)

	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}

		require.NoError(t, w.WriteHeader(&tar.Header{Typeflag: typeflag, Name: e.name, Mode: 0o644, Size: int64(len(e.content))}))
		_, err := w.Write([]byte(e.content))
		require.NoError(t, err)
	}

	require.NoError(t, w.Close())

	return &b
}

type files map[string]*bytes.Buffer

func newFiles() files {
	return files{
		task.TranscriptFile: {},
		task.LogFile:        {},
		task.ChangesFile:    {},
		task.ResultFile:     {},
	}
}

func (f files) writers() map[string]io.Writer {
	writers := map[string]io.Writer{}
	for name, b := range f {
		writers[name] = b
	}

	return writers
}

func TestReceiveWritesEachResultToItsFile(t *testing.T) {
	// arrange
	f := newFiles()
	r := archive(t,
		entry{name: task.TranscriptFile, content: "{}\n"},
		entry{name: task.LogFile, content: "warning\n"},
		entry{name: task.ChangesFile, content: "bundle"},
		entry{name: task.ResultFile, content: `{"base":"x"}`},
	)

	// act
	err := task.Receive(r, f.writers())

	// assert
	require.NoError(t, err)
	assert.Equal(t, "{}\n", f[task.TranscriptFile].String())
	assert.Equal(t, "warning\n", f[task.LogFile].String())
	assert.Equal(t, "bundle", f[task.ChangesFile].String())
	assert.Equal(t, `{"base":"x"}`, f[task.ResultFile].String())
}

func TestReceiveRefusesWhatItDoesNotTake(t *testing.T) {
	tests := map[string][]entry{
		"an unknown name":         {{name: "../evil", content: "x"}},
		"a link":                  {{name: task.LogFile, typeflag: tar.TypeSymlink}},
		"a file twice":            {{name: task.LogFile}, {name: task.LogFile}},
		"a file after the result": {{name: task.ResultFile, content: "{}"}, {name: task.LogFile}},
		"no result":               {{name: task.LogFile}},
		"too large a result":      {{name: task.ResultFile, content: strings.Repeat("x", task.MaxResultBytes+1)}},
	}

	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			// act
			err := task.Receive(archive(t, entries...), newFiles().writers())

			// assert
			assert.ErrorIs(t, err, task.ErrBadResults)
		})
	}
}

func TestReceiveRefusesAnArchiveThatIsNone(t *testing.T) {
	// act
	err := task.Receive(strings.NewReader(strings.Repeat("x", 1024)), newFiles().writers())

	// assert
	assert.ErrorIs(t, err, task.ErrBadResults)
}

const (
	base = "1111111111111111111111111111111111111111"
	head = "2222222222222222222222222222222222222222"
)

func TestReadBundleReadsTheHeader(t *testing.T) {
	for _, header := range []string{
		"# v2 git bundle\n-" + base + " one\n" + head + " refs/heads/aibox/task\n\nPACK",
		"# v3 git bundle\n@object-format=sha1\n-" + base + " one\n" + head + " refs/heads/aibox/task\n\nPACK",
	} {
		// act
		bundle, err := task.ReadBundle(strings.NewReader(header))

		// assert
		require.NoError(t, err)
		assert.Equal(t, []string{base}, bundle.Prerequisites)
		assert.Equal(t, map[string]string{"refs/heads/aibox/task": head}, bundle.Refs)
	}
}

func TestReadBundleRefusesWhatIsNoBundleHeader(t *testing.T) {
	for _, header := range []string{
		"",
		"PACK",
		"# v2 git bundle\n" + head + " refs/heads/aibox/task\n",
		"# v2 git bundle\nnot a line\n\n",
		"# v2 git bundle\n-xyz\n\n",
		"# v2 git bundle\n" + strings.Repeat(head+" refs/heads/x\n", 2000) + "\n",
		"# v2 git bundle\n" + strings.Repeat("x", 5000) + "\n\n",
	} {
		// act
		_, err := task.ReadBundle(strings.NewReader(header))

		// assert
		assert.ErrorIs(t, err, task.ErrBadBundle, "%.40q", header)
	}
}

func TestCheckReturnsTheCommitOfTheBranch(t *testing.T) {
	// arrange
	bundle := task.Bundle{Prerequisites: []string{base}, Refs: map[string]string{task.Branch: head}}

	// act
	got, err := bundle.Check(base)

	// assert
	require.NoError(t, err)
	assert.Equal(t, head, got)
}

func TestCheckRefusesABundleThatIsNotTheBranchFromTheBase(t *testing.T) {
	tests := map[string]task.Bundle{
		"another ref":          {Prerequisites: []string{base}, Refs: map[string]string{"refs/heads/main": head}},
		"a second ref":         {Prerequisites: []string{base}, Refs: map[string]string{task.Branch: head, "refs/heads/main": head}},
		"another prerequisite": {Prerequisites: []string{head}, Refs: map[string]string{task.Branch: head}},
	}

	for name, bundle := range tests {
		t.Run(name, func(t *testing.T) {
			// act
			_, err := bundle.Check(base)

			// assert
			assert.ErrorIs(t, err, task.ErrBadBundle)
		})
	}
}

func TestCleanTextLeavesNoControlOrFormatCharacters(t *testing.T) {
	// act
	got := task.CleanText("ok\x1b]52;c;x\x07\tdone\u202eevil\u200b\u0085\xff\nnext")

	// assert
	assert.Equal(t, "ok?]52;c;x? done?evil???\nnext", got)
}

func TestLineCleanerCleansEachLine(t *testing.T) {
	// arrange
	var out bytes.Buffer

	c := task.NewLineCleaner(&out)

	// act
	_, err := c.Write([]byte("aibox: one\x1b[31m\naibox: t"))
	require.NoError(t, err)
	_, err = c.Write([]byte("wo\nlast"))
	require.NoError(t, err)
	require.NoError(t, c.Close())

	// assert
	assert.Equal(t, "aibox: one?[31m\naibox: two\nlast\n", out.String())
}

func TestLineCleanerCutsALongLine(t *testing.T) {
	// arrange
	var out bytes.Buffer

	c := task.NewLineCleaner(&out)

	// act
	_, err := c.Write([]byte(strings.Repeat("x", 10000) + "\nnext\n"))

	// assert
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("x", 4096)+"\nnext\n", out.String())
}

func TestCheckOfTheSettings(t *testing.T) {
	good := []task.Settings{
		{},
		{Model: "opus", MaxTurns: 3, MaxBudgetUSD: 1.5, TimeoutSeconds: 60, GitName: "Some One", GitEmail: "someone@example.com"},
	}
	bad := []task.Settings{
		{MaxTurns: -1},
		{MaxBudgetUSD: -1},
		{TimeoutSeconds: -1},
		{Model: "-x"},
		{Model: "a b"},
		{GitName: "a\nb"},
		{GitName: "a <b>"},
		{GitEmail: "a b"},
		{GitEmail: "<a>"},
	}

	for _, s := range good {
		assert.NoError(t, s.Check(), "%+v", s)
	}

	for _, s := range bad {
		assert.ErrorIs(t, s.Check(), task.ErrBadSettings, "%+v", s)
	}
}
