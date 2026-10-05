// Package image downloads the VM image of a release of aibox: the kernel
// and the root disk, which a release carries as one archive for each
// architecture, listed in its checksums.txt.
package image

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// ReleasesURL is where the files of the releases of aibox are.
const ReleasesURL = "https://github.com/The127/aibox/releases/download"

var (
	// ErrNoImage is a release whose checksums.txt lists no image for the
	// architecture.
	ErrNoImage = errors.New("the release has no image for this architecture")
	// ErrChecksum is an archive that does not match checksums.txt.
	ErrChecksum = errors.New("the image does not match the checksum of the release")
	// ErrArchive is an archive that holds anything but the kernel and the
	// root disk.
	ErrArchive = errors.New("the archive of the image holds other files than vmlinuz and os.ext4")
)

// files are what the archive of an image holds.
var files = []string{"vmlinuz", "os.ext4"}

// Released tells whether the version is the one of a release, which has an
// image to download. A build from a checkout has none: its version is
// (devel), a pseudo-version or marked +dirty.
func Released(version string) bool {
	return semver.IsValid(version) && semver.Build(version) == "" && !module.IsPseudoVersion(version)
}

// Fetcher downloads images from the releases at BaseURL.
type Fetcher struct {
	BaseURL string
	Client  *http.Client
}

// Fetch downloads the image of the release for the architecture into dir,
// which must not exist yet. The image appears there only complete and
// checked. Another run that fetched it meanwhile wins.
func (f Fetcher) Fetch(ctx context.Context, version, arch, dir string) error {
	name := "aibox-image_" + arch + ".tar.gz"

	want, err := f.checksum(ctx, version, name)
	if err != nil {
		return err
	}

	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil { //nolint:gosec // the folder of the images, as just install-image makes it
		return err
	}

	tmp, err := os.MkdirTemp(parent, ".download-")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(tmp) }()

	body, err := f.get(ctx, version+"/"+name)
	if err != nil {
		return err
	}

	defer func() { _ = body.Close() }()

	hash := sha256.New()
	unpacked := unpack(io.TeeReader(body, hash), tmp)

	// the checksum covers all of the archive, also what tar did not read
	if _, err := io.Copy(hash, body); err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}

	if hex.EncodeToString(hash.Sum(nil)) != want {
		return ErrChecksum
	}

	if unpacked != nil {
		return unpacked
	}

	if err := os.Rename(tmp, dir); err != nil {
		if complete(dir) {
			return nil
		}

		return err
	}

	return nil
}

// checksum is the checksum checksums.txt of the release lists for the file.
func (f Fetcher) checksum(ctx context.Context, version, name string) (string, error) {
	body, err := f.get(ctx, version+"/checksums.txt")
	if err != nil {
		return "", err
	}

	defer func() { _ = body.Close() }()

	lines := bufio.NewScanner(body)
	for lines.Scan() {
		sum, file, ok := strings.Cut(lines.Text(), "  ")
		if ok && file == name {
			return sum, nil
		}
	}

	if err := lines.Err(); err != nil {
		return "", fmt.Errorf("read checksums.txt: %w", err)
	}

	return "", ErrNoImage
}

func (f Fetcher) get(ctx context.Context, path string) (io.ReadCloser, error) {
	url := f.BaseURL + "/" + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()

		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}

	return resp.Body, nil
}

// unpack writes the kernel and the root disk of the archive into dir. It
// takes nothing else, so that no name of the archive reaches outside dir.
func unpack(archive io.Reader, dir string) error {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrArchive, err)
	}

	entries := tar.NewReader(gz)

	for {
		header, err := entries.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return fmt.Errorf("%w: %w", ErrArchive, err)
		}

		name, ok := imageFile(header.Name)
		if header.Typeflag != tar.TypeReg || !ok {
			return fmt.Errorf("%w: %s", ErrArchive, header.Name)
		}

		if err := write(filepath.Join(dir, name), entries); err != nil {
			return err
		}
	}

	if !complete(dir) {
		return fmt.Errorf("%w: a file is missing", ErrArchive)
	}

	return nil
}

// imageFile is the file of an image the name in the archive stands for.
func imageFile(name string) (string, bool) {
	for _, file := range files {
		if name == file {
			return file, true
		}
	}

	return "", false
}

// write makes the file from the reader, and fails on a file of the same
// name the archive held before.
func write(path string, content io.Reader) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // the image is not secret
	if err != nil {
		return fmt.Errorf("%w: %w", ErrArchive, err)
	}

	if _, err := io.Copy(file, content); err != nil {
		_ = file.Close()

		return fmt.Errorf("unpack %s: %w", filepath.Base(path), err)
	}

	return file.Close()
}

// complete tells whether the folder holds the kernel and the root disk.
func complete(dir string) bool {
	for _, file := range files {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			return false
		}
	}

	return true
}

// Prune removes the images of the releases in parent other than keep. The
// image of a build from a checkout, right in parent, stays.
func Prune(parent, keep string) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() && Released(entry.Name()) && entry.Name() != keep {
			if err := os.RemoveAll(filepath.Join(parent, entry.Name())); err != nil {
				return err
			}
		}
	}

	return nil
}
