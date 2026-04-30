package github_preview

import (
	"net/http"
	"time"
)

type debugResponse struct {
	Key    string      `json:"key"`
	Cache  debugCache  `json:"cache"`
	Config debugConfig `json:"config"`
}

type debugCache struct {
	Status       string `json:"status"`
	ArtifactID   int64  `json:"artifact_id,omitempty"`
	HeadSHA      string `json:"head_sha,omitempty"`
	ResolvedAt   string `json:"resolved_at,omitempty"`
	TTLRemaining string `json:"ttl_remaining,omitempty"`
}

type debugConfig struct {
	Repo         string `json:"repo"`
	ArtifactName string `json:"artifact_name"`
	WorkDir      string `json:"workdir"`
	MetadataTTL  string `json:"metadata_ttl"`
}

func (g *GithubPreview) handleDebug(w http.ResponseWriter, r *http.Request, key string) error {
	w.Header().Set("Cache-Control", "no-store")

	resp := debugResponse{
		Key: key,
		Config: debugConfig{
			Repo:         g.Repo,
			ArtifactName: g.ArtifactName,
			WorkDir:      g.WorkDir,
			MetadataTTL:  time.Duration(g.MetadataTTL).String(),
		},
	}

	meta, fresh := g.metadataCache.get(key)
	switch {
	case meta == nil:
		resp.Cache.Status = "miss"
	case fresh:
		resp.Cache.Status = "hit"
		resp.Cache.ArtifactID = meta.artifactID
		resp.Cache.HeadSHA = meta.headSHA
		resp.Cache.ResolvedAt = meta.resolvedAt.Format(time.RFC3339)
		remaining := time.Duration(g.MetadataTTL) - time.Since(meta.resolvedAt)
		if remaining < 0 {
			remaining = 0
		}
		resp.Cache.TTLRemaining = remaining.Truncate(time.Second).String()
	default:
		resp.Cache.Status = "stale"
		resp.Cache.ArtifactID = meta.artifactID
		resp.Cache.HeadSHA = meta.headSHA
		resp.Cache.ResolvedAt = meta.resolvedAt.Format(time.RFC3339)
		resp.Cache.TTLRemaining = "0s"
	}

	writeJSON(w, http.StatusOK, resp)
	return nil
}
