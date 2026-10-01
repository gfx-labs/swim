package github_preview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestBranchPendingHeadRetainsCachedArtifact(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/git/ref/heads/master", jsonHandler(map[string]any{"object": map[string]string{"sha": "newsha"}}))
	mux.HandleFunc("/repos/testowner/testrepo/actions/workflows/build.yml/runs", jsonHandler(map[string]any{"workflow_runs": []ghWorkflowRun{{ID: 22, HeadBranch: "master", HeadSHA: "newsha", Event: "push"}}}))
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs/22/artifacts", jsonHandler(ghArtifactsResponse{}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	g := &GithubPreview{client: newTestClient(srv.URL), metadataCache: newMetadataCache(time.Nanosecond), artifactCache: newArtifactCache(2), log: zap.NewNop()}
	g.metadataCache.setRun("branch:master", 10, "oldsha", "2026-09-30T00:00:00Z", 10)
	g.artifactCache.set(10, afero.NewMemMapFs(), 0, nil)
	_, err := g.fullResolve(context.Background(), "branch:master")
	require.NoError(t, err)
	meta, _ := g.metadataCache.get("branch:master")
	require.Equal(t, int64(10), meta.artifactID)
	require.WithinDuration(t, time.Now(), meta.resolvedAt, time.Second)
}

func TestBranchOlderRunNotPromoted(t *testing.T) {
	cache := newMetadataCache(time.Minute)
	require.True(t, cache.setRun("branch:master", 20, "sha", "2026-10-01T10:00:00Z", 20))
	require.False(t, cache.setRun("branch:master", 10, "sha", "2026-10-01T09:00:00Z", 10))
	meta, _ := cache.get("branch:master")
	require.Equal(t, int64(20), meta.artifactID)
	require.True(t, cache.setRun("branch:master", 10, "force-pushed", "2026-09-01T09:00:00Z", 10))
}

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
