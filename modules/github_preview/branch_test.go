package github_preview

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractPR(t *testing.T) {
	tests := []struct {
		name   string
		hostRe string
		host   string
		wantPR int
		wantOk bool
	}{
		{
			name:   "default regex",
			hostRe: defaultHostRe,
			host:   "pr-42.preview.oku.trade",
			wantPR: 42,
			wantOk: true,
		},
		{
			name:   "default regex with port",
			hostRe: defaultHostRe,
			host:   "pr-42.preview.oku.trade:443",
			wantPR: 42,
			wantOk: true,
		},
		{
			name:   "default regex no match",
			hostRe: defaultHostRe,
			host:   "feature-xyz.preview.oku.trade",
			wantOk: false,
		},
		{
			name:   "default regex bare number",
			hostRe: defaultHostRe,
			host:   "42.preview.oku.trade",
			wantOk: false,
		},
		{
			name:   "custom regex bare number",
			hostRe: `^(\d+)\.(.+)$`,
			host:   "42.preview.oku.trade",
			wantPR: 42,
			wantOk: true,
		},
		{
			name:   "custom regex deploy prefix",
			hostRe: `^deploy-(\d+)-preview\.(.+)$`,
			host:   "deploy-42-preview.staging.oku.trade",
			wantPR: 42,
			wantOk: true,
		},
		{
			name:   "non-numeric capture group",
			hostRe: `^pr-(\w+)\.(.+)$`,
			host:   "pr-abc.foo.com",
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
		{
			name:   "no regex match at all",
			hostRe: `^completely-different-(\d+)$`,
			host:   "pr-42.preview.oku.trade",
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

			pr, ok := g.extractPR(r)
			require.Equal(t, tt.wantOk, ok)
			if tt.wantOk {
				require.Equal(t, tt.wantPR, pr)
			}
		})
	}
}
