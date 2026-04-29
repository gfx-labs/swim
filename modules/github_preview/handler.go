package github_preview

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/spf13/afero"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// Duration wraps time.Duration for JSON marshaling
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(dur)
	return nil
}

// max DNS hostname length
const maxHostLength = 253

// default host regex: pr-{number}.{any domain}
const defaultHostRe = `^pr-(\d+)\.(.+)$`

// fsKeyPrefix is used to namespace our entries in the global FileSystems map
const fsKeyPrefix = "github_preview:"

// GithubPreview is a Caddy middleware handler that resolves GitHub Actions
// build artifacts based on the PR number extracted from the request hostname.
// It registers the resolved artifact filesystem in Caddy's global FileSystems
// map and sets the "fs" request variable so downstream handlers (file_server,
// try_files, etc.) can serve files from it natively.
type GithubPreview struct {
	// github resolution
	Repo string `json:"repo"`

	// Token is a GitHub API token (PAT or GitHub App token).
	//
	// Required permissions (fine-grained PAT or GitHub App):
	//   - Actions: Read-only
	//   - Pull requests: Read-only
	//
	// For classic PATs, the "repo" scope is required (no finer granularity).
	//
	// For public repos, requests work without a token but are limited to
	// 60 requests/hour. Authenticated requests get 5,000 requests/hour.
	Token string `json:"token"`
	Workflow     string `json:"workflow,omitempty"`
	ArtifactName string `json:"artifact_name,omitempty"`
	ArtifactType string `json:"artifact_type,omitempty"`
	WorkDir      string `json:"workdir,omitempty"`
	ApiURL       string `json:"api_url,omitempty"`

	// host matching
	HostRe string `json:"host_re,omitempty"`

	// cache settings
	MetadataTTL          Duration `json:"metadata_ttl,omitempty"`
	MaxArtifacts         int      `json:"max_artifacts,omitempty"`
	MaxArtifactSize      int64    `json:"max_artifact_size,omitempty"`
	StaleWhileRevalidate bool     `json:"stale_while_revalidate,omitempty"`

	// refresh API
	ApiPath      string `json:"api_path,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`

	// error templates
	ErrorTemplate     string `json:"error_template,omitempty"`
	ErrorTemplateFile string `json:"error_template_file,omitempty"`

	// runtime (unexported)
	owner         string
	repoName      string
	hostRegexp    *regexp.Regexp
	metadataCache *MetadataCache
	artifactCache *ArtifactCache
	client        *GithubClient
	limiter       *RateLimiter
	singleflight  singleflight.Group
	templates     *templateRenderer
	fileSystems   caddy.FileSystems
	log           *zap.Logger

	// stale-while-revalidate background refresh tracking
	refreshMu     sync.Mutex
	refreshActive map[int]bool
}

func (g *GithubPreview) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID: "http.handlers.github_preview",
		New: func() caddy.Module {
			return new(GithubPreview)
		},
	}
}

