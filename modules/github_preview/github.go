package github_preview

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gfx-labs/swim/pkg/archive"
	"github.com/spf13/afero"
)

// GithubClient handles all GitHub API interactions
type GithubClient struct {
	owner        string
	repo         string
	token        string
	apiURL       string
	workflow     string
	artifactName string
	artifactType string

	apiClient      *http.Client
	downloadClient *http.Client
	limiter        *RateLimiter
}

type githubClientConfig struct {
	owner           string
	repo            string
	token           string
	apiURL          string
	workflow        string
	artifactName    string
	artifactType    string
	apiTimeout      time.Duration
	downloadTimeout time.Duration
	limiter         *RateLimiter
}

func newGithubClient(cfg githubClientConfig) *GithubClient {
	return &GithubClient{
		owner:        cfg.owner,
		repo:         cfg.repo,
		token:        cfg.token,
		apiURL:       cfg.apiURL,
		workflow:     cfg.workflow,
		artifactName: cfg.artifactName,
		artifactType: cfg.artifactType,
		apiClient: &http.Client{
			Timeout: cfg.apiTimeout,
			// don't follow redirects automatically for artifact downloads
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		downloadClient: &http.Client{
			Timeout: cfg.downloadTimeout,
		},
		limiter: cfg.limiter,
	}
}

// github API response types

type ghPullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Head   struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	HTMLURL string `json:"html_url"`
}

type ghWorkflowRun struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
	Path       string `json:"path"`
	CreatedAt  string `json:"created_at"`
	HTMLURL    string `json:"html_url"`
}

type ghWorkflowRunsResponse struct {
	TotalCount   int             `json:"total_count"`
	WorkflowRuns []ghWorkflowRun `json:"workflow_runs"`
}

type ghArtifact struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	SizeInBytes        int64  `json:"size_in_bytes"`
	Expired            bool   `json:"expired"`
	ArchiveDownloadURL string `json:"archive_download_url"`
	WorkflowRun        *struct {
		ID         int64  `json:"id"`
		HeadBranch string `json:"head_branch"`
	} `json:"workflow_run"`
}

type ghArtifactsResponse struct {
	TotalCount int          `json:"total_count"`
	Artifacts  []ghArtifact `json:"artifacts"`
}

// ResolutionResult contains everything found during PR resolution
type ResolutionResult struct {
	PR          *ghPullRequest
	WorkflowRun *ghWorkflowRun
	Artifact    *ghArtifact
	ArtifactID  int64
}

// ResolvePR resolves a PR number to an artifact ID through the GitHub API chain:
// PR number -> PR (branch) -> latest successful workflow run for branch -> artifact
//
// queries by branch name (not head SHA) so that previous successful builds
// are found even if the current head commit's build failed or is in progress.
func (c *GithubClient) ResolvePR(ctx context.Context, pr int) (*ResolutionResult, error) {
	// get PR by number
	prInfo, err := c.getPR(ctx, pr)
	if err != nil {
		return nil, fmt.Errorf("get PR #%d: %w", pr, err)
	}

	branch := prInfo.Head.Ref

	// find latest successful workflow run for this branch
	run, err := c.findWorkflowRun(ctx, branch)
	if err != nil {
		return nil, fmt.Errorf("find workflow run for branch %s: %w", branch, err)
	}

	// find artifact in the run
	artifact, err := c.findArtifact(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("find artifact in run %d: %w", run.ID, err)
	}

	return &ResolutionResult{
		PR:          prInfo,
		WorkflowRun: run,
		Artifact:    artifact,
		ArtifactID:  artifact.ID,
	}, nil
}

