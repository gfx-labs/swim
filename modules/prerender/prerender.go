package prerender

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

type Prerender struct {
	PrerenderURL *url.URL `json:"prerender_url,omitempty"`
	Token        string   `json:"token,omitempty"`
	PathPrefix   string   `json:"path_prefix,omitempty"`

	CrawlerUserAgents CrawlerUserAgents `json:"user_agents,omitempty"`
	SkippedFileTypes  FileTypes         `json:"skip_file_types,omitempty"`
}

func ParseCaddyFile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var p Prerender
	err := p.UnmarshalCaddyfile(h.Dispenser)
	return &p, err
}

func (co *Prerender) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		if d.NextArg() {
			co.Token = d.Val()
		}
		if d.NextArg() {
			// optional arg
			co.PathPrefix = d.Val()
		}
		var urlString string
		if d.NextArg() {
			// optional arg
			urlString = d.Val()
		} else {
			urlString = "https://service.prerender.io"
		}
		u, err := url.Parse(urlString)
		if err != nil {
			return err
		}
		co.PrerenderURL = u
		if d.NextArg() {
			return d.ArgErr()
		}
	}
	return nil
}

func (s *Prerender) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID: "http.handlers.prerender_io",
		New: func() caddy.Module {
			return new(Prerender)
		},
	}
}

func (p *Prerender) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	shouldPrerender := p.ShouldPrerender(r)
	if shouldPrerender {
		p.PreRenderHandler(w, r)
		return nil
	}
	return next.ServeHTTP(w, r)
}

func (p *Prerender) ShouldPrerender(or *http.Request) bool {
	userAgent := strings.ToLower(or.Header.Get("User-Agent"))
	bufferAgent := or.Header.Get("X-Bufferbot")
	isRequestingPrerenderedPage := false
	reqURL := strings.ToLower(or.URL.String())

	if userAgent == "" {
		return false
	}

	if or.Method != "GET" && or.Method != "HEAD" {
		return false
	}

	if p.SkippedFileTypes.Contains(reqURL) {
		return false
	}

	if _, ok := or.URL.Query()["_escaped_fragment_"]; bufferAgent != "" || ok {
		isRequestingPrerenderedPage = true
	}
	if isRequestingPrerenderedPage {
		return true
	}
	return p.CrawlerUserAgents.Contains(userAgent)
}

func (p *Prerender) buildURL(or *http.Request) string {
	url := p.PrerenderURL

	if !strings.HasSuffix(url.String(), "/") {
		url.Path = url.Path + "/"
	}

	protocol := or.URL.Scheme

	if cf := or.Header.Get("CF-Visitor"); cf != "" {
		match := cfSchemeRegex.FindStringSubmatch(cf)
		if len(match) > 1 {
			protocol = match[1]
		}
	}

	if len(protocol) == 0 {
		protocol = "http"
	}

	if fp := or.Header.Get("X-Forwarded-Proto"); fp != "" {
		protocol = strings.Split(fp, ",")[0]
	}
	apiURL := url.String() + protocol + "://" + or.Host + p.PathPrefix + or.URL.Path + "?" +
		or.URL.RawQuery
	return apiURL
}

func (p *Prerender) PreRenderHandler(rw http.ResponseWriter, or *http.Request) {
	client := &http.Client{}

	req, err := http.NewRequest("GET", p.buildURL(or), nil)
	if err != nil {
		return
	}
	if p.Token != "" {
		req.Header.Set("X-Prerender-Token", p.Token)
	}
	req.Header.Set("User-Agent", or.Header.Get("User-Agent"))
	req.Header.Set("Content-Type", or.Header.Get("Content-Type"))
	req.Header.Set("Accept-Encoding", "gzip")

	res, err := client.Do(req)
	if err != nil {
		return
	}

	rw.Header().Set("Content-Type", res.Header.Get("Content-Type"))

	defer res.Body.Close()

	doGzip := strings.Contains(or.Header.Get("Accept-Encoding"), "gzip")
	isGzip := strings.Contains(res.Header.Get("Content-Encoding"), "gzip")

	if doGzip && !isGzip {
		rw.Header().Set("Content-Encoding", "gzip")
		rw.WriteHeader(res.StatusCode)
		gz := gzip.NewWriter(rw)
		defer gz.Close()
		_, err = io.Copy(gz, res.Body)
		if err != nil {
			return
		}
		err = gz.Flush()
		if err != nil {
			return
		}
	} else if !doGzip && isGzip {
		rw.WriteHeader(res.StatusCode)
		gz, err := gzip.NewReader(res.Body)
		if err != nil {
			return
		}
		defer gz.Close()
		_, err = io.Copy(rw, gz)
		if err != nil {
			return
		}
	} else {
		rw.Header().Set("Content-Encoding", res.Header.Get("Content-Encoding"))
		rw.WriteHeader(res.StatusCode)
		_, err = io.Copy(rw, res.Body)
		if err != nil {
			return
		}
	}
}

func (p *Prerender) PrerenderMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.Token != "" {
			if p.ShouldPrerender(r) {
				p.PreRenderHandler(w, r)
			}
		}
		next.ServeHTTP(w, r)
	})
}