func (g *GithubPreview) Provision(ctx caddy.Context) error {
	g.log = ctx.Logger()

	// resolve placeholders
	rp := caddy.NewReplacer()
	g.Repo = rp.ReplaceAll(g.Repo, "")
	g.Token = rp.ReplaceAll(g.Token, "")
	g.RefreshToken = rp.ReplaceAll(g.RefreshToken, "")
	g.ApiURL = rp.ReplaceAll(g.ApiURL, "")
	g.ErrorTemplateFile = rp.ReplaceAll(g.ErrorTemplateFile, "")

	// validate required fields
	if g.Repo == "" {
		return fmt.Errorf("github_preview: repo is required")
	}
	parts := strings.SplitN(g.Repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("github_preview: repo must be in 'owner/repo' format")
	}
	g.owner = parts[0]
	g.repoName = parts[1]

	if g.Token == "" {
		g.log.Warn("github_preview: no token configured, API calls will be unauthenticated")
	}

	if g.RefreshToken == "" {
		g.log.Warn("github_preview: no refresh_token configured, refresh API will return 403")
	}

	// set defaults
	if g.ArtifactName == "" {
		g.ArtifactName = defaultArtifactName
	}
	if g.ArtifactType == "" {
		g.ArtifactType = defaultArtifactType
	}
	if g.WorkDir == "" {
		g.WorkDir = defaultWorkDir
	}
	if g.ApiURL == "" {
		g.ApiURL = defaultApiURL
	}
	if g.HostRe == "" {
		g.HostRe = defaultHostRe
	}
	if time.Duration(g.MetadataTTL) == 0 {
		g.MetadataTTL = Duration(defaultMetadataTTL)
	}
	if g.MaxArtifacts == 0 {
		g.MaxArtifacts = defaultMaxArtifacts
	}
	if g.MaxArtifactSize == 0 {
		g.MaxArtifactSize = defaultMaxArtifactSize
	}
	if g.ApiPath == "" {
		g.ApiPath = defaultApiPath
	}

	// compile host regex
	re, err := regexp.Compile(g.HostRe)
	if err != nil {
		return fmt.Errorf("github_preview: invalid host_re: %w", err)
	}
	g.hostRegexp = re

	// initialize rate limiter
	g.limiter = newRateLimiter(defaultRateLimit, defaultRateBurst)

	// grab global filesystems map
	g.fileSystems = ctx.FileSystems()

	// initialize caches
	g.metadataCache = newMetadataCache(time.Duration(g.MetadataTTL))
	g.artifactCache = newArtifactCache(g.MaxArtifacts)

	// initialize github client
	g.client = newGithubClient(githubClientConfig{
		owner:           g.owner,
		repo:            g.repoName,
		token:           g.Token,
		apiURL:          g.ApiURL,
		workflow:        g.Workflow,
		artifactName:    g.ArtifactName,
		artifactType:    g.ArtifactType,
		apiTimeout:      defaultApiTimeout,
		downloadTimeout: defaultDownloadTimeout,
		limiter:         g.limiter,
	})

	// initialize templates
	tmpl, err := newTemplateRenderer(g.ErrorTemplate, g.ErrorTemplateFile)
	if err != nil {
		return fmt.Errorf("github_preview: error template: %w", err)
	}
	g.templates = tmpl

	// stale-while-revalidate tracking
	g.refreshActive = make(map[int]bool)

	g.log.Debug("provisioned github_preview",
		zap.String("repo", g.Repo),
		zap.String("workflow", g.Workflow),
		zap.String("artifact_name", g.ArtifactName),
		zap.String("host_re", g.HostRe),
		zap.Duration("metadata_ttl", time.Duration(g.MetadataTTL)),
		zap.Int("max_artifacts", g.MaxArtifacts),
		zap.Bool("stale_while_revalidate", g.StaleWhileRevalidate),
	)

	return nil
}

func (g *GithubPreview) Cleanup() error {
	// unregister all our filesystems from the global map
	entries := g.metadataCache.snapshot()
	for pr := range entries {
		g.fileSystems.Unregister(fsKey(pr))
	}
	return nil
}

func (g *GithubPreview) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// check if this is an API request
	if strings.HasPrefix(r.URL.Path, g.ApiPath+"/") || r.URL.Path == g.ApiPath {
		return g.handleAPI(w, r)
	}

	// extract PR number from host
	pr, ok := g.extractPR(r)
	if !ok {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		g.templates.renderError(w, errorData{
			Host:  r.Host,
			Error: "no matching pull request found in hostname",
		})
		return nil
	}

	// check for debug endpoint (requires refresh token)
	if r.URL.Path == "/.well-known/deployment-debug" {
		if !g.authenticateRefresh(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "unauthorized",
			})
			return nil
		}
		return g.handleDebug(w, r, pr)
	}

	// resolve PR to artifact filesystem and register it
	key, err := g.resolveAndRegister(r.Context(), pr)
	if err != nil {
		g.log.Debug("failed to resolve artifact",
			zap.Int("pr", pr),
			zap.Error(err),
		)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		g.templates.renderError(w, errorData{
			PR:    pr,
			Error: err.Error(),
		})
		return nil
	}

	// set the filesystem variable for downstream handlers (file_server, try_files, etc.)
	caddyhttp.SetVar(r.Context(), "fs", key)

	return next.ServeHTTP(w, r)
}

// extractPR extracts the PR number from the request Host using host_re.
// the first capture group of host_re must be the PR number (digits).
func (g *GithubPreview) extractPR(r *http.Request) (int, bool) {
	host := r.Host
	// strip port if present
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}

	// reject hostnames exceeding DNS max length
	if len(host) > maxHostLength {
		return 0, false
	}

	matches := g.hostRegexp.FindStringSubmatch(host)
	if len(matches) < 2 {
		return 0, false
	}

	n, err := strconvAtoi(matches[1])
	if err != nil {
		return 0, false
	}

	return n, true
}

