package github_preview

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestClient(url string, opts ...func(*githubClientConfig)) *GithubClient {
	cfg := githubClientConfig{
		owner:        "testowner",
		repo:         "testrepo",
		token:        "test-token",
		apiURL:       url,
		workflow:     "build.yml",
		artifactName: "site",
		artifactType: ".zip",
		timeout:      5 * time.Second,
		limiter:      newRateLimiter(1000, 1000),
		log:          zap.NewNop(),
	}
	for _, o := range opts {
		o(&cfg)
	}
	return newGithubClient(cfg)
}

func jsonHandler(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
}

func TestGetPR(t *testing.T) {
	tests := []struct {
		name     string
		pr       int
		status   int
		response ghPullRequest
		wantErr  string
		wantNum  int
	}{
		{
			name:   "fetches PR by number",
			pr:     42,
			status: http.StatusOK,
			response: ghPullRequest{
				Number:  42,
				Title:   "Add feature X",
				State:   "open",
				HTMLURL: "https://github.com/testowner/testrepo/pull/42",
			},
			wantNum: 42,
		},
		{
			name:    "PR not found",
			pr:      999,
			status:  http.StatusNotFound,
			wantErr: fmt.Sprintf("not found: "),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, fmt.Sprintf("/repos/testowner/testrepo/pulls/%d", tt.pr), r.URL.Path)
				require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
				if tt.status == http.StatusNotFound {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				jsonHandler(tt.response)(w, r)
			}))
			defer srv.Close()

			client := newTestClient(srv.URL)
			pr, err := client.getPR(context.Background(), tt.pr)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				require.Nil(t, pr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.wantNum, pr.Number)
				require.Equal(t, tt.response.Title, pr.Title)
			}
		})
	}
}

func TestResolveArtifact(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/git/ref/heads/my-branch", jsonHandler(map[string]any{"object": map[string]string{"sha": "abc123"}}))
	mux.HandleFunc("/repos/testowner/testrepo/actions/workflows/build.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "abc123", r.URL.Query().Get("head_sha"))
		require.Empty(t, r.URL.Query().Get("branch"))
		jsonHandler(map[string]any{"workflow_runs": []ghWorkflowRun{{ID: 101, HeadBranch: "tag", HeadSHA: "abc123", Event: "push"}, {ID: 100, HeadBranch: "my-branch", HeadSHA: "abc123", Event: "push"}}})(w, r)
	})
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs/100/artifacts", jsonHandler(ghArtifactsResponse{Artifacts: []ghArtifact{{ID: 501, Name: "site"}}}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	run, artifact, err := newTestClient(srv.URL).resolveArtifact(context.Background(), "my-branch")
	require.NoError(t, err)
	require.Equal(t, int64(501), artifact.ID)
	require.Equal(t, int64(100), run.ID)
}

func TestResolveArtifactCheckRunsFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/git/ref/heads/feature/x", jsonHandler(map[string]any{"object": map[string]string{"sha": "abc"}}))
	mux.HandleFunc("/repos/testowner/testrepo/actions/workflows/build.yml/runs", jsonHandler(map[string]any{"workflow_runs": []any{}}))
	mux.HandleFunc("/repos/testowner/testrepo/commits/abc/check-runs", jsonHandler(map[string]any{"check_runs": []any{map[string]any{"app": map[string]string{"slug": "github-actions"}, "details_url": "https://github.com/testowner/testrepo/actions/runs/30/job/10"}}}))
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs/30", jsonHandler(ghWorkflowRun{ID: 30, HeadBranch: "feature/x", HeadSHA: "abc", Event: "push"}))
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs/30/artifacts", jsonHandler(ghArtifactsResponse{Artifacts: []ghArtifact{{ID: 700, Name: "site"}}}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	_, artifact, err := newTestClient(srv.URL).resolveArtifact(context.Background(), "feature/x")
	require.NoError(t, err)
	require.Equal(t, int64(700), artifact.ID)
}

func TestResolvePR(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/testowner/testrepo/pulls/10", jsonHandler(ghPullRequest{
		Number: 10,
		Title:  "My PR",
		State:  "open",
		Head: struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		}{SHA: "deadbeef", Ref: "my-branch"},
	}))

	mux.HandleFunc("/repos/testowner/testrepo/actions/workflows/build.yml/runs", jsonHandler(struct {
		WorkflowRuns []ghWorkflowRun `json:"workflow_runs"`
	}{
		WorkflowRuns: []ghWorkflowRun{
			{ID: 1001, HeadBranch: "my-branch", HeadSHA: "deadbeef"},
		},
	}))

	mux.HandleFunc("/repos/testowner/testrepo/actions/runs/1001/artifacts", jsonHandler(ghArtifactsResponse{
		TotalCount: 1,
		Artifacts: []ghArtifact{
			{ID: 2001, Name: "site", Expired: false},
		},
	}))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(srv.URL)
	result, err := client.ResolvePR(context.Background(), 10)

	require.NoError(t, err)
	require.Equal(t, 10, result.PR.Number)
	require.Equal(t, "deadbeef", result.PR.Head.SHA)
	require.Equal(t, int64(1001), result.WorkflowRun.ID)
	require.Equal(t, int64(2001), result.Artifact.ID)
	require.Equal(t, int64(2001), result.ArtifactID)
}

func TestDownloadArtifact(t *testing.T) {
	// build a minimal zip in memory
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	fw, err := zw.Create("index.html")
	require.NoError(t, err)
	_, err = fw.Write([]byte("<html>test</html>"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	zipBytes := zipBuf.Bytes()

	// server serves the zip directly (client follows redirects automatically)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipBytes)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	fs, size, cleanup, err := client.DownloadArtifact(context.Background(), 9001, 10*1024*1024, "")

	require.NoError(t, err)
	require.NotNil(t, fs)
	require.GreaterOrEqual(t, size, int64(0))
	require.NotNil(t, cleanup)
	defer cleanup()

	// verify the extracted content
	f, err := fs.Open("index.html")
	require.NoError(t, err)
	defer f.Close()

	content, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, "<html>test</html>", string(content))
}
