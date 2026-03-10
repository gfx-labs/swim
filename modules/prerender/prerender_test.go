package prerender_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gfx-labs/swim/modules/prerender"
	"github.com/stretchr/testify/require"
)

func newPrerender(prerenderURL string) *prerender.Prerender {
	u, _ := url.Parse(prerenderURL)
	return &prerender.Prerender{
		PrerenderURL: u,
		Token:        "test-token",
	}
}

func TestShouldPrerender(t *testing.T) {
	p := newPrerender("https://service.prerender.io")

	tests := []struct {
		name   string
		method string
		path   string
		ua     string
		header map[string]string
		want   bool
	}{
		{
			name:   "crawler user agent",
			method: "GET",
			path:   "/page",
			ua:     "Mozilla/5.0 (compatible; Googlebot/2.1)",
			want:   true,
		},
		{
			name:   "normal user agent",
			method: "GET",
			path:   "/page",
			ua:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
			want:   false,
		},
		{
			name:   "empty user agent",
			method: "GET",
			path:   "/page",
			ua:     "",
			want:   false,
		},
		{
			name:   "POST request",
			method: "POST",
			path:   "/page",
			ua:     "Googlebot",
			want:   false,
		},
		{
			name:   "HEAD request with crawler",
			method: "HEAD",
			path:   "/page",
			ua:     "Googlebot",
			want:   true,
		},
		{
			name:   "skipped file type .js",
			method: "GET",
			path:   "/bundle.js",
			ua:     "Googlebot",
			want:   false,
		},
		{
			name:   "skipped file type .css",
			method: "GET",
			path:   "/style.css",
			ua:     "Googlebot",
			want:   false,
		},
		{
			name:   "file type in query string does not skip",
			method: "GET",
			path:   "/page?file=test.js",
			ua:     "Googlebot",
			want:   true,
		},
		{
			name:   "escaped fragment",
			method: "GET",
			path:   "/page?_escaped_fragment_=",
			ua:     "Mozilla/5.0",
			want:   true,
		},
		{
			name:   "X-Bufferbot header",
			method: "GET",
			path:   "/page",
			ua:     "Mozilla/5.0",
			header: map[string]string{"X-Bufferbot": "1"},
			want:   true,
		},
		{
			name:   "discordbot",
			method: "GET",
			path:   "/page",
			ua:     "Mozilla/5.0 (compatible; Discordbot/2.0)",
			want:   true,
		},
		{
			name:   "twitterbot",
			method: "GET",
			path:   "/page",
			ua:     "Twitterbot/1.0",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "http://example.com"+tt.path, nil)
			if tt.ua != "" {
				req.Header.Set("User-Agent", tt.ua)
			}
			for k, v := range tt.header {
				req.Header.Set(k, v)
			}
			got := p.ShouldPrerender(req)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestBuildURL(t *testing.T) {
	tests := []struct {
		name       string
		prerender  string
		pathPrefix string
		reqURL     string
		headers    map[string]string
		want       string
	}{
		{
			name:      "basic url",
			prerender: "https://service.prerender.io",
			reqURL:    "http://example.com/page",
			want:      "https://service.prerender.io/http://example.com/page?",
		},
		{
			name:      "trailing slash already present",
			prerender: "https://service.prerender.io/",
			reqURL:    "http://example.com/page",
			want:      "https://service.prerender.io/http://example.com/page?",
		},
		{
			name:       "with path prefix",
			prerender:  "https://service.prerender.io",
			pathPrefix: "/prefix",
			reqURL:     "http://example.com/page",
			want:       "https://service.prerender.io/http://example.com/prefix/page?",
		},
		{
			name:      "with query string",
			prerender: "https://service.prerender.io",
			reqURL:    "http://example.com/page?foo=bar",
			want:      "https://service.prerender.io/http://example.com/page?foo=bar",
		},
		{
			name:      "X-Forwarded-Proto overrides scheme",
			prerender: "https://service.prerender.io",
			reqURL:    "http://example.com/page",
			headers:   map[string]string{"X-Forwarded-Proto": "https"},
			want:      "https://service.prerender.io/https://example.com/page?",
		},
		{
			name:      "CF-Visitor overrides scheme",
			prerender: "https://service.prerender.io",
			reqURL:    "http://example.com/page",
			headers:   map[string]string{"CF-Visitor": `{"scheme":"https"}`},
			want:      "https://service.prerender.io/https://example.com/page?",
		},
		{
			name:      "X-Forwarded-Proto takes priority over CF-Visitor",
			prerender: "https://service.prerender.io",
			reqURL:    "http://example.com/page",
			headers: map[string]string{
				"CF-Visitor":        `{"scheme":"http"}`,
				"X-Forwarded-Proto": "https",
			},
			want: "https://service.prerender.io/https://example.com/page?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.prerender)
			require.NoError(t, err)
			p := &prerender.Prerender{
				PrerenderURL: u,
				PathPrefix:   tt.pathPrefix,
			}
			req := httptest.NewRequest("GET", tt.reqURL, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			got := p.BuildURL(req)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestBuildURLNotMutated(t *testing.T) {
	// verify that calling BuildURL multiple times does not mutate PrerenderURL
	u, _ := url.Parse("https://service.prerender.io")
	p := &prerender.Prerender{PrerenderURL: u}

	req := httptest.NewRequest("GET", "http://example.com/page", nil)
	first := p.BuildURL(req)
	second := p.BuildURL(req)
	require.Equal(t, first, second)
	require.Equal(t, "https://service.prerender.io", p.PrerenderURL.String())
}

func TestPreRenderHandler(t *testing.T) {
	// stand up a fake prerender service
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify the token was forwarded
		if r.Header.Get("X-Prerender-Token") != "test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html>prerendered</html>"))
	}))
	defer fake.Close()

	p := newPrerender(fake.URL)

	req := httptest.NewRequest("GET", "http://example.com/page", nil)
	req.Header.Set("User-Agent", "Googlebot")
	rr := httptest.NewRecorder()

	p.PreRenderHandler(rr, req)

	res := rr.Result()
	defer res.Body.Close()

	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "1", res.Header.Get("X-Prerendered"))
	require.Equal(t, "text/html", res.Header.Get("Content-Type"))

	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, "<html>prerendered</html>", string(body))
}

