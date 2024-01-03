package vfs

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/afero/tarfs"
	"github.com/spf13/afero/zipfs"
)

func filetypeFromName(filename string) (guess string) {
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

func filesystemFromReader(ft string, r io.Reader) (rootFs afero.Fs, err error) {
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
