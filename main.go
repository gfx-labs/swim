package main

import (
	_ "github.com/gfx-labs/swim/plugin"

	// plug in Caddy modules here
	caddycmd "github.com/caddyserver/caddy/v2/cmd"
	_ "github.com/caddyserver/caddy/v2/modules/standard"
)

func main() {
	caddycmd.Main()
}
