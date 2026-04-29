package github_preview

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

type debugResponse struct {
	Key       string       `json:"key"`
	Cache     debugCache   `json:"cache"`
	Github    debugGithub  `json:"github"`
	Config    debugConfig  `json:"config"`
	Errors    []string     `json:"errors"`
}

type debugCache struct {
	Status       string `json:"status"`
	ArtifactID   int64  `json:"artifact_id,omitempty"`
	ResolvedAt   string `json:"resolved_at,omitempty"`
	TTLRemaining string `json:"ttl_remaining,omitempty"`
}

type debugGithub struct {
	PR          *debugPR          `json:"pr"`
	WorkflowRun *debugWorkflowRun `json:"workflow_run"`
	Artifact    *debugArtifact    `json:"artifact"`
}

type debugPR struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HeadSHA string `json:"head_sha"`
	URL     string `json:"url"`
}

type debugWorkflowRun struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Workflow   string `json:"workflow"`
	CreatedAt  string `json:"created_at"`
	URL        string `json:"url"`
}

type debugArtifact struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	Expired   bool   `json:"expired"`
}

type debugConfig struct {
	Repo         string `json:"repo"`
	Workflow     string `json:"workflow,omitempty"`
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
			Workflow:     g.Workflow,
			ArtifactName: g.ArtifactName,
			WorkDir:      g.WorkDir,
			MetadataTTL:  time.Duration(g.MetadataTTL).String(),
		},
		Errors: []string{},
	}

	// populate cache status
	meta, fresh := g.metadataCache.get(key)
	switch {
	case meta == nil:
		resp.Cache.Status = "miss"
	case fresh:
		resp.Cache.Status = "hit"
		resp.Cache.ArtifactID = meta.artifactID
		resp.Cache.ResolvedAt = meta.resolvedAt.Format(time.RFC3339)
		remaining := time.Duration(g.MetadataTTL) - time.Since(meta.resolvedAt)
		if remaining < 0 {
			remaining = 0
		}
		resp.Cache.TTLRemaining = remaining.Truncate(time.Second).String()
	default:
		resp.Cache.Status = "stale"
		resp.Cache.ArtifactID = meta.artifactID
		resp.Cache.ResolvedAt = meta.resolvedAt.Format(time.RFC3339)
		resp.Cache.TTLRemaining = "0s"
	}

	// fetch live github data (uses tryAcquire -- fails fast if rate limited)
	ctx := r.Context()

	// determine branch name for workflow run lookup
	var branch string
	if strings.HasPrefix(key, "branch:") {
		branch = strings.TrimPrefix(key, "branch:")
	} else if strings.HasPrefix(key, "pr:") {
		prStr := strings.TrimPrefix(key, "pr:")
		prNum, err := strconvAtoi(prStr)
		if err == nil {
			prInfo, err := g.client.GetPRInfo(ctx, prNum)
			if err != nil {
				resp.Errors = append(resp.Errors, err.Error())
			} else {
				resp.Github.PR = &debugPR{
					Number:  prInfo.Number,
					Title:   prInfo.Title,
					State:   prInfo.State,
					HeadSHA: prInfo.Head.SHA,
					URL:     prInfo.HTMLURL,
				}
				branch = prInfo.Head.Ref
			}
		}
	}

	if branch != "" {
		run, err := g.client.GetLatestRunInfo(ctx, branch)
		if err != nil {
			resp.Errors = append(resp.Errors, err.Error())
		} else {
			resp.Github.WorkflowRun = &debugWorkflowRun{
				ID:         run.ID,
				Status:     run.Status,
				Conclusion: run.Conclusion,
				Workflow:   run.Path,
				CreatedAt:  run.CreatedAt,
				URL:        run.HTMLURL,
			}

			artifact, err := g.client.GetArtifactInfo(ctx, run.ID)
			if err != nil {
				resp.Errors = append(resp.Errors, err.Error())
			} else {
				resp.Github.Artifact = &debugArtifact{
					ID:        artifact.ID,
					Name:      artifact.Name,
					SizeBytes: artifact.SizeInBytes,
					Expired:   artifact.Expired,
				}

				if meta != nil && meta.artifactID != artifact.ID {
					resp.Errors = append(resp.Errors,
						fmt.Sprintf("cached artifact %d is outdated, latest is %d (cache will refresh on next request or via %s/refresh)",
							meta.artifactID, artifact.ID, g.ApiPath))
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
	return nil
}
