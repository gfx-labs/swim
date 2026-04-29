package github_preview

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractKey(t *testing.T) {
	tests := []struct {
		name    string
		hostRe  string
		host    string
		wantKey string
		wantOk  bool
	}{
		{
			name:    "default regex: PR number",
			hostRe:  defaultHostRe,
			host:    "pr-42.preview.oku.trade",
			wantKey: "pr:42",
			wantOk:  true,
		},
		{
			name:    "default regex: branch name",
			hostRe:  defaultHostRe,
			host:    "pr-master.preview.oku.trade",
			wantKey: "branch:master",
			wantOk:  true,
		},
		{
			name:    "default regex: with port",
			hostRe:  defaultHostRe,
			host:    "pr-42.preview.oku.trade:443",
			wantKey: "pr:42",
			wantOk:  true,
		},
		{
			name:   "default regex: no match",
			hostRe: defaultHostRe,
			host:   "42.preview.oku.trade",
			wantOk: false,
		},
		{
			name:    "custom regex: bare number is PR",
			hostRe:  `^(\d+)\.(.+)$`,
			host:    "42.preview.oku.trade",
			wantKey: "pr:42",
			wantOk:  true,
		},
		{
			name:    "custom regex: branch name",
			hostRe:  `^(.+?)\.staging\.oku\.trade$`,
			host:    "master.staging.oku.trade",
			wantKey: "branch:master",
			wantOk:  true,
		},
		{
			name:    "custom regex: feature branch",
			hostRe:  `^(.+?)\.staging\.oku\.trade$`,
			host:    "feature-xyz.staging.oku.trade",
			wantKey: "branch:feature-xyz",
			wantOk:  true,
		},
		{
			name:   "custom regex: no match",
			hostRe: `^(.+?)\.staging\.oku\.trade$`,
			host:   "preview.other.com",
			wantOk: false,
		},
		{
			name:   "empty host",
			hostRe: defaultHostRe,
			host:   "",
			wantOk: false,
		},
		{
			name:   "host exceeding 253 chars",
			hostRe: defaultHostRe,
			host:   "pr-1." + strings.Repeat("a", 250) + ".com",
			wantOk: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GithubPreview{
				hostRegexp: regexp.MustCompile(tt.hostRe),
			}

			r := httptest.NewRequest("GET", "/", nil)
			r.Host = tt.host

			key, ok := g.extractKey(r)
			require.Equal(t, tt.wantOk, ok)
			if tt.wantOk {
				require.Equal(t, tt.wantKey, key)
			}
		})
	}
}
