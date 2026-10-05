// Package image downloads the VM image of a release of aibox: the kernel
// and the root disk, which a release carries as one archive for each
// architecture. The release workflow builds the images before aibox and
// writes their SHA-256 into it, so aibox takes only the image it was
// released with.
package image

import (
	"archive/tar"
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
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// ReleasesURL is where the files of the releases of aibox are.
const ReleasesURL = "https://github.com/The127/aibox/releases/download"

// digests are the SHA-256 of the image archives of this release, as
// arch:digest separated by commas. The release workflow sets it with
// -ldflags -X. A build from a checkout has none.
var digests string

var (
	// ErrChecksum is an archive that is not the one aibox was released with.
	ErrChecksum = errors.New("the image is not the one of this release")
	// ErrArchive is an archive that holds anything but the kernel and the
	// root disk.
	ErrArchive = errors.New("the archive of the image holds other files than vmlinuz and os.ext4")
)

const (
	// maxFileSize stops an archive that would fill the disk before its
	// checksum is known. The root disk takes about 400 MB.
	maxFileSize = 8 << 30
	// staleDownload is how old a download left by a crashed run must be
	// before Prune removes it, so that it never takes one still running.
	staleDownload  = 24 * time.Hour
	downloadPrefix = ".download-"
)

// files are what the archive of an image holds.
var files = []string{"vmlinuz", "os.ext4"}

// Released tells whether the version is the one of a release. A build from
// a checkout is not: its version is (devel), a pseudo-version or marked
// +dirty.
func Released(version string) bool {
	return semver.IsValid(version) && semver.Canonical(version) == version && !module.IsPseudoVersion(version)
}

// Digest is the SHA-256 of the image archive of the architecture this
// aibox was released with.
func Digest(arch string) (string, bool) {
	return digestOf(digests, arch)
}

func digestOf(list, arch string) (string, bool) {
	for entry := range strings.SplitSeq(list, ",") {
		name, digest, _ := strings.Cut(entry, ":")
		if name == arch && digest != "" {
			return digest, true
		}
	}

	return "", false
}

// Fetcher downloads images from the releases at BaseURL. Progress, if set,
// hears how many bytes of how many arrived so far. The total is -1 when
// the server does not say.
type Fetcher struct {
	BaseURL  string
	Client   *http.Client
	Progress func(done, total int64)
}

// NewClient is a client for downloads that gives up on a server that does
// not answer, but not on a slow download.
func NewClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}}
}

// Fetch downloads the image archive of the release for the architecture,
// checks it against digest and unpacks it into dir. The image appears in
// dir only complete and checked. Another run that fetched it meanwhile
// wins, and an incomplete dir is replaced.
func (f Fetcher) Fetch(ctx context.Context, version, arch, digest, dir string) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil { //nolint:gosec // the folder of the images, as just install-image makes it
		return err
	}

	tmp, err := os.MkdirTemp(parent, downloadPrefix)
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(tmp) }()

	if err := f.download(ctx, version, arch, digest, tmp); err != nil {
		return err
	}

	// MkdirTemp makes the folder for its owner only
	if err := os.Chmod(tmp, 0o755); err != nil { //nolint:gosec // the image is not secret
		return err
	}

	return place(tmp, dir)
}

func (f Fetcher) download(ctx context.Context, version, arch, digest, dir string) error {
	name := "aibox-image_" + arch + ".tar.gz"
	url := f.BaseURL + "/" + version + "/" + name

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := f.Client.Do(req)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}

	var body io.Reader = resp.Body
	if f.Progress != nil {
		body = &counter{r: body, total: resp.ContentLength, report: f.Progress}
	}

	hash := sha256.New()
	if err := unpack(io.TeeReader(body, hash), dir); err != nil {
		return err
	}

	// the checksum covers all of the archive, also what tar did not read
	if _, err := io.Copy(hash, body); err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}

	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return ErrChecksum
	}

	return nil
}

// place renames the downloaded image to dir. A complete image another run
// placed meanwhile stays, an incomplete one gives way.
func place(tmp, dir string) error {
	err := os.Rename(tmp, dir)
	if err == nil || complete(dir) {
		return nil
	}

	if err := os.RemoveAll(dir); err != nil {
		return err
	}

	return os.Rename(tmp, dir)
}

// counter reports how much of the body was read.
type counter struct {
	r      io.Reader
	done   int64
	total  int64
	report func(done, total int64)
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.done += int64(n)
	c.report(c.done, c.total)

	return n, err
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
		if header.Typeflag != tar.TypeReg || !ok || header.Size > maxFileSize {
			return fmt.Errorf("%w: %s", ErrArchive, header.Name)
		}

		if err := write(filepath.Join(dir, name), io.LimitReader(entries, maxFileSize)); err != nil {
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

// Prune removes from parent the images of the releases other than keep,
// and downloads a run that crashed left behind. The image of a build from a
// checkout, right in parent, stays.
func Prune(parent, keep string) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == keep {
			continue
		}

		if Released(entry.Name()) || isStaleDownload(entry) {
			if err := os.RemoveAll(filepath.Join(parent, entry.Name())); err != nil {
				return err
			}
		}
	}

	return nil
}

func isStaleDownload(entry os.DirEntry) bool {
	if !strings.HasPrefix(entry.Name(), downloadPrefix) {
		return false
	}

	info, err := entry.Info()

	return err == nil && time.Since(info.ModTime()) > staleDownload
}
