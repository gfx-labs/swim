package github_preview

import (
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/gfx-labs/swim/modules/github_preview"
)

func init() {
	caddy.RegisterModule(&github_preview.GithubPreview{})
	httpcaddyfile.RegisterHandlerDirective("github_preview", parseCaddyfile)
	// order next to the "fs" directive since github_preview serves the same
	// purpose (sets the filesystem for downstream try_files / file_server)
	httpcaddyfile.RegisterDirectiveOrder("github_preview", httpcaddyfile.After, "fs")
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var g github_preview.GithubPreview
	err := g.UnmarshalCaddyfile(h.Dispenser)
	return &g, err
}
