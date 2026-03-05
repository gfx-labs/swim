package mergefs

import (
	"github.com/caddyserver/caddy/v2"
	"github.com/gfx-labs/swim/modules/mergefs"
)

func init() {
	caddy.RegisterModule(&mergefs.Mergefs{})
}
