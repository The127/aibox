package image_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/image"
)

func TestOnlyTheVersionOfAReleaseHasAnImageToDownload(t *testing.T) {
	for version, released := range map[string]bool{
		"v0.1.0":                               true,
		"v1.2.3":                               true,
		"v1.0.0-rc.1":                          true,
		"(devel)":                              false,
		"":                                     false,
		"v0.0.0-20261005192625-d6074691c060":   false,
		"v0.1.1-0.20261005192625-d6074691c060": false,
		"v0.1.0+dirty":                         false,
		"0.1.0":                                false,
	} {
		assert.Equal(t, released, image.Released(version), version)
	}
}

// release is a release on a fake server: the archive of the image of an
// architecture and checksums.txt.
type release struct {
	version, arch string
	files         map[string]string
	// checksum overrides the checksum of the archive in checksums.txt
	checksum string
	// noImage leaves the archive out of checksums.txt
	noImage bool
	// before runs before the archive is served
	before func()
}

func (r release) archive(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, content := range r.files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}

	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	return buf.Bytes()
}

func (r release) serve(t *testing.T) image.Fetcher {
	t.Helper()

	archive := r.archive(t)
	name := "aibox-image_" + r.arch + ".tar.gz"

	sum := sha256.Sum256(archive)
	checksum := hex.EncodeToString(sum[:])

	if r.checksum != "" {
		checksum = r.checksum
	}

	checksums := "0000000000000000000000000000000000000000000000000000000000000000  aibox_linux_amd64.tar.gz\n"
	if !r.noImage {
		checksums += fmt.Sprintf("%s  %s\n", checksum, name)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/"+r.version+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	mux.HandleFunc("/"+r.version+"/"+name, func(w http.ResponseWriter, _ *http.Request) {
		if r.before != nil {
			r.before()
		}

		_, _ = w.Write(archive)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return image.Fetcher{BaseURL: server.URL, Client: server.Client()}
}

func theImage() map[string]string {
	return map[string]string{"vmlinuz": "a kernel", "os.ext4": "a root disk"}
}

func TestFetchUnpacksTheImageOfTheRelease(t *testing.T) {
	// arrange
	fetcher := release{version: "v0.1.0", arch: "arm64", files: theImage()}.serve(t)
	dir := filepath.Join(t.TempDir(), "image", "v0.1.0")

	// act
	err := fetcher.Fetch(context.Background(), "v0.1.0", "arm64", dir)

	// assert
	require.NoError(t, err)

	for name, content := range theImage() {
		got, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a file of the test
		require.NoError(t, err)
		assert.Equal(t, content, string(got))
	}

	entries, err := os.ReadDir(filepath.Dir(dir))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "nothing but the image is left")
}

func TestFetchRefusesAnArchiveWhoseChecksumDiffers(t *testing.T) {
	// arrange
	fetcher := release{version: "v0.1.0", arch: "arm64", files: theImage(), checksum: "ab"}.serve(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, "v0.1.0")

	// act
	err := fetcher.Fetch(context.Background(), "v0.1.0", "arm64", dir)

	// assert
	require.ErrorIs(t, err, image.ErrChecksum)

	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing of the download is left")
}

func TestFetchRefusesAReleaseWithoutAnImageForTheArchitecture(t *testing.T) {
	// arrange
	fetcher := release{version: "v0.1.0", arch: "arm64", files: theImage(), noImage: true}.serve(t)

	// act
	err := fetcher.Fetch(context.Background(), "v0.1.0", "arm64", filepath.Join(t.TempDir(), "v0.1.0"))

	// assert
	require.ErrorIs(t, err, image.ErrNoImage)
}

func TestFetchRefusesAnArchiveWithOtherFiles(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"a file outside":  {"vmlinuz": "a kernel", "os.ext4": "a root disk", "../outside": "x"},
		"another file":    {"vmlinuz": "a kernel", "os.ext4": "a root disk", "extra": "x"},
		"a file missing":  {"vmlinuz": "a kernel"},
		"a folder of its": {"image/vmlinuz": "a kernel", "image/os.ext4": "a root disk"},
	} {
		t.Run(name, func(t *testing.T) {
			// arrange
			fetcher := release{version: "v0.1.0", arch: "arm64", files: files}.serve(t)
			parent := filepath.Join(t.TempDir(), "image")
			dir := filepath.Join(parent, "v0.1.0")

			// act
			err := fetcher.Fetch(context.Background(), "v0.1.0", "arm64", dir)

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
	err := fetcher.Fetch(context.Background(), "v0.1.0", "arm64", filepath.Join(t.TempDir(), "v0.1.0"))

	// assert
	require.Error(t, err)
	assert.ErrorContains(t, err, "404")
}

func TestFetchKeepsTheImageAnotherRunFetchedMeanwhile(t *testing.T) {
	// arrange
	dir := filepath.Join(t.TempDir(), "v0.1.0")
	other := map[string]string{"vmlinuz": "the other kernel", "os.ext4": "the other root disk"}

	fetcher := release{version: "v0.1.0", arch: "arm64", files: theImage(), before: func() {
		require.NoError(t, os.MkdirAll(dir, 0o750))

		for name, content := range other {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)) //nolint:gosec // a file of the test
		}
	}}.serve(t)

	// act
	err := fetcher.Fetch(context.Background(), "v0.1.0", "arm64", dir)

	// assert
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dir, "vmlinuz")) //nolint:gosec // a file of the test
	require.NoError(t, err)
	assert.Equal(t, "the other kernel", string(got))
}

func TestPruneRemovesTheImagesOfOtherReleasesOnly(t *testing.T) {
	// arrange
	parent := t.TempDir()
	for _, dir := range []string{"v0.1.0", "v0.2.0", "notes"} {
		require.NoError(t, os.Mkdir(filepath.Join(parent, dir), 0o750))
	}

	for _, file := range []string{"vmlinuz", "os.ext4"} {
		require.NoError(t, os.WriteFile(filepath.Join(parent, file), nil, 0o644)) //nolint:gosec // a file of the test
	}

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

	assert.ElementsMatch(t, []string{"v0.2.0", "notes", "vmlinuz", "os.ext4"}, names, "the image of a build from a checkout stays")
}