func TestPreRenderHandlerForwardsStatusCode(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer fake.Close()

	p := newPrerender(fake.URL)

	req := httptest.NewRequest("GET", "http://example.com/missing", nil)
	rr := httptest.NewRecorder()

	p.PreRenderHandler(rr, req)

	res := rr.Result()
	defer res.Body.Close()

	require.Equal(t, http.StatusNotFound, res.StatusCode)
	require.Equal(t, "1", res.Header.Get("X-Prerendered"))
}

func TestPrerenderMiddleware(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("prerendered"))
	}))
	defer fake.Close()

	p := newPrerender(fake.URL)

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("original"))
	})

	t.Run("crawler gets prerendered response", func(t *testing.T) {
		nextCalled = false
		req := httptest.NewRequest("GET", "http://example.com/page", nil)
		req.Header.Set("User-Agent", "Googlebot")
		rr := httptest.NewRecorder()

		handler := p.PrerenderMiddleware(next)
		handler.ServeHTTP(rr, req)

		body, _ := io.ReadAll(rr.Result().Body)
		require.Equal(t, "prerendered", string(body))
		require.False(t, nextCalled, "next handler should not be called for crawler")
	})

	t.Run("normal user gets next handler", func(t *testing.T) {
		nextCalled = false
		req := httptest.NewRequest("GET", "http://example.com/page", nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		rr := httptest.NewRecorder()

		handler := p.PrerenderMiddleware(next)
		handler.ServeHTTP(rr, req)

		body, _ := io.ReadAll(rr.Result().Body)
		require.Equal(t, "original", string(body))
		require.True(t, nextCalled, "next handler should be called for normal user")
	})
}
