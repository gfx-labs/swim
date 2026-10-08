package github_preview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gfx-labs/swim/pkg/archive"
	"github.com/spf13/afero"
	"go.uber.org/zap"
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

	client  *http.Client
	limiter *RateLimiter
	log     *zap.Logger
}

type githubClientConfig struct {
	owner        string
	repo         string
	token        string
	apiURL       string
	workflow     string
	artifactName string
	artifactType string
	timeout      time.Duration
	limiter      *RateLimiter
	log          *zap.Logger
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
		client: &http.Client{
			Timeout: cfg.timeout,
			// strip auth header on redirect so the bearer token doesn't
			// leak to the presigned URL host when downloading artifacts
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				req.Header.Del("Authorization")
				return nil
			},
		},
		limiter: cfg.limiter,
		log:     cfg.log,
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


type ghArtifact struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	SizeInBytes        int64  `json:"size_in_bytes"`
	Expired            bool   `json:"expired"`
	Digest             string `json:"digest"`
	ArchiveDownloadURL string `json:"archive_download_url"`
	WorkflowRun *struct {
		ID         int64  `json:"id"`
		HeadBranch string `json:"head_branch"`
		HeadSHA    string `json:"head_sha"`
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
	// TargetSHA is the current head commit of the PR/branch, if known
	TargetSHA string
}

// ExactHead reports whether the resolved run was built from the current head
// commit (found via the reliable head_sha lookup).
func (r *ResolutionResult) ExactHead() bool {
	return r.TargetSHA != "" && r.WorkflowRun != nil && r.WorkflowRun.HeadSHA == r.TargetSHA
}

// ResolvePR resolves a PR number to an artifact ID through the GitHub API chain:
// PR number -> PR (branch) -> workflow runs for branch -> first run with our artifact
//
// queries by branch name (not head SHA) so that previous builds are found
// even if the current head commit's build failed or is in progress.
func (c *GithubClient) ResolvePR(ctx context.Context, pr int) (*ResolutionResult, error) {
	prInfo, err := c.getPR(ctx, pr)
	if err != nil {
		return nil, fmt.Errorf("get PR #%d: %w", pr, err)
	}

	run, artifact, err := c.resolveArtifactAt(ctx, prInfo.Head.Ref, prInfo.Head.SHA)
	if err != nil {
		return nil, err
	}

	return &ResolutionResult{
		PR:          prInfo,
		WorkflowRun: run,
		Artifact:    artifact,
		ArtifactID:  artifact.ID,
		TargetSHA:   prInfo.Head.SHA,
	}, nil
}

// ResolveBranch resolves a branch name to its most recent artifact.
func (c *GithubClient) ResolveBranch(ctx context.Context, branch string) (*ResolutionResult, error) {
	headSHA, err := c.getBranchHeadSHA(ctx, branch)
	if err != nil {
		// non-fatal: the branch listing can still find a build
		c.log.Debug("could not resolve branch head", zap.String("branch", branch), zap.Error(err))
	}
	run, artifact, err := c.resolveArtifactAt(ctx, branch, headSHA)
	if err != nil {
		return nil, err
	}
	return &ResolutionResult{
		WorkflowRun: run,
		Artifact:    artifact,
		ArtifactID:  artifact.ID,
		TargetSHA:   headSHA,
	}, nil
}

// resolveArtifact finds the most recent artifact with the configured name
// for a branch. see ResolveBranch.
func (c *GithubClient) resolveArtifact(ctx context.Context, branch string) (*ghWorkflowRun, *ghArtifact, error) {
	res, err := c.ResolveBranch(ctx, branch)
	if err != nil {
		return nil, nil, err
	}
	return res.WorkflowRun, res.Artifact, nil
}