// DownloadArtifact downloads and extracts an artifact into an afero filesystem
func (c *GithubClient) DownloadArtifact(ctx context.Context, artifactID int64, maxSize int64) (afero.Fs, int64, error) {
	if err := c.limiter.wait(ctx); err != nil {
		return nil, 0, fmt.Errorf("rate limited: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/%s/actions/artifacts/%d/zip",
		c.apiURL, c.owner, c.repo, artifactID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	c.setAuth(req)

	// first request gets a 302 redirect to a presigned URL
	resp, err := c.apiClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, 0, fmt.Errorf("artifact %d not found (may have expired)", artifactID)
	}

	downloadURL := ""
	if resp.StatusCode == http.StatusFound {
		downloadURL = resp.Header.Get("Location")
	} else if resp.StatusCode == http.StatusOK {
		// some API versions return the content directly
		downloadURL = ""
	} else {
		return nil, 0, fmt.Errorf("unexpected status %d fetching artifact %d", resp.StatusCode, artifactID)
	}

	var body io.ReadCloser
	var contentLength int64

	if downloadURL != "" {
		// follow the redirect with the download client (no auth needed for presigned URL)
		dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
		if err != nil {
			return nil, 0, err
		}
		dlResp, err := c.downloadClient.Do(dlReq)
		if err != nil {
			return nil, 0, err
		}
		if dlResp.StatusCode != http.StatusOK {
			dlResp.Body.Close()
			return nil, 0, fmt.Errorf("download failed with status %d", dlResp.StatusCode)
		}
		body = dlResp.Body
		contentLength = dlResp.ContentLength
	} else {
		body = resp.Body
		contentLength = resp.ContentLength
	}
	defer body.Close()

	// check content-length against limit if available
	if contentLength > 0 && contentLength > maxSize {
		return nil, 0, fmt.Errorf("artifact %d size %d exceeds max %d", artifactID, contentLength, maxSize)
	}

	// wrap reader with size limit enforcement
	limited := io.LimitReader(body, maxSize+1)

	fs, err := archive.FilesystemFromReader(c.artifactType, limited)
	if err != nil {
		return nil, 0, fmt.Errorf("extract artifact %d: %w", artifactID, err)
	}

	// use content-length if available for size tracking
	size := contentLength
	if size <= 0 {
		size = 0
	}

	return fs, size, nil
}

// VerifyArtifactForPR checks that an artifact ID belongs to a workflow run
// whose branch matches the given PR's head branch
func (c *GithubClient) VerifyArtifactForPR(ctx context.Context, pr int, artifactID int64) error {
	if err := c.limiter.wait(ctx); err != nil {
		return fmt.Errorf("rate limited: %w", err)
	}

	// get the PR to find its head branch
	prInfo, err := c.getPR(ctx, pr)
	if err != nil {
		return fmt.Errorf("get PR #%d: %w", pr, err)
	}

	// get artifact metadata
	artifactURL := fmt.Sprintf("%s/repos/%s/%s/actions/artifacts/%d",
		c.apiURL, c.owner, c.repo, artifactID)
	var artifact ghArtifact
	if err := c.doJSON(ctx, artifactURL, &artifact); err != nil {
		return fmt.Errorf("get artifact %d: %w", artifactID, err)
	}

	if artifact.WorkflowRun == nil {
		return fmt.Errorf("artifact %d has no associated workflow run", artifactID)
	}

	// get the workflow run to check head branch
	runURL := fmt.Sprintf("%s/repos/%s/%s/actions/runs/%d",
		c.apiURL, c.owner, c.repo, artifact.WorkflowRun.ID)
	var run ghWorkflowRun
	if err := c.doJSON(ctx, runURL, &run); err != nil {
		return fmt.Errorf("get workflow run %d: %w", artifact.WorkflowRun.ID, err)
	}

	if run.HeadBranch != prInfo.Head.Ref {
		return fmt.Errorf("artifact %d does not belong to PR #%d (branch mismatch: artifact=%s, pr=%s)",
			artifactID, pr, run.HeadBranch, prInfo.Head.Ref)
	}

	return nil
}

// GetPRInfo fetches PR info for the debug endpoint.
// uses tryAcquire so it fails fast if rate limited.
func (c *GithubClient) GetPRInfo(ctx context.Context, pr int) (*ghPullRequest, error) {
	if !c.limiter.tryAcquire() {
		return nil, fmt.Errorf("rate limited")
	}
	return c.getPR(ctx, pr)
}

// GetLatestRunInfo fetches the latest successful workflow run for a branch.
// uses tryAcquire so it fails fast if rate limited.
func (c *GithubClient) GetLatestRunInfo(ctx context.Context, branch string) (*ghWorkflowRun, error) {
	if !c.limiter.tryAcquire() {
		return nil, fmt.Errorf("rate limited")
	}
	return c.findWorkflowRun(ctx, branch)
}

// GetArtifactInfo fetches artifact info for a run.
// uses tryAcquire so it fails fast if rate limited.
func (c *GithubClient) GetArtifactInfo(ctx context.Context, runID int64) (*ghArtifact, error) {
	if !c.limiter.tryAcquire() {
		return nil, fmt.Errorf("rate limited")
	}
	return c.findArtifact(ctx, runID)
}

// getPR fetches a single PR by number: GET /repos/{owner}/{repo}/pulls/{number}
func (c *GithubClient) getPR(ctx context.Context, pr int) (*ghPullRequest, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d",
		c.apiURL, c.owner, c.repo, pr)

	var prInfo ghPullRequest
	if err := c.doJSON(ctx, url, &prInfo); err != nil {
		return nil, err
	}
	return &prInfo, nil
}

func (c *GithubClient) findWorkflowRun(ctx context.Context, branch string) (*ghWorkflowRun, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/actions/runs?branch=%s&status=success&per_page=20",
		c.apiURL, c.owner, c.repo, branch)

	var runs ghWorkflowRunsResponse
	if err := c.doJSON(ctx, url, &runs); err != nil {
		return nil, err
	}

	for i := range runs.WorkflowRuns {
		run := &runs.WorkflowRuns[i]
		if c.workflow != "" {
			// filter by workflow file name (path ends with the workflow name)
			if !strings.HasSuffix(run.Path, "/"+c.workflow) && run.Path != c.workflow {
				continue
			}
		}
		return run, nil
	}

	if c.workflow != "" {
		return nil, fmt.Errorf("no successful workflow run found for branch %s with workflow '%s'", branch, c.workflow)
	}
	return nil, fmt.Errorf("no successful workflow run found for branch %s", branch)
}

func (c *GithubClient) findArtifact(ctx context.Context, runID int64) (*ghArtifact, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/actions/runs/%d/artifacts",
		c.apiURL, c.owner, c.repo, runID)

	var artifacts ghArtifactsResponse
	if err := c.doJSON(ctx, url, &artifacts); err != nil {
		return nil, err
	}

	for i := range artifacts.Artifacts {
		a := &artifacts.Artifacts[i]
		if a.Name == c.artifactName && !a.Expired {
			return a, nil
		}
	}

	return nil, fmt.Errorf("no artifact named '%s' found in run %d", c.artifactName, runID)
}

func (c *GithubClient) doJSON(ctx context.Context, url string, v any) error {
	if err := c.limiter.wait(ctx); err != nil {
		return fmt.Errorf("rate limited: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.downloadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("not found: %s", url)
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("GitHub API rate limited (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API error: %s (status %d)", url, resp.StatusCode)
	}

	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *GithubClient) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}
