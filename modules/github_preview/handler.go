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
const defaultHostRe = `^pr-(.+?)\.(.+)$`

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
	Workflow     string `json:"workflow"`
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
	PruneInterval        Duration `json:"prune_interval,omitempty"`    // how often to run background pruning (default 6h)
	MaxArtifactAge       Duration `json:"max_artifact_age,omitempty"` // evict artifacts not accessed in this long (default: disabled)
	ReadCacheSize        int64    `json:"read_cache_size,omitempty"`  // per-artifact LRU read cache in bytes (default 10MB)

	// management API
	ApiPath string `json:"api_path,omitempty"`
	ApiKey  string `json:"api_key,omitempty"`

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
	refreshActive map[string]bool
	refreshWg     sync.WaitGroup

	// pruner shutdown
	pruneStop chan struct{}
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
	g.ApiKey = rp.ReplaceAll(g.ApiKey, "")
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

	if g.Workflow == "" {
		return fmt.Errorf("github_preview: workflow is required")
	}

	if g.Token == "" {
		g.log.Warn("github_preview: no token configured, API calls will be unauthenticated")
	}

	if g.ApiKey == "" {
		g.log.Warn("github_preview: no api_key configured, management API will return 403")
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
	if time.Duration(g.PruneInterval) == 0 {
		g.PruneInterval = Duration(defaultPruneInterval)
	}
	if g.ReadCacheSize == 0 {
		g.ReadCacheSize = defaultReadCacheSize
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
		owner:        g.owner,
		repo:         g.repoName,
		token:        g.Token,
		apiURL:       g.ApiURL,
		workflow:     g.Workflow,
		artifactName: g.ArtifactName,
		artifactType: g.ArtifactType,
		timeout:      defaultDownloadTimeout,
		limiter:      g.limiter,
		log:          g.log,
	})

	// initialize templates
	tmpl, err := newTemplateRenderer(g.ErrorTemplate, g.ErrorTemplateFile)
	if err != nil {
		return fmt.Errorf("github_preview: error template: %w", err)
	}
	g.templates = tmpl

	// stale-while-revalidate tracking
	g.refreshActive = make(map[string]bool)

	// start background pruner
	g.pruneStop = make(chan struct{})
	go g.runPruner()

	g.log.Debug("provisioned github_preview",
		zap.String("repo", g.Repo),
		zap.String("artifact_name", g.ArtifactName),
		zap.String("host_re", g.HostRe),
		zap.Duration("metadata_ttl", time.Duration(g.MetadataTTL)),
		zap.Int("max_artifacts", g.MaxArtifacts),
		zap.Bool("stale_while_revalidate", g.StaleWhileRevalidate),
		zap.Duration("prune_interval", time.Duration(g.PruneInterval)),
	)

	return nil
}

func (g *GithubPreview) Cleanup() error {
	// stop the background pruner
	if g.pruneStop != nil {
		close(g.pruneStop)
	}

	// wait for in-flight background refreshes to finish
	g.refreshWg.Wait()

	// unregister all our filesystems from the global map
	entries := g.metadataCache.snapshot()
	for key := range entries {
		g.unregisterFs(key)
	}
	// remove all temp files
	g.artifactCache.cleanupAll()
	return nil
}

func (g *GithubPreview) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// check if this is an API request
	if strings.HasPrefix(r.URL.Path, g.ApiPath+"/") || r.URL.Path == g.ApiPath {
		return g.handleAPI(w, r)
	}

	// extract key from host (PR number or branch name depending on mode)
	key, ok := g.extractKey(r)
	if !ok {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		g.templates.renderError(w, errorData{
			Host:  r.Host,
			Error: "no matching preview found in hostname",
		})
		return nil
	}

	// check for debug endpoint (reads from cache only, no auth required)
	if r.URL.Path == "/.well-known/deployment-debug" {
		return g.handleDebug(w, r, key)
	}

	// resolve to artifact filesystem and register it
	fsName, err := g.resolveAndRegister(r.Context(), key)
	if err != nil {
		g.log.Debug("failed to resolve artifact",
			zap.String("key", key),
			zap.Error(err),
		)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		g.templates.renderError(w, errorData{
			Host:  r.Host,
			Error: err.Error(),
		})
		return nil
	}

	// set the filesystem variable for downstream handlers (file_server, try_files, etc.)
	caddyhttp.SetVar(r.Context(), "fs", fsName)

	return next.ServeHTTP(w, r)
}

