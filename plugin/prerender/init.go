package prerender

import (
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/gfx-labs/swim/modules/prerender"
)

func init() {
	caddy.RegisterModule(&prerender.Prerender{})
	httpcaddyfile.RegisterHandlerDirective("prerender_io", prerender.ParseCaddyFile)
}
