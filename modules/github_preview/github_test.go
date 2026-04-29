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
)

func newTestClient(url string, opts ...func(*githubClientConfig)) *GithubClient {
	cfg := githubClientConfig{
		owner:           "testowner",
		repo:            "testrepo",
		token:           "test-token",
		apiURL:          url,
		workflow:        "build.yml",
		artifactName:    "site",
		artifactType:    ".zip",
		apiTimeout:      5 * time.Second,
		downloadTimeout: 5 * time.Second,
		limiter:         newRateLimiter(1000, 1000),
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
		name    string
		pr      int
		status  int
		response ghPullRequest
		wantErr string
		wantNum int
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

func TestFindWorkflowRun(t *testing.T) {
	tests := []struct {
		name     string
		workflow string
		response ghWorkflowRunsResponse
		wantErr  string
		wantID   int64
	}{
		{
			name:     "matches workflow by suffix",
			workflow: "build.yml",
			response: ghWorkflowRunsResponse{
				TotalCount: 2,
				WorkflowRuns: []ghWorkflowRun{
					{
						ID:         100,
						Status:     "completed",
						Conclusion: "success",
						HeadBranch: "feature-x",
						HeadSHA:    "abc123",
						Path:       ".github/workflows/test.yml",
					},
					{
						ID:         101,
						Status:     "completed",
						Conclusion: "success",
						HeadBranch: "feature-x",
						HeadSHA:    "abc123",
						Path:       ".github/workflows/build.yml",
					},
				},
			},
			wantID: 101,
		},
		{
			name:     "no workflow filter returns first run",
			workflow: "",
			response: ghWorkflowRunsResponse{
				TotalCount: 1,
				WorkflowRuns: []ghWorkflowRun{
					{
						ID:         200,
						Status:     "completed",
						Conclusion: "success",
						HeadSHA:    "abc123",
						Path:       ".github/workflows/deploy.yml",
					},
				},
			},
			wantID: 200,
		},
		{
			name:     "no matching run",
			workflow: "build.yml",
			response: ghWorkflowRunsResponse{
				TotalCount: 1,
				WorkflowRuns: []ghWorkflowRun{
					{
						ID:      300,
						HeadSHA: "abc123",
						Path:    ".github/workflows/lint.yml",
					},
				},
			},
			wantErr: "no successful workflow run found for branch feature-xyz with workflow 'build.yml'",
		},
		{
			name:     "empty runs list",
			workflow: "",
			response: ghWorkflowRunsResponse{
				TotalCount:   0,
				WorkflowRuns: []ghWorkflowRun{},
			},
			wantErr: "no successful workflow run found for branch feature-xyz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Contains(t, r.URL.Path, "/repos/testowner/testrepo/actions/runs")
				require.Equal(t, "feature-xyz", r.URL.Query().Get("branch"))
				jsonHandler(tt.response)(w, r)
			}))
			defer srv.Close()

			client := newTestClient(srv.URL, func(cfg *githubClientConfig) {
				cfg.workflow = tt.workflow
			})
			run, err := client.findWorkflowRun(context.Background(), "feature-xyz")

			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				require.Nil(t, run)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.wantID, run.ID)
			}
		})
	}
}

