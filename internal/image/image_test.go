package image_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/image"
)

func TestOnlyTheVersionOfAReleaseCountsAsReleased(t *testing.T) {
	for version, released := range map[string]bool{
		"v0.1.0":                               true,
		"v1.2.3":                               true,
		"v1.0.0-rc.1":                          true,
		"(devel)":                              false,
		"":                                     false,
		"v1":                                   false,
		"v0.0.0-20261005192625-d6074691c060":   false,
		"v0.1.1-0.20261005192625-d6074691c060": false,
		"v0.1.0+dirty":                         false,
		"0.1.0":                                false,
	} {
		assert.Equal(t, released, image.Released(version), version)
	}
}

func TestABuildFromACheckoutKnowsNoDigest(t *testing.T) {
	// act
	_, ok := image.Digest("arm64")

	// assert
	assert.False(t, ok)
}

func TestTheDigestOfAnArchitectureIsTakenFromTheList(t *testing.T) {
	list := "amd64:aaa,arm64:bbb"

	for arch, want := range map[string]string{"amd64": "aaa", "arm64": "bbb", "riscv64": ""} {
		got, ok := image.DigestOf(list, arch)
		assert.Equal(t, want, got, arch)
		assert.Equal(t, want != "", ok, arch)
	}

	_, ok := image.DigestOf("amd64:,arm64:", "amd64")
	assert.False(t, ok, "an empty digest is none")
}

// archive is the tar.gz of the files.
func archive(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}

	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	return buf.Bytes()
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)

	return hex.EncodeToString(sum[:])
}

// serve serves the archive as the image of v0.1.0 for the architecture,
// after before.
func serve(t *testing.T, arch string, content []byte, before func()) image.Fetcher {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/v0.1.0/aibox-image_"+arch+".tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		if before != nil {
			before()
		}

		_, _ = w.Write(content)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return image.Fetcher{BaseURL: server.URL, Client: server.Client()}
}

func theImage() map[string]string {
	return map[string]string{"vmlinuz": "a kernel", "os.ext4": "a root disk"}
}

func readImage(t *testing.T, dir string) map[string]string {
	t.Helper()

	got := map[string]string{}

	for _, name := range []string{"vmlinuz", "os.ext4"} {
		content, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a file of the test
		require.NoError(t, err)

		got[name] = string(content)
	}

	return got
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o750))

	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
}

func TestFetchUnpacksTheImageOfTheRelease(t *testing.T) {
	// arrange
	content := archive(t, theImage())
	fetcher := serve(t, "arm64", content, nil)

	var reported int64

	fetcher.Progress = func(done, _ int64) { reported = done }
	dir := filepath.Join(t.TempDir(), "image", "v0.1.0")

	// act
	err := fetcher.Fetch(context.Background(), "v0.1.0", "arm64", digest(content), dir)

	// assert
	require.NoError(t, err)
	assert.Equal(t, theImage(), readImage(t, dir))
	assert.Equal(t, int64(len(content)), reported)

	entries, err := os.ReadDir(filepath.Dir(dir))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "nothing but the image is left")

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "as just install-image makes it")
}

func TestFetchUnpacksTheKernelForHyperV(t *testing.T) {
	// arrange
	files := theImage()
	files["vmlinuz-hyperv"] = "a kernel for Hyper-V"
	content := archive(t, files)
	fetcher := serve(t, "amd64", content, nil)
	dir := filepath.Join(t.TempDir(), "image", "v0.1.0")

	// act
	err := fetcher.Fetch(context.Background(), "v0.1.0", "amd64", digest(content), dir)

	// assert
	require.NoError(t, err)

	kernel, err := os.ReadFile(filepath.Join(dir, "vmlinuz-hyperv")) //nolint:gosec // a file of the test
	require.NoError(t, err)
	assert.Equal(t, "a kernel for Hyper-V", string(kernel))
}

func TestFetchRefusesAnArchiveWithoutTheKernelsOfItsArchitecture(t *testing.T) {
	withHyperV := theImage()
	withHyperV["vmlinuz-hyperv"] = "a kernel for Hyper-V"

	for name, test := range map[string]struct {
		arch  string
		files map[string]string
	}{
		"amd64 without the kernel for Hyper-V": {arch: "amd64", files: theImage()},
		"arm64 with a kernel for Hyper-V":      {arch: "arm64", files: withHyperV},
	} {
		t.Run(name, func(t *testing.T) {
			// arrange
			content := archive(t, test.files)
			fetcher := serve(t, test.arch, content, nil)
			dir := filepath.Join(t.TempDir(), "image", "v0.1.0")

			// act
			err := fetcher.Fetch(context.Background(), "v0.1.0", test.arch, digest(content), dir)

			// assert
			require.ErrorIs(t, err, image.ErrArchive)
			assert.NoDirExists(t, dir)
		})
	}
}

