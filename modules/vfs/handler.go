package vfs

import (
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/spf13/afero"
	"go.uber.org/zap"
)

var _ fs.FS = (*Vfs)(nil)

type Vfs struct {
	Overlay *Overlay `json:"overlay"`

	a afero.Fs

	log *zap.Logger

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
	return s.FS.Open(name)
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
	s.log = ctx.Logger()
	s.log.Debug("initializing vfs", zap.Any("fs", s.Overlay))
	start := time.Now()
	s.Overlay.resolvePlaceholders()
	srv, err := s.Overlay.OpenFilesystem()
	if err != nil {
		return fmt.Errorf("initialize overlay %s: %w", s.Overlay.String(), err)
	}
	s.a = srv
	s.FS = afero.NewIOFS(s.a)
	s.log.Debug("initialized vfs", zap.Any("fs", s.Overlay), zap.Duration("took", time.Since(start)))

	return nil
}

func (s *Vfs) Cleanup() error {
	for _, closer := range s.closers {
		closer()
	}
	return nil
}
