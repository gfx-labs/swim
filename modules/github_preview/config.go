package github_preview

import (
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
)

// defaults
const (
	defaultArtifactName    = "dist"
	defaultArtifactType    = ".zip"
	defaultWorkDir         = "/"
	defaultMetadataTTL     = 120 * time.Second
	defaultMaxArtifacts    = 50
	defaultMaxArtifactSize = 100 * 1024 * 1024 // 100MB
	defaultApiPath         = "/.well-known/github-preview"
	defaultApiURL          = "https://api.github.com"
	defaultDownloadTimeout = 120 * time.Second
	defaultPruneInterval   = 6 * time.Hour
	defaultReadCacheSize   = 10 * 1024 * 1024 // 10MB per artifact
	defaultRateLimit       = 10.0 // requests per second
	defaultRateBurst       = 20
)

func (g *GithubPreview) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		for nesting := d.Nesting(); d.NextBlock(nesting); {
			key := d.Val()
			switch strings.ToLower(key) {
			case "repo":
				if !d.NextArg() {
					return d.ArgErr()
				}
				g.Repo = d.Val()
			case "token":
				if !d.NextArg() {
					return d.ArgErr()
				}
				g.Token = d.Val()
			case "workflow":
				if !d.NextArg() {
					return d.ArgErr()
				}
				g.Workflow = d.Val()
			case "artifact_name":
				if !d.NextArg() {
					return d.ArgErr()
				}
				g.ArtifactName = d.Val()
			case "artifact_type":
				if !d.NextArg() {
					return d.ArgErr()
				}
				g.ArtifactType = d.Val()
			case "workdir":
				if !d.NextArg() {
					return d.ArgErr()
				}
				g.WorkDir = d.Val()
			case "host_re":
				if !d.NextArg() {
					return d.ArgErr()
				}
				g.HostRe = d.Val()
			case "metadata_ttl":
				if !d.NextArg() {
					return d.ArgErr()
				}
				dur, err := time.ParseDuration(d.Val())
				if err != nil {
					return d.Errf("invalid metadata_ttl: %s", d.Val())
				}
				g.MetadataTTL = Duration(dur)
			case "max_artifacts":
				if !d.NextArg() {
					return d.ArgErr()
				}
				val, err := strconvAtoi(d.Val())
				if err != nil {
					return d.Errf("invalid max_artifacts: %s", d.Val())
				}
				g.MaxArtifacts = val
			case "max_artifact_size":
				if !d.NextArg() {
					return d.ArgErr()
				}
				val, err := parseByteSize(d.Val())
				if err != nil {
					return d.Errf("invalid max_artifact_size: %s", d.Val())
				}
				g.MaxArtifactSize = val
			case "read_cache_size":
				if !d.NextArg() {
					return d.ArgErr()
				}
				val, err := parseByteSize(d.Val())
				if err != nil {
					return d.Errf("invalid read_cache_size: %s", d.Val())
				}
				g.ReadCacheSize = val
			case "api_path":
				if !d.NextArg() {
					return d.ArgErr()
				}
				g.ApiPath = d.Val()
			case "api_key":
				if !d.NextArg() {
					return d.ArgErr()
				}
				g.ApiKey = d.Val()
			case "api_url":
				if !d.NextArg() {
					return d.ArgErr()
				}
				g.ApiURL = d.Val()
			case "prune_interval":
				if !d.NextArg() {
					return d.ArgErr()
				}
				dur, err := time.ParseDuration(d.Val())
				if err != nil {
					return d.Errf("invalid prune_interval: %s", d.Val())
				}
				g.PruneInterval = Duration(dur)
			case "max_artifact_age":
				if !d.NextArg() {
					return d.ArgErr()
				}
				dur, err := time.ParseDuration(d.Val())
				if err != nil {
					return d.Errf("invalid max_artifact_age: %s", d.Val())
				}
				g.MaxArtifactAge = Duration(dur)
			case "stale_while_revalidate":
				if !d.NextArg() {
					return d.ArgErr()
				}
				g.StaleWhileRevalidate = strings.ToLower(d.Val()) == "true"
			case "error_template":
				if !d.NextArg() {
					return d.ArgErr()
				}
				g.ErrorTemplate = d.Val()
			case "error_template_file":
				if !d.NextArg() {
					return d.ArgErr()
				}
				g.ErrorTemplateFile = d.Val()
			default:
				return d.SyntaxErr("invalid github_preview option: " + key)
			}
		}
	}
	return nil
}

// strconvAtoi parses a non-negative integer from a string without importing strconv
func strconvAtoi(s string) (int, error) {
	if len(s) == 0 {
		return 0, &parseError{s}
	}
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, &parseError{s}
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

type parseError struct{ val string }

func (e *parseError) Error() string { return "invalid integer: " + e.val }

// parseByteSize parses a human-readable byte size like "100MB", "1GB", "512KB"
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.ToUpper(s)

	multiplier := int64(1)
	switch {
	case strings.HasSuffix(s, "GB"):
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KB"):
		multiplier = 1024
		s = strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	}

	n, err := strconvAtoi(strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	return int64(n) * multiplier, nil
}
