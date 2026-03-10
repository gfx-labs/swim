package s3fs

import (
	"github.com/caddyserver/caddy/v2"
	"github.com/gfx-labs/swim/modules/s3fs"
)

func init() {
	caddy.RegisterModule(&s3fs.S3fs{})
}