func TestFindArtifact(t *testing.T) {
	tests := []struct {
		name     string
		response ghArtifactsResponse
		wantErr  string
		wantID   int64
	}{
		{
			name: "finds matching artifact",
			response: ghArtifactsResponse{
				TotalCount: 2,
				Artifacts: []ghArtifact{
					{ID: 500, Name: "logs", Expired: false},
					{ID: 501, Name: "site", Expired: false, ArchiveDownloadURL: "https://example.com/dl"},
				},
			},
			wantID: 501,
		},
		{
			name: "expired artifact skipped",
			response: ghArtifactsResponse{
				TotalCount: 1,
				Artifacts: []ghArtifact{
					{ID: 600, Name: "site", Expired: true},
				},
			},
			wantErr: "no artifact named 'site' found in run 101",
		},
		{
			name: "no matching name",
			response: ghArtifactsResponse{
				TotalCount: 1,
				Artifacts: []ghArtifact{
					{ID: 700, Name: "other-artifact", Expired: false},
				},
			},
			wantErr: "no artifact named 'site' found in run 101",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Contains(t, r.URL.Path, "/repos/testowner/testrepo/actions/runs/101/artifacts")
				jsonHandler(tt.response)(w, r)
			}))
			defer srv.Close()

			client := newTestClient(srv.URL)
			artifact, err := client.findArtifact(context.Background(), 101)

			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				require.Nil(t, artifact)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.wantID, artifact.ID)
			}
		})
	}
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

	mux.HandleFunc("/repos/testowner/testrepo/actions/runs", jsonHandler(ghWorkflowRunsResponse{
		TotalCount: 1,
		WorkflowRuns: []ghWorkflowRun{
			{
				ID:         1001,
				Status:     "completed",
				Conclusion: "success",
				HeadBranch: "my-branch",
				HeadSHA:    "deadbeef",
				Path:       ".github/workflows/build.yml",
			},
		},
	}))

	mux.HandleFunc("/repos/testowner/testrepo/actions/runs/1001/artifacts", jsonHandler(ghArtifactsResponse{
		TotalCount: 1,
		Artifacts: []ghArtifact{
			{
				ID:                 2001,
				Name:               "site",
				Expired:            false,
				ArchiveDownloadURL: "https://example.com/download",
			},
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

func TestVerifyArtifactForPR(t *testing.T) {
	tests := []struct {
		name       string
		pr         int
		artifactID int64
		prInfo     ghPullRequest
		artifact   ghArtifact
		run        ghWorkflowRun
		wantErr    string
	}{
		{
			name:       "branch matches",
			pr:         10,
			artifactID: 3001,
			prInfo: ghPullRequest{
				Number: 10,
				Head: struct {
					SHA string `json:"sha"`
					Ref string `json:"ref"`
				}{SHA: "aaa111", Ref: "feature-a"},
			},
			artifact: ghArtifact{
				ID:   3001,
				Name: "site",
				WorkflowRun: &struct {
					ID         int64  `json:"id"`
					HeadBranch string `json:"head_branch"`
				}{ID: 4001, HeadBranch: "feature-a"},
			},
			run: ghWorkflowRun{
				ID:         4001,
				HeadBranch: "feature-a",
			},
		},
		{
			name:       "branch mismatch",
			pr:         11,
			artifactID: 3002,
			prInfo: ghPullRequest{
				Number: 11,
				Head: struct {
					SHA string `json:"sha"`
					Ref string `json:"ref"`
				}{SHA: "aaa111", Ref: "feature-a"},
			},
			artifact: ghArtifact{
				ID:   3002,
				Name: "site",
				WorkflowRun: &struct {
					ID         int64  `json:"id"`
					HeadBranch string `json:"head_branch"`
				}{ID: 4002, HeadBranch: "feature-b"},
			},
			run: ghWorkflowRun{
				ID:         4002,
				HeadBranch: "feature-b",
			},
			wantErr: "artifact 3002 does not belong to PR #11 (branch mismatch: artifact=feature-b, pr=feature-a)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()

			mux.HandleFunc(fmt.Sprintf("/repos/testowner/testrepo/pulls/%d", tt.pr), jsonHandler(tt.prInfo))

			mux.HandleFunc("/repos/testowner/testrepo/actions/artifacts/", func(w http.ResponseWriter, r *http.Request) {
				jsonHandler(tt.artifact)(w, r)
			})

			mux.HandleFunc("/repos/testowner/testrepo/actions/runs/", func(w http.ResponseWriter, r *http.Request) {
				jsonHandler(tt.run)(w, r)
			})

			srv := httptest.NewServer(mux)
			defer srv.Close()

			client := newTestClient(srv.URL)
			err := client.VerifyArtifactForPR(context.Background(), tt.pr, tt.artifactID)

			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
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

	// download server serves the actual zip content
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipBytes)
	}))
	defer dlSrv.Close()

	// api server returns a 302 redirect to the download server
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, "/repos/testowner/testrepo/actions/artifacts/9001/zip")
		w.Header().Set("Location", dlSrv.URL+"/download.zip")
		w.WriteHeader(http.StatusFound)
	}))
	defer apiSrv.Close()

	client := newTestClient(apiSrv.URL)
	fs, size, err := client.DownloadArtifact(context.Background(), 9001, 10*1024*1024)

	require.NoError(t, err)
	require.NotNil(t, fs)
	require.GreaterOrEqual(t, size, int64(0))

	// verify the extracted content
	f, err := fs.Open("index.html")
	require.NoError(t, err)
	defer f.Close()

	content, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, "<html>test</html>", string(content))
}