// resolveAndRegister resolves a PR to an artifact filesystem, registers it
// in the global FileSystems map, and returns the key.
func (g *GithubPreview) resolveAndRegister(ctx context.Context, pr int) (string, error) {
	key := fsKey(pr)

	// check metadata cache
	meta, fresh := g.metadataCache.get(pr)

	if meta != nil && fresh {
		// cache hit + fresh -- check that the artifact is still in cache and registered
		if _, ok := g.artifactCache.get(meta.artifactID); ok {
			return key, nil
		}
		// artifact evicted from LRU, re-download
		_, err := g.downloadAndCache(ctx, pr, meta.artifactID)
		if err != nil {
			return "", err
		}
		return key, nil
	}

	if meta != nil && !fresh && g.StaleWhileRevalidate {
		// cache hit + stale + stale-while-revalidate enabled
		if _, ok := g.artifactCache.get(meta.artifactID); ok {
			g.triggerBackgroundRefresh(pr)
			return key, nil
		}
	}

	// cache miss or stale without SWR -- full resolve through singleflight
	sfKey := fmt.Sprintf("resolve:%d", pr)
	_, err, _ := g.singleflight.Do(sfKey, func() (any, error) {
		_, err := g.fullResolve(ctx, pr)
		return nil, err
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

// fullResolve does the complete GitHub API -> download -> cache pipeline
func (g *GithubPreview) fullResolve(ctx context.Context, pr int) (afero.Fs, error) {
	res, err := g.client.ResolvePR(ctx, pr)
	if err != nil {
		return nil, err
	}

	// check if we already have this artifact cached
	if fs, ok := g.artifactCache.get(res.ArtifactID); ok {
		g.metadataCache.set(pr, res.ArtifactID, "")
		// ensure registered
		g.registerFs(pr, fs)
		return fs, nil
	}

	return g.downloadAndCache(ctx, pr, res.ArtifactID)
}

// downloadAndCache downloads an artifact and puts it in both caches,
// then registers the filesystem in the global map
func (g *GithubPreview) downloadAndCache(ctx context.Context, pr int, artifactID int64) (afero.Fs, error) {
	rawFs, size, err := g.client.DownloadArtifact(ctx, artifactID, g.MaxArtifactSize)
	if err != nil {
		return nil, err
	}

	// apply workdir + caching layers (same pattern as VFS)
	wd := g.WorkDir
	if wd == "" {
		wd = "/"
	}
	layered := afero.NewBasePathFs(rawFs, wd)
	layered = afero.NewReadOnlyFs(afero.NewCacheOnReadFs(layered, afero.NewMemMapFs(), 0))

	g.artifactCache.set(artifactID, layered, size)
	g.metadataCache.set(pr, artifactID, "")
	g.registerFs(pr, layered)

	return layered, nil
}

// registerFs registers an afero.Fs in Caddy's global FileSystems map
func (g *GithubPreview) registerFs(pr int, afs afero.Fs) {
	if g.fileSystems != nil {
		g.fileSystems.Register(fsKey(pr), afero.NewIOFS(afs))
	}
}

// unregisterFs removes a filesystem from Caddy's global FileSystems map
func (g *GithubPreview) unregisterFs(pr int) {
	if g.fileSystems != nil {
		g.fileSystems.Unregister(fsKey(pr))
	}
}

// fsKey returns the global FileSystems map key for a PR
func fsKey(pr int) string {
	return fmt.Sprintf("%s%d", fsKeyPrefix, pr)
}

// triggerBackgroundRefresh starts an async re-resolve for a PR if one isn't already running
func (g *GithubPreview) triggerBackgroundRefresh(pr int) {
	g.refreshMu.Lock()
	if g.refreshActive[pr] {
		g.refreshMu.Unlock()
		return
	}
	g.refreshActive[pr] = true
	g.refreshMu.Unlock()

	go func() {
		defer func() {
			g.refreshMu.Lock()
			delete(g.refreshActive, pr)
			g.refreshMu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), defaultDownloadTimeout)
		defer cancel()

		_, err := g.fullResolve(ctx, pr)
		if err != nil {
			g.log.Debug("background refresh failed",
				zap.Int("pr", pr),
				zap.Error(err),
			)
		} else {
			g.log.Debug("background refresh completed",
				zap.Int("pr", pr),
			)
		}
	}()
}

// interface assertion
var _ caddyhttp.MiddlewareHandler = (*GithubPreview)(nil)
