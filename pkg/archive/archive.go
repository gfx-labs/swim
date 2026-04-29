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

// ZipFsFromDisk writes the contents of r to a file at the given path and
// returns a zip-backed afero.Fs that serves files directly from the on-disk
// zip (random access via the central directory, no full extraction into memory).
// parent directories are created automatically.
//
// the returned cleanup function removes the file and must be called when the
// filesystem is no longer needed. the returned digest is the sha256 hex hash
// of the written content.
//
// maxSize limits how many bytes are written to disk. if the stream exceeds
// this limit the file is removed and an error is returned.
func ZipFsFromDisk(r io.Reader, maxSize int64, path string) (rootFs afero.Fs, sizeBytes int64, digest string, cleanup func(), err error) {
	os.MkdirAll(filepath.Dir(path), 0o700)
	f, err := os.Create(path)
	if err != nil {
		return nil, 0, "", nil, fmt.Errorf("create temp file: %w", err)
	}
	tempPath := f.Name()

	// ensure cleanup on any error path
	removeTemp := func() { os.Remove(tempPath) }

	// copy with size limit, computing sha256 as we go
	limited := io.LimitReader(r, maxSize+1)
	h := sha256.New()
	tee := io.TeeReader(limited, h)
	n, err := io.Copy(f, tee)
	if err != nil {
		f.Close()
		removeTemp()
		return nil, 0, "", nil, fmt.Errorf("write temp file: %w", err)
	}
	if n > maxSize {
		f.Close()
		removeTemp()
		return nil, 0, "", nil, fmt.Errorf("archive exceeds max size %d", maxSize)
	}

	digest = "sha256:" + hex.EncodeToString(h.Sum(nil))

	// open the zip from the on-disk file (random access via io.ReaderAt)
	ziprd, err := zip.NewReader(f, n)
	if err != nil {
		f.Close()
		removeTemp()
		return nil, 0, "", nil, fmt.Errorf("open zip: %w", err)
	}

	cleanup = func() {
		f.Close()
		os.Remove(tempPath)
	}

	return zipfs.New(ziprd), n, digest, cleanup, nil
}
