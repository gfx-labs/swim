package mergefs

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/spf13/afero"
	"go.uber.org/zap"
)

var _ fs.FS = (*Mergefs)(nil)

type Mergefs struct {
	// ordered list of filesystem layers, first entry has highest priority
	LayersRaw []json.RawMessage `json:"layers,omitempty" caddy:"namespace=caddy.fs inline_key=backend"`

	layers []fs.FS
	a      afero.Fs
	log    *zap.Logger

	fs.FS
	closers []func()
}

func (s *Mergefs) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID: "caddy.fs.merge",
		New: func() caddy.Module {
			return new(Mergefs)
		},
	}
}

func (s *Mergefs) Provision(ctx caddy.Context) error {
	s.log = ctx.Logger()
	s.log.Debug("initializing mergefs")

	if len(s.LayersRaw) == 0 {
		return fmt.Errorf("mergefs: at least one layer is required")
	}

	mods, err := ctx.LoadModule(s, "LayersRaw")
	if err != nil {
		return fmt.Errorf("loading mergefs layers: %w", err)
	}
	for _, mod := range mods.([]any) {
		fsys, ok := mod.(fs.FS)
		if !ok {
			return fmt.Errorf("mergefs: layer module is not fs.FS")
		}
		s.layers = append(s.layers, fsys)
	}

	// build the union: last layer is the base, each earlier layer overlays it
	// CopyOnWriteFs gives us merged directory listings via UnionFile
	var merged afero.Fs = afero.FromIOFS{FS: s.layers[len(s.layers)-1]}
	for i := len(s.layers) - 2; i >= 0; i-- {
		layer := afero.FromIOFS{FS: s.layers[i]}
		merged = afero.NewCopyOnWriteFs(merged, layer)
	}
	merged = afero.NewReadOnlyFs(merged)

	s.a = merged
	s.FS = afero.NewIOFS(s.a)
	s.log.Debug("initialized mergefs", zap.Int("layers", len(s.layers)))
	return nil
}

func (s *Mergefs) Open(name string) (fs.File, error) {
	name = strings.Trim(name, "/")
	return s.FS.Open(name)
}

func (s *Mergefs) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		for nesting := d.Nesting(); d.NextBlock(nesting); {
			key := d.Val()
			switch strings.ToLower(key) {
			case "layer":
				if !d.NextArg() {
					return d.ArgErr()
				}
				name := d.Val()
				modID := "caddy.fs." + name
				unm, err := caddyfile.UnmarshalModule(d, modID)
				if err != nil {
					return err
				}
				fsys, ok := unm.(fs.FS)
				if !ok {
					return d.Errf("module %s (%T) is not a supported file system implementation", modID, unm)
				}
				s.LayersRaw = append(s.LayersRaw, caddyconfig.JSONModuleObject(fsys, "backend", name, nil))
			default:
				return d.SyntaxErr("expected 'layer', got '" + key + "'")
			}
		}
	}
	return nil
}

func (s *Mergefs) Cleanup() error {
	for _, closer := range s.closers {
		closer()
	}
	return nil
}
