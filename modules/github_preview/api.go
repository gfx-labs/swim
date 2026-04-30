package github_preview

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// handleAPI dispatches refresh API requests
func (g *GithubPreview) handleAPI(w http.ResponseWriter, r *http.Request) error {
	// authenticate
	if !g.authenticateAPI(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "unauthorized",
		})
		return nil
	}

	subpath := strings.TrimPrefix(r.URL.Path, g.ApiPath)
	subpath = strings.TrimPrefix(subpath, "/")

	switch {
	case subpath == "refresh" && r.Method == http.MethodPost:
		return g.handleRefresh(w, r)
	case subpath == "refresh" && r.Method == http.MethodDelete:
		return g.handleEvict(w, r)
	case subpath == "status" && r.Method == http.MethodGet:
		return g.handleStatus(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "not found",
		})
		return nil
	}
}

type refreshRequest struct {
	PR         int    `json:"pr,omitempty"`
	Branch     string `json:"branch,omitempty"`
	ArtifactID int64  `json:"artifact_id,omitempty"`
}

// key returns the cache key from the request, preferring branch if set
func (r *refreshRequest) key() string {
	if r.Branch != "" {
		return "branch:" + r.Branch
	}
	return fmt.Sprintf("pr:%d", r.PR)
}

func (r *refreshRequest) valid() bool {
	return r.PR != 0 || r.Branch != ""
}

type refreshResponse struct {
	Key        string `json:"key"`
	ArtifactID int64  `json:"artifact_id"`
	Cached     bool   `json:"cached"`
	Error      string `json:"error,omitempty"`
}

func (g *GithubPreview) handleRefresh(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid JSON body",
		})
		return nil
	}

	if !req.valid() {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "pr or branch is required",
		})
		return nil
	}

	key := req.key()
	ctx := r.Context()

	// always do a full resolve
	sfKey := "resolve:" + key
	_, err, _ := g.singleflight.Do(sfKey, func() (any, error) {
		fs, err := g.fullResolve(ctx, key)
		if err != nil {
			g.singleflight.Forget(sfKey)
			return nil, err
		}
		return fs, nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, refreshResponse{
			Key:   key,
			Error: err.Error(),
		})
		return nil
	}

	meta, _ := g.metadataCache.get(key)
	aid := int64(0)
	if meta != nil {
		aid = meta.artifactID
	}

	// if client provided an expected artifact_id, verify it matches
	if req.ArtifactID != 0 && aid != req.ArtifactID {
		writeJSON(w, http.StatusConflict, refreshResponse{
			Key:        key,
			ArtifactID: aid,
			Error:      fmt.Sprintf("expected artifact %d but resolved %d", req.ArtifactID, aid),
		})
		return nil
	}

	writeJSON(w, http.StatusOK, refreshResponse{
		Key:        key,
		ArtifactID: aid,
		Cached:     true,
	})
	return nil
}

type evictRequest struct {
	PR     int    `json:"pr,omitempty"`
	Branch string `json:"branch,omitempty"`
}

func (r *evictRequest) key() string {
	if r.Branch != "" {
		return "branch:" + r.Branch
	}
	return fmt.Sprintf("pr:%d", r.PR)
}

func (r *evictRequest) valid() bool {
	return r.PR != 0 || r.Branch != ""
}

type evictResponse struct {
	Key     string `json:"key"`
	Evicted bool   `json:"evicted"`
}

func (g *GithubPreview) handleEvict(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req evictRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid JSON body",
		})
		return nil
	}

	if !req.valid() {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "pr or branch is required",
		})
		return nil
	}

	key := req.key()
	g.evictKey(key)

	writeJSON(w, http.StatusOK, evictResponse{
		Key:     key,
		Evicted: true,
	})
	return nil
}

type statusResponse struct {
	Entries map[string]entryStatus `json:"entries"`
	Cache   cacheStatus            `json:"cache"`
}

type entryStatus struct {
	ArtifactID int64  `json:"artifact_id"`
	ResolvedAt string `json:"resolved_at"`
}

type cacheStatus struct {
	ArtifactCount  int   `json:"artifact_count"`
	TotalSizeBytes int64 `json:"total_size_bytes"`
	MaxArtifacts   int   `json:"max_artifacts"`
}

func (g *GithubPreview) handleStatus(w http.ResponseWriter, r *http.Request) error {
	snapshot := g.metadataCache.snapshot()
	entries := make(map[string]entryStatus, len(snapshot))
	for key, meta := range snapshot {
		entries[key] = entryStatus{
			ArtifactID: meta.artifactID,
			ResolvedAt: meta.resolvedAt.Format(time.RFC3339),
		}
	}

	count, totalBytes := g.artifactCache.stats()

	writeJSON(w, http.StatusOK, statusResponse{
		Entries: entries,
		Cache: cacheStatus{
			ArtifactCount:  count,
			TotalSizeBytes: totalBytes,
			MaxArtifacts:   g.MaxArtifacts,
		},
	})
	return nil
}

func (g *GithubPreview) authenticateAPI(r *http.Request) bool {
	if g.ApiKey == "" {
		return false
	}
	provided := r.Header.Get("X-Api-Key")
	return subtle.ConstantTimeCompare([]byte(provided), []byte(g.ApiKey)) == 1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
