package github_preview

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// handleAPI dispatches refresh API requests
func (g *GithubPreview) handleAPI(w http.ResponseWriter, r *http.Request) error {
	// authenticate
	if !g.authenticateRefresh(r) {
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
	PR         int   `json:"pr"`
	ArtifactID int64 `json:"artifact_id,omitempty"`
}

type refreshResponse struct {
	PR         int    `json:"pr"`
	ArtifactID int64  `json:"artifact_id"`
	Verified   bool   `json:"verified"`
	Cached     bool   `json:"cached"`
	Error      string `json:"error,omitempty"`
}

func (g *GithubPreview) handleRefresh(w http.ResponseWriter, r *http.Request) error {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid JSON body",
		})
		return nil
	}

	if req.PR == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "pr is required",
		})
		return nil
	}

	ctx := r.Context()

	if req.ArtifactID != 0 {
		// verify that the artifact belongs to this PR
		if err := g.client.VerifyArtifactForPR(ctx, req.PR, req.ArtifactID); err != nil {
			writeJSON(w, http.StatusForbidden, refreshResponse{
				PR:         req.PR,
				ArtifactID: req.ArtifactID,
				Verified:   false,
				Error:      err.Error(),
			})
			return nil
		}

		// download and cache the artifact
		_, err := g.downloadAndCache(ctx, req.PR, req.ArtifactID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, refreshResponse{
				PR:         req.PR,
				ArtifactID: req.ArtifactID,
				Verified:   true,
				Error:      err.Error(),
			})
			return nil
		}

		writeJSON(w, http.StatusOK, refreshResponse{
			PR:         req.PR,
			ArtifactID: req.ArtifactID,
			Verified:   true,
			Cached:     true,
		})
		return nil
	}

	// no artifact hint -- do full resolution
	res, err := g.client.ResolvePR(ctx, req.PR)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, refreshResponse{
			PR:    req.PR,
			Error: err.Error(),
		})
		return nil
	}

	// download and cache
	_, err = g.downloadAndCache(ctx, req.PR, res.ArtifactID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, refreshResponse{
			PR:         req.PR,
			ArtifactID: res.ArtifactID,
			Error:      err.Error(),
		})
		return nil
	}

	writeJSON(w, http.StatusOK, refreshResponse{
		PR:         req.PR,
		ArtifactID: res.ArtifactID,
		Verified:   true,
		Cached:     true,
	})
	return nil
}

type evictRequest struct {
	PR int `json:"pr"`
}

type evictResponse struct {
	PR      int  `json:"pr"`
	Evicted bool `json:"evicted"`
}

func (g *GithubPreview) handleEvict(w http.ResponseWriter, r *http.Request) error {
	var req evictRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid JSON body",
		})
		return nil
	}

	if req.PR == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "pr is required",
		})
		return nil
	}

	// evict metadata, artifact cache, and global filesystem registration
	meta, _ := g.metadataCache.get(req.PR)
	if meta != nil {
		g.artifactCache.evict(meta.artifactID)
	}
	g.unregisterFs(req.PR)
	evicted := g.metadataCache.evict(req.PR)

	writeJSON(w, http.StatusOK, evictResponse{
		PR:      req.PR,
		Evicted: evicted,
	})
	return nil
}

type statusResponse struct {
	PRs   map[string]prStatus `json:"prs"`
	Cache cacheStatus         `json:"cache"`
}

type prStatus struct {
	ArtifactID int64  `json:"artifact_id"`
	ResolvedAt string `json:"resolved_at"`
}

type cacheStatus struct {
	ArtifactCount  int   `json:"artifact_count"`
	TotalSizeBytes int64 `json:"total_size_bytes"`
	MaxArtifacts   int   `json:"max_artifacts"`
}

func (g *GithubPreview) handleStatus(w http.ResponseWriter, r *http.Request) error {
	entries := g.metadataCache.snapshot()
	prs := make(map[string]prStatus, len(entries))
	for pr, meta := range entries {
		key := fmt.Sprintf("%d", pr)
		prs[key] = prStatus{
			ArtifactID: meta.artifactID,
			ResolvedAt: meta.resolvedAt.Format(time.RFC3339),
		}
	}

	count, totalBytes := g.artifactCache.stats()

	writeJSON(w, http.StatusOK, statusResponse{
		PRs: prs,
		Cache: cacheStatus{
			ArtifactCount:  count,
			TotalSizeBytes: totalBytes,
			MaxArtifacts:   g.MaxArtifacts,
		},
	})
	return nil
}

func (g *GithubPreview) authenticateRefresh(r *http.Request) bool {
	if g.RefreshToken == "" {
		return false
	}
	auth := r.Header.Get("Authorization")
	return auth == "Bearer "+g.RefreshToken
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