func TestFetchRefusesAnArchiveThatIsNotTheOneOfTheRelease(t *testing.T) {
	// arrange
	fetcher := serve(t, "arm64", archive(t, theImage()), nil)
	parent := t.TempDir()

	// act
	err := fetcher.Fetch(context.Background(), "v0.1.0", "arm64", digest([]byte("another archive")), filepath.Join(parent, "v0.1.0"))

	// assert
	require.ErrorIs(t, err, image.ErrChecksum)

	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing of the download is left")
}

func TestFetchRefusesAnArchiveWithOtherFiles(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"a file outside":   {"vmlinuz": "a kernel", "os.ext4": "a root disk", "../outside": "x"},
		"another file":     {"vmlinuz": "a kernel", "os.ext4": "a root disk", "extra": "x"},
		"a file missing":   {"vmlinuz": "a kernel"},
		"a folder of its":  {"image/vmlinuz": "a kernel", "image/os.ext4": "a root disk"},
		"macOS's ._ files": {"vmlinuz": "a kernel", "os.ext4": "a root disk", "._vmlinuz": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			// arrange
			content := archive(t, files)
			fetcher := serve(t, "arm64", content, nil)
			parent := filepath.Join(t.TempDir(), "image")
			dir := filepath.Join(parent, "v0.1.0")

			// act
			err := fetcher.Fetch(context.Background(), "v0.1.0", "arm64", digest(content), dir)

			// assert
			require.ErrorIs(t, err, image.ErrArchive)
			assert.NoFileExists(t, filepath.Join(parent, "outside"))
			assert.NoDirExists(t, dir)
		})
	}
}

func TestFetchSaysWhenTheReleaseCannotBeReached(t *testing.T) {
	// arrange
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)

	fetcher := image.Fetcher{BaseURL: server.URL, Client: server.Client()}

	// act
	err := fetcher.Fetch(context.Background(), "v0.1.0", "arm64", "ab", filepath.Join(t.TempDir(), "v0.1.0"))

	// assert
	require.Error(t, err)
	assert.ErrorContains(t, err, "404")
}

func TestFetchKeepsTheImageAnotherRunFetchedMeanwhile(t *testing.T) {
	// arrange
	dir := filepath.Join(t.TempDir(), "v0.1.0")
	other := map[string]string{"vmlinuz": "the other kernel", "os.ext4": "the other root disk"}
	content := archive(t, theImage())

	fetcher := serve(t, "arm64", content, func() {
		writeFiles(t, dir, other)
	})

	// act
	err := fetcher.Fetch(context.Background(), "v0.1.0", "arm64", digest(content), dir)

	// assert
	require.NoError(t, err)
	assert.Equal(t, other, readImage(t, dir))
}

func TestFetchReplacesAnIncompleteImage(t *testing.T) {
	// arrange
	dir := filepath.Join(t.TempDir(), "v0.1.0")
	writeFiles(t, dir, map[string]string{"vmlinuz": "a kernel without its root disk"})

	content := archive(t, theImage())
	fetcher := serve(t, "arm64", content, nil)

	// act
	err := fetcher.Fetch(context.Background(), "v0.1.0", "arm64", digest(content), dir)

	// assert
	require.NoError(t, err)
	assert.Equal(t, theImage(), readImage(t, dir))
}

func TestPruneRemovesTheImagesOfOtherReleasesAndStaleDownloads(t *testing.T) {
	// arrange
	parent := t.TempDir()
	for _, dir := range []string{"v0.1.0", "v0.2.0", "notes", ".download-old", ".download-running"} {
		require.NoError(t, os.Mkdir(filepath.Join(parent, dir), 0o750))
	}

	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(parent, ".download-old"), old, old))
	writeFiles(t, parent, map[string]string{"vmlinuz": "", "os.ext4": ""})

	// act
	err := image.Prune(parent, "v0.2.0")

	// assert
	require.NoError(t, err)

	entries, err := os.ReadDir(parent)
	require.NoError(t, err)

	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	assert.ElementsMatch(t, []string{"v0.2.0", "notes", ".download-running", "vmlinuz", "os.ext4"}, names,
		"the image of a build from a checkout and a download that may still run stay")
}
