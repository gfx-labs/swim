package github_preview

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAuthenticateAPI(t *testing.T) {
	tests := []struct {
		name      string
		apiKey    string
		headerVal string
		want      bool
	}{
		{
			name:      "valid key",
			apiKey:    "secret123",
			headerVal: "secret123",
			want:      true,
		},
		{
			name:      "invalid key",
			apiKey:    "secret123",
			headerVal: "wrongkey",
			want:      false,
		},
		{
			name:      "missing header",
			apiKey:    "secret123",
			headerVal: "",
			want:      false,
		},
		{
			name:      "empty configured key",
			apiKey:    "",
			headerVal: "anything",
			want:      false,
		},
		{
			name:      "both empty",
			apiKey:    "",
			headerVal: "",
			want:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GithubPreview{
				ApiKey: tt.apiKey,
			}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.headerVal != "" {
				r.Header.Set("X-Api-Key", tt.headerVal)
			}
			got := g.authenticateAPI(r)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestHandleEvict(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		preKey       string
		preArtifact  int64
		wantStatus   int
		wantEvicted  bool
		wantErrField string
	}{
		{
			name:        "evict cached pr",
			body:        `{"pr":42}`,
			preKey:      "pr:42",
			preArtifact: 100,
			wantStatus:  http.StatusOK,
			wantEvicted: true,
		},
		{
			name:        "evict non-existent pr",
			body:        `{"pr":99}`,
			wantStatus:  http.StatusOK,
			wantEvicted: true,
		},
		{
			name:         "missing pr field",
			body:         `{}`,
			wantStatus:   http.StatusBadRequest,
			wantErrField: "pr or branch is required",
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
			if tt.preKey != "" {
				g.metadataCache.set(tt.preKey, tt.preArtifact, "")
			}

			r := httptest.NewRequest(http.MethodDelete, "/.well-known/github-preview/refresh", bytes.NewBufferString(tt.body))
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
			if tt.wantEvicted && tt.preKey != "" {
				entry, _ := g.metadataCache.get(tt.preKey)
				require.Nil(t, entry)
			}
		})
	}
}

func TestHandleStatus(t *testing.T) {
	tests := []struct {
		name             string
		entries          map[string]int64
		maxArtifacts     int
		wantEntries      int
		wantArtifacts    int
		wantMaxArtifacts int
	}{
		{
			name:             "empty cache",
			entries:          nil,
			maxArtifacts:     50,
			wantEntries:      0,
			wantArtifacts:    0,
			wantMaxArtifacts: 50,
		},
		{
			name: "some cached entries",
			entries: map[string]int64{
				"pr:1":        100,
				"branch:main": 300,
			},
			maxArtifacts:     25,
			wantEntries:      2,
			wantArtifacts:    0,
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

			for key, artifactID := range tt.entries {
				g.metadataCache.set(key, artifactID, "")
			}

			r := httptest.NewRequest(http.MethodGet, "/.well-known/github-preview/status", nil)
			w := httptest.NewRecorder()

			err := g.handleStatus(w, r)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, w.Code)

			var resp statusResponse
			require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
			require.Len(t, resp.Entries, tt.wantEntries)
			require.Equal(t, tt.wantArtifacts, resp.Cache.ArtifactCount)
			require.Equal(t, tt.wantMaxArtifacts, resp.Cache.MaxArtifacts)

			for key, artifactID := range tt.entries {
				es, ok := resp.Entries[key]
				require.True(t, ok, "expected key %s in response", key)
				require.Equal(t, artifactID, es.ArtifactID)
				require.NotEmpty(t, es.ResolvedAt)
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
			path:       "/.well-known/github-preview/unknown",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "refresh with GET returns 404",
			method:     http.MethodGet,
			path:       "/.well-known/github-preview/refresh",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "status with POST returns 404",
			method:     http.MethodPost,
			path:       "/.well-known/github-preview/status",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "status with GET dispatches correctly",
			method:     http.MethodGet,
			path:       "/.well-known/github-preview/status",
			wantStatus: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GithubPreview{
				ApiKey:        "test-key",
				ApiPath:       "/.well-known/github-preview",
				metadataCache: newMetadataCache(5 * time.Minute),
				artifactCache: newArtifactCache(10),
			}

			r := httptest.NewRequest(tt.method, tt.path, nil)
			r.Header.Set("X-Api-Key", "test-key")
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
			path:   "/.well-known/github-preview/refresh",
		},
		{
			name:   "DELETE refresh",
			method: http.MethodDelete,
			path:   "/.well-known/github-preview/refresh",
		},
		{
			name:   "GET status",
			method: http.MethodGet,
			path:   "/.well-known/github-preview/status",
		},
		{
			name:   "GET unknown",
			method: http.MethodGet,
			path:   "/.well-known/github-preview/unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GithubPreview{
				ApiKey:        "real-key",
				ApiPath:       "/.well-known/github-preview",
				metadataCache: newMetadataCache(5 * time.Minute),
				artifactCache: newArtifactCache(10),
			}

			r := httptest.NewRequest(tt.method, tt.path, nil)
			// no X-Api-Key header
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
