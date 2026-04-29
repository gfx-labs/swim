package github_preview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAuthenticateRefresh(t *testing.T) {
	tests := []struct {
		name       string
		token      string
		authHeader string
		want       bool
	}{
		{
			name:       "valid token",
			token:      "secret123",
			authHeader: "Bearer secret123",
			want:       true,
		},
		{
			name:       "invalid token",
			token:      "secret123",
			authHeader: "Bearer wrongtoken",
			want:       false,
		},
		{
			name:       "missing authorization header",
			token:      "secret123",
			authHeader: "",
			want:       false,
		},
		{
			name:       "empty configured token",
			token:      "",
			authHeader: "Bearer anything",
			want:       false,
		},
		{
			name:       "empty configured token and empty header",
			token:      "",
			authHeader: "",
			want:       false,
		},
		{
			name:       "wrong scheme",
			token:      "secret123",
			authHeader: "Basic secret123",
			want:       false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GithubPreview{
				RefreshToken: tt.token,
			}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.authHeader != "" {
				r.Header.Set("Authorization", tt.authHeader)
			}
			got := g.authenticateRefresh(r)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestHandleEvict(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		prePR        int
		preArtifact  int64
		wantStatus   int
		wantEvicted  bool
		wantErrField string
	}{
		{
			name:        "evict cached pr",
			body:        `{"pr":42}`,
			prePR:       42,
			preArtifact: 100,
			wantStatus:  http.StatusOK,
			wantEvicted: true,
		},
		{
			name:        "evict non-existent pr",
			body:        `{"pr":99}`,
			wantStatus:  http.StatusOK,
			wantEvicted: false,
		},
		{
			name:         "missing pr field",
			body:         `{}`,
			wantStatus:   http.StatusBadRequest,
			wantErrField: "pr is required",
		},
		{
			name:         "invalid json",
			body:         `{not json`,
			wantStatus:   http.StatusBadRequest,
			wantErrField: "invalid JSON body",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GithubPreview{
				metadataCache: newMetadataCache(5 * time.Minute),
				artifactCache: newArtifactCache(10),
			}
			if tt.prePR != 0 {
				g.metadataCache.set(tt.prePR, tt.preArtifact, "")
			}

			r := httptest.NewRequest(http.MethodDelete, "/_preview/refresh", bytes.NewBufferString(tt.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			err := g.handleEvict(w, r)
			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, w.Code)

			if tt.wantErrField != "" {
				var resp map[string]string
				require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
				require.Equal(t, tt.wantErrField, resp["error"])
			} else {
				var resp evictResponse
				require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
				require.Equal(t, tt.wantEvicted, resp.Evicted)
			}

			// verify eviction actually removed the entry
			if tt.wantEvicted {
				entry, _ := g.metadataCache.get(tt.prePR)
				require.Nil(t, entry)
			}
		})
	}
}

func TestHandleStatus(t *testing.T) {
	tests := []struct {
		name             string
		prs              map[int]int64
		maxArtifacts     int
		wantPRs          int
		wantArtifacts    int
		wantMaxArtifacts int
	}{
		{
			name:             "empty cache",
			prs:              nil,
			maxArtifacts:     50,
			wantPRs:          0,
			wantArtifacts:    0,
			wantMaxArtifacts: 50,
		},
		{
			name: "some cached entries",
			prs: map[int]int64{
				1:   100,
				200: 300,
			},
			maxArtifacts:     25,
			wantPRs:          2,
			wantArtifacts:    0, // we only populate metadata, not artifact cache
			wantMaxArtifacts: 25,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GithubPreview{
				metadataCache: newMetadataCache(5 * time.Minute),
				artifactCache: newArtifactCache(tt.maxArtifacts),
				MaxArtifacts:  tt.maxArtifacts,
			}

			for pr, artifactID := range tt.prs {
				g.metadataCache.set(pr, artifactID, "")
			}

			r := httptest.NewRequest(http.MethodGet, "/_preview/status", nil)
			w := httptest.NewRecorder()

			err := g.handleStatus(w, r)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, w.Code)

			var resp statusResponse
			require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
			require.Len(t, resp.PRs, tt.wantPRs)
			require.Equal(t, tt.wantArtifacts, resp.Cache.ArtifactCount)
			require.Equal(t, tt.wantMaxArtifacts, resp.Cache.MaxArtifacts)

			// verify individual PRs have expected artifact IDs
			for pr, artifactID := range tt.prs {
				key := fmt.Sprintf("%d", pr)
				ps, ok := resp.PRs[key]
				require.True(t, ok, "expected pr %d in response", pr)
				require.Equal(t, artifactID, ps.ArtifactID)
				require.NotEmpty(t, ps.ResolvedAt)
			}
		})
	}
}

func TestHandleAPIRouting(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{
			name:       "unknown path returns 404",
			method:     http.MethodGet,
			path:       "/_preview/unknown",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "refresh with GET returns 404",
			method:     http.MethodGet,
			path:       "/_preview/refresh",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "status with POST returns 404",
			method:     http.MethodPost,
			path:       "/_preview/status",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "status with GET dispatches correctly",
			method:     http.MethodGet,
			path:       "/_preview/status",
			wantStatus: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GithubPreview{
				RefreshToken:  "test-token",
				ApiPath:       "/_preview",
				metadataCache: newMetadataCache(5 * time.Minute),
				artifactCache: newArtifactCache(10),
			}

			r := httptest.NewRequest(tt.method, tt.path, nil)
			r.Header.Set("Authorization", "Bearer test-token")
			w := httptest.NewRecorder()

			err := g.handleAPI(w, r)
			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, w.Code)

			// verify all responses are JSON
			require.Equal(t, "application/json", w.Header().Get("Content-Type"))
		})
	}
}

func TestHandleAPIUnauthorized(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{
			name:   "POST refresh",
			method: http.MethodPost,
			path:   "/_preview/refresh",
		},
		{
			name:   "DELETE refresh",
			method: http.MethodDelete,
			path:   "/_preview/refresh",
		},
		{
			name:   "GET status",
			method: http.MethodGet,
			path:   "/_preview/status",
		},
		{
			name:   "GET unknown",
			method: http.MethodGet,
			path:   "/_preview/unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GithubPreview{
				RefreshToken:  "real-token",
				ApiPath:       "/_preview",
				metadataCache: newMetadataCache(5 * time.Minute),
				artifactCache: newArtifactCache(10),
			}

			r := httptest.NewRequest(tt.method, tt.path, nil)
			// no Authorization header
			w := httptest.NewRecorder()

			err := g.handleAPI(w, r)
			require.NoError(t, err)
			require.Equal(t, http.StatusUnauthorized, w.Code)

			var resp map[string]string
			require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
			require.Equal(t, "unauthorized", resp["error"])
		})
	}
}
