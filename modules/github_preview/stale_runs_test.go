package github_preview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type runsResp struct {
	WorkflowRuns []ghWorkflowRun `json:"workflow_runs"`
}

// staleRunsServer simulates https://github.com/orgs/community/discussions/206725:
// the branch listing returns a weeks-old result set, while the head_sha
// lookup returns the correct run.
func staleRunsServer(t *testing.T, headSHA string, headRuns []ghWorkflowRun) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/branches/feature/x", jsonHandler(map[string]any{
		"commit": map[string]string{"sha": headSHA},
	}))
	mux.HandleFunc("/repos/testowner/testrepo/actions/workflows/build.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if sha := q.Get("head_sha"); sha != "" {
			require.Equal(t, headSHA, sha)
			jsonHandler(runsResp{WorkflowRuns: headRuns})(w, r)
			return
		}
		require.Equal(t, "feature/x", q.Get("branch"))
		jsonHandler(runsResp{WorkflowRuns: []ghWorkflowRun{
			{ID: 10, HeadSHA: "stale", CreatedAt: "2026-09-01T00:00:00Z"},
		}})(w, r)
	})
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs/10/artifacts", jsonHandler(ghArtifactsResponse{
		Artifacts: []ghArtifact{{ID: 100, Name: "site"}},
	}))
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs/20/artifacts", jsonHandler(ghArtifactsResponse{
		Artifacts: []ghArtifact{{ID: 200, Name: "site"}},
	}))
	return httptest.NewServer(mux)
}

func TestResolveBranchPrefersHeadSHA(t *testing.T) {
	t.Run("head_sha lookup wins over stale branch listing", func(t *testing.T) {
		srv := staleRunsServer(t, "fresh", []ghWorkflowRun{
			{ID: 20, HeadSHA: "fresh", CreatedAt: "2026-10-08T00:00:00Z"},
		})
		defer srv.Close()

		res, err := newTestClient(srv.URL).ResolveBranch(context.Background(), "feature/x")
		require.NoError(t, err)
		require.Equal(t, int64(200), res.ArtifactID)
		require.True(t, res.ExactHead())
	})

	t.Run("falls back to branch listing when head has no artifact yet", func(t *testing.T) {
		srv := staleRunsServer(t, "fresh", nil)
		defer srv.Close()

		res, err := newTestClient(srv.URL).ResolveBranch(context.Background(), "feature/x")
		require.NoError(t, err)
		require.Equal(t, int64(100), res.ArtifactID)
		require.False(t, res.ExactHead())
	})
}

func TestListRunsSortsNewestFirst(t *testing.T) {
	srv := httptest.NewServer(jsonHandler(runsResp{WorkflowRuns: []ghWorkflowRun{
		{ID: 1, CreatedAt: "2026-09-01T00:00:00Z"},
		{ID: 3, CreatedAt: "2026-10-01T00:00:00Z"},
		{ID: 2, CreatedAt: "2026-10-01T00:00:00Z"},
	}}))
	defer srv.Close()

	runs, err := newTestClient(srv.URL).listRuns(context.Background(), "branch=x")
	require.NoError(t, err)
	require.Equal(t, []int64{3, 2, 1}, []int64{runs[0].ID, runs[1].ID, runs[2].ID})
}

func TestFullResolveDoesNotRegressToOlderArtifact(t *testing.T) {
	newPreview := func(url string) *GithubPreview {
		return &GithubPreview{
			metadataCache: newMetadataCache(time.Minute),
			artifactCache: newArtifactCache(10),
			client:        newTestClient(url),
			log:           zap.NewNop(),
		}
	}
	key := "branch:feature/x"

	t.Run("keeps newer cached artifact when fallback returns older", func(t *testing.T) {
		srv := staleRunsServer(t, "fresh", nil)
		defer srv.Close()

		g := newPreview(srv.URL)
		cached := afero.NewMemMapFs()
		g.artifactCache.set(500, cached, 0, nil)
		g.metadataCache.set(key, 500, "newer")

		fs, err := g.fullResolve(context.Background(), key)
		require.NoError(t, err)
		require.Same(t, cached, fs)
		meta, fresh := g.metadataCache.get(key)
		require.True(t, fresh)
		require.Equal(t, int64(500), meta.artifactID)
	})

	t.Run("exact head match is trusted even if older", func(t *testing.T) {
		srv := staleRunsServer(t, "fresh", []ghWorkflowRun{{ID: 20, HeadSHA: "fresh"}})
		defer srv.Close()

		g := newPreview(srv.URL)
		g.artifactCache.set(500, afero.NewMemMapFs(), 0, nil)
		older := afero.NewMemMapFs()
		g.artifactCache.set(200, older, 0, nil)
		g.metadataCache.set(key, 500, "newer")

		fs, err := g.fullResolve(context.Background(), key)
		require.NoError(t, err)
		require.Same(t, older, fs)
		meta, _ := g.metadataCache.get(key)
		require.Equal(t, int64(200), meta.artifactID)
	})
}