// resolveArtifactAt finds the most recent artifact for a branch whose head is
// headSHA (may be empty).
//
// the branch-filtered workflow runs listing intermittently returns stale
// result sets (sometimes weeks old) on busy repos, see
// https://github.com/orgs/community/discussions/206725. head_sha scoped
// queries are a point lookup and have been reported consistent, so try that
// first. the branch listing is only used as a fallback (e.g. the head commit's
// build failed or hasn't uploaded its artifact yet), and its results are
// sorted client-side rather than trusting server ordering.
// works as soon as the build step uploads the artifact, even while the
// workflow run is still in progress.
func (c *GithubClient) resolveArtifactAt(ctx context.Context, branch, headSHA string) (*ghWorkflowRun, *ghArtifact, error) {
	c.log.Debug("searching workflow runs for branch",
		zap.String("branch", branch),
		zap.String("head_sha", headSHA),
		zap.String("artifact_name", c.artifactName),
	)

	if headSHA != "" {
		runs, err := c.listRuns(ctx, "head_sha="+url.QueryEscape(headSHA))
		if err != nil {
			c.log.Debug("head_sha run lookup failed", zap.String("head_sha", headSHA), zap.Error(err))
		} else if run, a := c.findArtifact(ctx, runs); a != nil {
			return run, a, nil
		}
	}

	runs, err := c.listRuns(ctx, "branch="+url.QueryEscape(branch))
	if err != nil {
		return nil, nil, fmt.Errorf("list runs for branch %s: %w", branch, err)
	}
	if run, a := c.findArtifact(ctx, runs); a != nil {
		return run, a, nil
	}

	return nil, nil, fmt.Errorf("no artifact '%s' found for branch %s", c.artifactName, branch)
}

// listRuns lists runs of the configured workflow with the given filter query,
// sorted newest first.
func (c *GithubClient) listRuns(ctx context.Context, filter string) ([]ghWorkflowRun, error) {
	// use the workflow-specific runs endpoint to filter server-side
	runsURL := fmt.Sprintf("%s/repos/%s/%s/actions/workflows/%s/runs?%s&per_page=30",
		c.apiURL, c.owner, c.repo, url.PathEscape(c.workflow), filter)

	var runsResp struct {
		WorkflowRuns []ghWorkflowRun `json:"workflow_runs"`
	}
	if err := c.doJSON(ctx, runsURL, &runsResp); err != nil {
		return nil, err
	}
	runs := runsResp.WorkflowRuns
	// RFC 3339 UTC timestamps sort lexically; ties broken by run ID
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].CreatedAt != runs[j].CreatedAt {
			return runs[i].CreatedAt > runs[j].CreatedAt
		}
		return runs[i].ID > runs[j].ID
	})
	return runs, nil
}

// findArtifact returns the first run (in order) that has a non-expired
// artifact with the configured name.
func (c *GithubClient) findArtifact(ctx context.Context, runs []ghWorkflowRun) (*ghWorkflowRun, *ghArtifact) {
	for i := range runs {
		run := &runs[i]

		artifactsURL := fmt.Sprintf("%s/repos/%s/%s/actions/runs/%d/artifacts",
			c.apiURL, c.owner, c.repo, run.ID)

		var artifactsResp ghArtifactsResponse
		if err := c.doJSON(ctx, artifactsURL, &artifactsResp); err != nil {
			continue
		}

		for j := range artifactsResp.Artifacts {
			a := &artifactsResp.Artifacts[j]
			if a.Name == c.artifactName && !a.Expired {
				c.log.Debug("found artifact",
					zap.Int64("artifact_id", a.ID),
					zap.Int64("run_id", run.ID),
					zap.String("branch", run.HeadBranch),
					zap.String("head_sha", run.HeadSHA),
					zap.Int64("size_bytes", a.SizeInBytes),
				)
				return run, a
			}
		}
	}
	return nil, nil
}

