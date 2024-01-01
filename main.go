package main

import (
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	caddycmd "github.com/caddyserver/caddy/v2/cmd"
	"github.com/gfx-labs/swim/modules/prerender"
	"github.com/gfx-labs/swim/modules/vfs"

	// plug in Caddy modules here
	_ "github.com/caddyserver/caddy/v2/modules/standard"
)

func activate() {

	caddy.RegisterModule(&vfs.SwimVfs{})
	caddy.RegisterModule(&prerender.Prerender{})
	httpcaddyfile.RegisterHandlerDirective("prerender_io", prerender.ParseCaddyFile)

}
func main() {
	activate()
	caddycmd.Main()
}
