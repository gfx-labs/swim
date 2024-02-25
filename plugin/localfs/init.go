package localfs

import (
	"github.com/caddyserver/caddy/v2"
	"github.com/gfx-labs/swim/modules/localfs"
)

func init() {
	caddy.RegisterModule(&localfs.Localfs{})
}
