package localfs

import (
	"io/fs"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/spf13/afero"
	"go.uber.org/zap"
)

var _ fs.FS = (*Localfs)(nil)

type Localfs struct {
	Root string `json:"root"`

	a   afero.Fs
	log *zap.Logger
	fs.FS

	closers []func()
}

func (co *Localfs) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	if !d.Next() {
		return d.ArgErr()
	}
	if d.NextArg() {
		// optional arg
		co.Root = d.Val()
	}
	for nesting := d.Nesting(); d.NextBlock(nesting); {
		vKey := d.Val()
		switch strings.ToLower(vKey) {
		case "root":
			if !d.Args(&co.Root) {
				// not enough args
				return d.ArgErr()
			}
		default:
			return d.SyntaxErr("invalid localfs option: " + vKey)
		}
	}
	return nil
}

func (s *Localfs) Open(name string) (fs.File, error) {
	name = strings.Trim(name, "/")
	return s.FS.Open(name)
}

func (s *Localfs) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID: "caddy.fs.localfs",
		New: func() caddy.Module {
			return new(Localfs)
		},
	}
}

func (s *Localfs) Provision(ctx caddy.Context) error {
	rp := caddy.NewReplacer()
	s.Root = rp.ReplaceAll(s.Root, "")
	s.log = ctx.Logger()
	s.a = afero.NewBasePathFs(afero.NewOsFs(), s.Root)
	s.FS = afero.NewIOFS(s.a)

	s.log.Debug("provisioned localfs", zap.Any("root", s.Root))
	return nil
}

func (s *Localfs) Cleanup() error {
	for _, closer := range s.closers {
		closer()
	}
	return nil
}