// extractKey extracts the cache key from the request Host using host_re.
// if the capture group is all digits, it's treated as a PR number ("pr:42").
// otherwise it's treated as a branch name ("branch:master").
func (g *GithubPreview) extractKey(r *http.Request) (string, bool) {
	host := r.Host
	// strip port if present
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}

	// reject hostnames exceeding DNS max length
	if len(host) > maxHostLength {
		return "", false
	}

	matches := g.hostRegexp.FindStringSubmatch(host)
	if len(matches) < 2 {
		return "", false
	}

	captured := matches[1]
	if captured == "" {
		return "", false
	}

	// auto-detect: all digits = PR number, otherwise branch name
	if _, err := strconvAtoi(captured); err == nil {
		return "pr:" + captured, true
	}
	return "branch:" + captured, true
}

// resolveAndRegister resolves a key to an artifact filesystem, registers it
// in the global FileSystems map, and returns the fs registration key.
func (g *GithubPreview) resolveAndRegister(ctx context.Context, key string) (string, error) {
	regKey := fsKeyPrefix + key

	// check metadata cache
	meta, fresh := g.metadataCache.get(key)

	if meta != nil && fresh {
		if _, ok := g.artifactCache.get(meta.artifactID); ok {
			return regKey, nil
		}
		_, err := g.downloadAndCache(ctx, key, meta.artifactID, "", meta.headSHA)
		if err != nil {
			return "", err
		}
		return regKey, nil
	}

	if meta != nil && !fresh && g.StaleWhileRevalidate {
		if _, ok := g.artifactCache.get(meta.artifactID); ok {
			g.triggerBackgroundRefresh(key)
			return regKey, nil
		}
	}

	// cache miss or stale without SWR -- full resolve through singleflight
	sfKey := "resolve:" + key
	_, err, _ := g.singleflight.Do(sfKey, func() (any, error) {
		fs, err := g.fullResolve(ctx, key)
		if err != nil {
			// forget on error so the next request retries immediately
			// instead of all concurrent waiters sharing a transient failure
			g.singleflight.Forget(sfKey)
			return nil, err
		}
		return fs, nil
	})
	if err != nil {
		return "", err
	}
	return regKey, nil
}

// fullResolve does the complete GitHub API -> download -> cache pipeline.
// auto-detects PR vs branch based on the key prefix.
func (g *GithubPreview) fullResolve(ctx context.Context, key string) (afero.Fs, error) {
	var digest string
	var artifactID int64
	var headSHA string

	if strings.HasPrefix(key, "branch:") {
		branchName := strings.TrimPrefix(key, "branch:")

		run, artifact, err := g.client.resolveArtifact(ctx, branchName)
		if err != nil {
			return nil, err
		}
		artifactID = artifact.ID
		digest = artifact.Digest
		headSHA = run.HeadSHA
	} else {
		prStr := strings.TrimPrefix(key, "pr:")
		prNum, err := strconvAtoi(prStr)
		if err != nil {
			return nil, fmt.Errorf("invalid PR number: %s", prStr)
		}

		res, err := g.client.ResolvePR(ctx, prNum)
		if err != nil {
			return nil, err
		}

		// on-demand closed PR detection
		if res.PR.State != "open" {
			g.evictKey(key)
			return nil, fmt.Errorf("PR #%d is %s", prNum, res.PR.State)
		}

		artifactID = res.ArtifactID
		digest = res.Artifact.Digest
		headSHA = res.WorkflowRun.HeadSHA
	}

	// check if we already have this artifact cached
	if fs, ok := g.artifactCache.get(artifactID); ok {
		g.metadataCache.set(key, artifactID, headSHA)
		g.registerFs(key, fs)
		return fs, nil
	}

	return g.downloadAndCache(ctx, key, artifactID, digest, headSHA)
}

