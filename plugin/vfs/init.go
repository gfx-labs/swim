package vfs

import (
	"github.com/caddyserver/caddy/v2"
	"github.com/gfx-labs/swim/modules/vfs"
)

func init() {
	caddy.RegisterModule(&vfs.Vfs{})
}
