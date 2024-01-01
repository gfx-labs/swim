package vfs

import (
	"fmt"
	"net/http"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
)

type Overlay struct {
	Fs      string      `json:"root,omitempty"`
	Home    string      `json:"home,omitempty"`
	Type    string      `json:"type,omitempty"`
	Headers http.Header `json:"request_headers"`

	Overlays []*Overlay `json:"overlays"`
}

func (co *Overlay) String() string {
	return fmt.Sprintf("root=%s home=%s type=%s", co.Fs, co.Home, co.Type)
}

func (co *Overlay) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	if co.Headers == nil {
		co.Headers = make(http.Header)
	}
	if d.NextArg() {
		// optional arg
		co.Fs = d.Val()
	}
	if d.NextArg() {
		// optional arg
		co.Home = d.Val()
	}
	if d.NextArg() {
		// optional arg
		co.Type = d.Val()
	}
	for nesting := d.Nesting(); d.NextBlock(nesting); {
		vKey := d.Val()
		switch vKey {
		case "header":
			var k, v string
			if !d.Args(&k, &v) {
				return d.ArgErr()
			}
			co.Headers.Add(k, v)
		case "home":
			if !d.Args(&co.Home) {
				// not enough args
				return d.ArgErr()
			}
		case "type":
			if !d.Args(&co.Type) {
				// not enough args
				return d.ArgErr()
			}
		case "root":
			if !d.Args(&co.Fs) {
				// not enough args
				return d.ArgErr()
			}
		case "overlay":
			n := &Overlay{}
			co.Overlays = append(co.Overlays, n)
			if err := n.UnmarshalCaddyfile(d); err != nil {
				return err
			}
		default:
			return d.SyntaxErr("invalid overlay option: " + vKey)
		}
	}
	return nil
}
