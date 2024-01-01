package vfs

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/spf13/afero"
	"github.com/spf13/afero/tarfs"
	"github.com/spf13/afero/zipfs"
)

var _ fs.FS = (*Vfs)(nil)

type Vfs struct {
	Overlay *Overlay `json:"overlay"`

	a afero.Fs
	fs.FS
	closers []func()
}

func (s *Vfs) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	s.Overlay = &Overlay{}
	for d.Next() {
		if err := s.Overlay.UnmarshalCaddyfile(d); err != nil {
			return err
		}
		if d.NextArg() {
			// too many args
			return d.ArgErr()
		}
	}
	return nil
}

func (s *Vfs) Open(name string) (fs.File, error) {
	name = strings.Trim(name, "/")
	return afero.NewIOFS(s.a).Open(name)
}

func (s *Vfs) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID: "caddy.fs.vfs",
		New: func() caddy.Module {
			return new(Vfs)
		},
	}
}

func (s *Vfs) Provision(ctx caddy.Context) error {
	srv, err := NewFileServer(s.Overlay)
	if err != nil {
		return err
	}
	s.a = srv
	s.FS = afero.NewIOFS(s.a)
	return nil
}

func (s *Vfs) Cleanup() error {
	for _, closer := range s.closers {
		closer()
	}
	return nil
}

func NewFileServer(args *Overlay) (afero.Fs, error) {
	hfs, err := createFileServer(args)
	if err != nil {
		return nil, fmt.Errorf("initialize overlay %s: %w", args.String(), err)
	}
	for _, v := range args.Overlays {
		ov, err := createFileServer(v)
		if err != nil {
			return nil, fmt.Errorf("initialize overlay %s: %w", v.String(), err)
		}
		// it will look in ov first, then hfs
		hfs = afero.NewCopyOnWriteFs(hfs, ov)
	}
	return hfs, nil
}
func createFileServer(args *Overlay) (afero.Fs, error) {
	resp, err := createRawFileServer(args)
	if err != nil {
		return nil, err
	}
	if args.Home != "" {
		resp = afero.NewBasePathFs(resp, args.Home)
	}
	return resp, nil
}

func createRawFileServer(args *Overlay) (afero.Fs, error) {
	if strings.HasPrefix(args.Fs, "http") || strings.HasPrefix(args.Fs, "https") {
		return createFromHttp(args)
	}
	return createFromFs(args)
}

func createFromFs(args *Overlay) (afero.Fs, error) {
	fd, err := os.Open(args.Fs)
	if err != nil {
		return nil, err
	}
	rootFs, err := createFromReader(args, fd)
	if err != nil {
		return nil, err
	}
	return rootFs, nil
}

func createFromReader(args *Overlay, r io.Reader) (rootFs afero.Fs, err error) {
	rootFs = afero.NewOsFs()
	ft := args.Type
	if ft == "" {
		if strings.HasSuffix(args.Fs, ".tar.gz") {
			ft = ".tar.gz"
		} else {
			ft = path.Ext(args.Fs)
		}
	}
	switch ft {
	case ".zip":
		body, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		ziprd, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return nil, err
		}
		rootFs = zipfs.New(ziprd)
	case ".tar.gz", ".tgz":
		body, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		tarball, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		tarrd := tar.NewReader(tarball)
		rootFs = tarfs.New(tarrd)
		d, _ := afero.ReadDir(rootFs, "/")
		log.Println("files", d)
		//for _, v := range d {
		//}
	case ".tar":
		body, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		tarrd := tar.NewReader(bytes.NewReader(body))
		rootFs = tarfs.New(tarrd)
	default:
		rootFs = afero.NewBasePathFs(rootFs, args.Fs)
	}
	return rootFs, nil
}

func createFromHttp(args *Overlay) (afero.Fs, error) {
	req, err := http.NewRequest(http.MethodGet, args.Fs, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range args.Headers {
		for _, vv := range v {
			req.Header.Add(k, vv)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("unable to get network resource: %s", resp.Status)
	}
	rootFs, err := createFromReader(args, resp.Body)
	if err != nil {
		return nil, err
	}
	return rootFs, nil
}
