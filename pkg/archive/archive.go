package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/afero/tarfs"
	"github.com/spf13/afero/zipfs"
)

func FiletypeFromName(filename string) (guess string) {
	switch {
	case strings.HasSuffix(filename, ".zip"):
		return ".zip"
	case strings.HasSuffix(filename, ".tar.gz") || strings.HasSuffix(filename, ".tgz"):
		return ".tar.gz"
	case strings.HasSuffix(filename, ".tar"):
		return ".tar"
	}
	return ".tar"
}

func FilesystemFromReader(ft string, r io.Reader) (rootFs afero.Fs, err error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	switch ft {
	// TODO: .xz, .zlib
	case ".zip":
		ziprd, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return nil, err
		}
		rootFs = zipfs.New(ziprd)
	case ".tar.gz":
		tarball, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		tarrd := tar.NewReader(tarball)
		rootFs = tarfs.New(tarrd)
	case ".tar":
		tarrd := tar.NewReader(bytes.NewReader(body))
		rootFs = tarfs.New(tarrd)
	default:
		return nil, fmt.Errorf("unsupported file type: %s", ft)
	}
	return rootFs, nil
}

// OpenZipFs opens an existing zip file on disk and returns a zip-backed
// afero.Fs that serves files via random access (no full extraction into memory).
// the returned cleanup function closes the file handle.
func OpenZipFs(path string) (rootFs afero.Fs, sizeBytes int64, cleanup func(), err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, nil, err
	}
	n := info.Size()

	ziprd, err := zip.NewReader(f, n)
	if err != nil {
		f.Close()
		return nil, 0, nil, fmt.Errorf("open zip: %w", err)
	}

	cleanup = func() { f.Close() }
	return zipfs.New(ziprd), n, cleanup, nil
}

// DownloadZipFs downloads the contents of r to a file at path, computing
// the sha256 digest as it goes, then opens the result as a zip-backed afero.Fs.
// parent directories are created automatically. if the stream exceeds maxSize
// the file is removed and an error is returned.
func DownloadZipFs(r io.Reader, maxSize int64, path string) (rootFs afero.Fs, sizeBytes int64, digest string, cleanup func(), err error) {
	if err := downloadToFile(r, maxSize, path); err != nil {
		return nil, 0, "", nil, err
	}

	// compute digest from the written file
	digest, err = hashFile(path)
	if err != nil {
		os.Remove(path)
		return nil, 0, "", nil, err
	}

	rootFs, sizeBytes, fsCleanup, err := OpenZipFs(path)
	if err != nil {
		os.Remove(path)
		return nil, 0, "", nil, err
	}

	cleanup = func() {
		fsCleanup()
		os.Remove(path)
	}
	return rootFs, sizeBytes, digest, cleanup, nil
}

// downloadToFile writes r to path, enforcing maxSize. creates parent dirs.
func downloadToFile(r io.Reader, maxSize int64, path string) error {
	os.MkdirAll(filepath.Dir(path), 0o700)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	limited := io.LimitReader(r, maxSize+1)
	n, err := io.Copy(f, limited)
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return fmt.Errorf("write file: %w", err)
	}
	if n > maxSize {
		os.Remove(path)
		return fmt.Errorf("archive exceeds max size %d", maxSize)
	}
	return nil
}

// hashFile computes the sha256 digest of a file on disk
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