// getBranchHeadSHA returns the commit SHA at the tip of a branch.
func (c *GithubClient) getBranchHeadSHA(ctx context.Context, branch string) (string, error) {
	// escape each segment but keep '/' so names like feature/x resolve
	segs := strings.Split(branch, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	branchURL := fmt.Sprintf("%s/repos/%s/%s/branches/%s",
		c.apiURL, c.owner, c.repo, strings.Join(segs, "/"))
	var resp struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := c.doJSON(ctx, branchURL, &resp); err != nil {
		return "", err
	}
	return resp.Commit.SHA, nil
}

// DownloadArtifact downloads an artifact zip to a temp file on disk and returns
// an afero.Fs backed by the on-disk zip (random access, no full extraction into memory).
// the cleanup function removes the temp file and must be called when the filesystem
// is no longer needed. if expectedDigest is non-empty, the downloaded content is
// verified against it (format: "sha256:<hex>").
func (c *GithubClient) DownloadArtifact(ctx context.Context, artifactID int64, maxSize int64, expectedDigest string) (afero.Fs, int64, func(), error) {
	if err := c.limiter.wait(ctx); err != nil {
		return nil, 0, nil, fmt.Errorf("rate limited: %w", err)
	}

	dlURL := fmt.Sprintf("%s/repos/%s/%s/actions/artifacts/%d/zip",
		c.apiURL, c.owner, c.repo, artifactID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return nil, 0, nil, err
	}
	c.setAuth(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, 0, nil, fmt.Errorf("artifact %d not found (may have expired)", artifactID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, nil, fmt.Errorf("unexpected status %d fetching artifact %d", resp.StatusCode, artifactID)
	}

	// check content-length against limit if available
	if resp.ContentLength > 0 && resp.ContentLength > maxSize {
		return nil, 0, nil, fmt.Errorf("artifact %d size %d exceeds max %d", artifactID, resp.ContentLength, maxSize)
	}

	// build a namespaced path: {tmpdir}/swim-github-preview/{host}/{owner}/{repo}/{artifact_name}/{artifact_id}.zip
	apiHost := "github.com"
	if parsedURL, parseErr := url.Parse(c.apiURL); parseErr == nil && parsedURL.Host != "" {
		apiHost = parsedURL.Host
	}
	zipPath := filepath.Join(
		os.TempDir(), "swim-github-preview",
		apiHost, c.owner, c.repo, c.artifactName,
		fmt.Sprintf("%d.zip", artifactID),
	)

	fs, size, digest, cleanup, err := archive.DownloadZipFs(resp.Body, maxSize, zipPath)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("extract artifact %d: %w", artifactID, err)
	}

	if expectedDigest != "" && digest != expectedDigest {
		cleanup()
		return nil, 0, nil, fmt.Errorf("artifact %d digest mismatch: expected %s, got %s", artifactID, expectedDigest, digest)
	}

	c.log.Debug("downloaded artifact",
		zap.Int64("artifact_id", artifactID),
		zap.Int64("size_bytes", size),
		zap.String("digest", digest),
	)

	return fs, size, cleanup, nil
}

// GetPRState fetches the state of a PR ("open", "closed").
func (c *GithubClient) GetPRState(ctx context.Context, pr int) (string, error) {
	prInfo, err := c.getPR(ctx, pr)
	if err != nil {
		return "", err
	}
	return prInfo.State, nil
}

// getPR fetches a single PR by number: GET /repos/{owner}/{repo}/pulls/{number}
func (c *GithubClient) getPR(ctx context.Context, pr int) (*ghPullRequest, error) {
	c.log.Debug("fetching pull request", zap.Int("pr", pr))
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d",
		c.apiURL, c.owner, c.repo, pr)

	var prInfo ghPullRequest
	if err := c.doJSON(ctx, url, &prInfo); err != nil {
		return nil, err
	}
	c.log.Debug("found pull request",
		zap.Int("pr", pr),
		zap.String("branch", prInfo.Head.Ref),
		zap.String("sha", prInfo.Head.SHA),
		zap.String("state", prInfo.State),
	)
	return &prInfo, nil
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

	resp, err := c.client.Do(req)
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