// downloadAndCache downloads an artifact and puts it in both caches,
// then registers the filesystem in the global map
func (g *GithubPreview) downloadAndCache(ctx context.Context, key string, artifactID int64, expectedDigest string, headSHA string) (afero.Fs, error) {
	rawFs, size, cleanup, err := g.client.DownloadArtifact(ctx, artifactID, g.MaxArtifactSize, expectedDigest)
	if err != nil {
		return nil, err
	}

	wd := g.WorkDir
	if wd == "" {
		wd = "/"
	}
	layered := afero.NewBasePathFs(rawFs, wd)
	layered = newLruCacheFs(layered, g.ReadCacheSize)

	g.artifactCache.set(artifactID, layered, size, cleanup)
	g.metadataCache.set(key, artifactID, headSHA)
	g.registerFs(key, layered)

	return layered, nil
}

// registerFs registers an afero.Fs in Caddy's global FileSystems map
func (g *GithubPreview) registerFs(key string, afs afero.Fs) {
	if g.fileSystems != nil {
		g.fileSystems.Register(fsKeyPrefix+key, afero.NewIOFS(afs))
	}
}

// unregisterFs removes a filesystem from Caddy's global FileSystems map
func (g *GithubPreview) unregisterFs(key string) {
	if g.fileSystems != nil {
		g.fileSystems.Unregister(fsKeyPrefix + key)
	}
}

// triggerBackgroundRefresh starts an async re-resolve if one isn't already running
func (g *GithubPreview) triggerBackgroundRefresh(key string) {
	g.refreshMu.Lock()
	if g.refreshActive[key] {
		g.refreshMu.Unlock()
		return
	}
	g.refreshActive[key] = true
	g.refreshWg.Add(1)
	g.refreshMu.Unlock()

	go func() {
		defer g.refreshWg.Done()
		defer func() {
			g.refreshMu.Lock()
			delete(g.refreshActive, key)
			g.refreshMu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), defaultDownloadTimeout)
		defer cancel()

		_, err := g.fullResolve(ctx, key)
		if err != nil {
			g.log.Debug("background refresh failed",
				zap.String("key", key),
				zap.Error(err),
			)
		} else {
			g.log.Debug("background refresh completed",
				zap.String("key", key),
			)
		}
	}()
}

// runPruner periodically prunes closed PRs and (optionally) stale artifacts
func (g *GithubPreview) runPruner() {
	ticker := time.NewTicker(time.Duration(g.PruneInterval))
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			g.pruneOnce()
		case <-g.pruneStop:
			return
		}
	}
}

// pruneOnce runs one pruning cycle: evicts closed/merged PRs and stale artifacts
func (g *GithubPreview) pruneOnce() {
	g.log.Debug("starting prune cycle")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	entries := g.metadataCache.snapshot()

	// prune closed/merged PRs (skip branch entries)
	for key, meta := range entries {
		if !strings.HasPrefix(key, "pr:") {
			continue
		}
		prStr := strings.TrimPrefix(key, "pr:")
		prNum, err := strconvAtoi(prStr)
		if err != nil {
			continue
		}
		state, err := g.client.GetPRState(ctx, prNum)
		if err != nil {
			g.log.Debug("prune: failed to check PR state",
				zap.String("key", key),
				zap.Error(err),
			)
			continue
		}
		if state != "open" {
			g.log.Debug("prune: evicting closed PR",
				zap.String("key", key),
				zap.String("state", state),
			)
			g.artifactCache.evict(meta.artifactID)
			g.metadataCache.evict(key)
			g.unregisterFs(key)
		}
	}

	// prune by age (if configured)
	maxAge := time.Duration(g.MaxArtifactAge)
	if maxAge > 0 {
		stale := g.artifactCache.staleEntries(maxAge)
		for _, artifactID := range stale {
			g.log.Debug("prune: evicting stale artifact",
				zap.Int64("artifact_id", artifactID),
			)
			g.artifactCache.evict(artifactID)
			// also clean up metadata entries pointing to this artifact
			for key, meta := range entries {
				if meta.artifactID == artifactID {
					g.metadataCache.evict(key)
					g.unregisterFs(key)
				}
			}
		}
	}

	g.log.Debug("prune cycle complete")
}

// evictKey is used for on-demand eviction
func (g *GithubPreview) evictKey(key string) {
	meta, _ := g.metadataCache.get(key)
	if meta != nil {
		g.artifactCache.evict(meta.artifactID)
	}
	g.metadataCache.evict(key)
	g.unregisterFs(key)
}

// interface assertion
var _ caddyhttp.MiddlewareHandler = (*GithubPreview)(nil)
