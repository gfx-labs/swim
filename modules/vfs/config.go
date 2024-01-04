package vfs

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
)

type Overlay struct {
	Root    string      `json:"root,omitempty"`
	WorkDir string      `json:"workdir,omitempty"`
	Type    string      `json:"type,omitempty"`
	Headers http.Header `json:"request_headers"`
}

func (co *Overlay) String() string {
	return fmt.Sprintf("root=%s home=%s type=%s", co.Root, co.WorkDir, co.Type)
}

var rp = caddy.NewReplacer()

func (co *Overlay) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	if co.Headers == nil {
		co.Headers = make(http.Header)
	}
	if d.NextArg() {
		// optional arg
		co.Root = d.Val()
	}
	if d.NextArg() {
		// optional arg
		co.WorkDir = d.Val()
	}
	if d.NextArg() {
		// optional arg
		co.Type = d.Val()
	}
	for nesting := d.Nesting(); d.NextBlock(nesting); {
		vKey := d.Val()
		switch strings.ToLower(vKey) {
		case "header":
			var k, v string
			if !d.Args(&k, &v) {
				return d.ArgErr()
			}
			co.Headers.Add(k, v)
		case "workdir":
			if !d.Args(&co.WorkDir) {
				// not enough args
				return d.ArgErr()
			}
		case "type":
			if !d.Args(&co.Type) {
				// not enough args
				return d.ArgErr()
			}
		case "root":
			if !d.Args(&co.Root) {
				// not enough args
				return d.ArgErr()
			}
		default:
			return d.SyntaxErr("invalid overlay option: " + vKey)
		}
	}
	return nil
}
