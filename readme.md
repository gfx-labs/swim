# swim

the original etymology: (s)tatic (w)ebsite (i)mage (m)aker

swim is a package containing multiple caddy modules which are used to host static websites.

while it originally was its own program, it has now been packaged into caddy modules.


# modules


## vfs


```
github.com/gfx-labs/swim/plugin/vfs
```

the vfs plugin allows using archives, either on local fs, s3, or accessed via https, as filesystems.

```
{
	admin off
	filesystem sitezip vfs "https://cdn.gfx.xyz/archive.tar.gz" /
}

:8000 {
	fs sitezip
	file_server browse {
	}
}
```

it depends on https://github.com/caddyserver/caddy/pull/5833

## localfs

```
github.com/gfx-labs/swim/plugin/localfs
```

this is a simple local fs

## mergefs

```
github.com/gfx-labs/swim/plugin/mergefs
```

mergefs is a union filesystem that merges multiple caddy filesystem modules into a single read-only fs. layers are ordered by priority, with the first layer winning on conflicts. directory listings are combined across all layers.

```
{
	admin off
	filesystem merged merge {
		layer local {
			root /srv/overrides
		}
		layer vfs {
			root s3://bucket/site.tar.gz
		}
	}
}

:8000 {
	fs merged
	file_server browse {
	}
}
```

## prerender



```
github.com/gfx-labs/swim/plugin/prerender
```


this does prerender middleware, so it has the list of user agents, and do like prerender-style forwarding of the request to the remote

## github_preview

```
github.com/gfx-labs/swim/plugin/github_preview
```

github_preview is a middleware that serves GitHub Actions build artifacts for pull request preview deploys. it extracts a PR number from the request hostname, resolves the latest successful build artifact via the GitHub API, and registers the artifact as a caddy filesystem. downstream handlers (`file_server`, `try_files`, etc.) serve from it natively.

a single swim instance serves all PR previews with no config changes or restarts when PRs are opened or closed. artifacts are cached in memory with configurable TTL and LRU eviction.

**note:** github_preview sets the `{http.vars.fs}` request variable to point at the resolved artifact filesystem. this overrides any `fs` directive in the same site block. if the request hostname doesn't match a PR (regex doesn't match), a 404 error page is returned -- it does not fall back to local disk.

### token permissions

the github token requires the following permissions:

- **fine-grained PAT or GitHub App**: Actions (read), Pull requests (read)
- **classic PAT**: `repo` scope

### example

```
*.preview.oku.trade {
    route /config.js {
        respond `window.Config = { api: "https://api.staging.oku.trade" }`
    }
    github_preview {
        repo "oku-trade/trade"
        token {env.GITHUB_TOKEN}
        workflow "ci.yml"
        artifact_name "build-artifacts"
        workdir "dist"
        refresh_token {env.PREVIEW_REFRESH_TOKEN}
    }
    try_files {path} /index.html
    @no_cache path */sw.js */index.html
    header @no_cache Cache-Control "no-cache, must-revalidate, max-age=0"
    header Cache-Control "max-age=3600"
    file_server
}
```

### configuration

| option | default | description |
|---|---|---|
| `repo` | (required) | github repo in `owner/repo` format |
| `token` | (required) | github API token |
| `workflow` | (none) | filter workflow runs by file name (e.g. `build.yml`) |
| `artifact_name` | `dist` | artifact name from `actions/upload-artifact` |
| `artifact_type` | `.zip` | archive format (`.zip`, `.tar.gz`, `.tar`) |
| `workdir` | `/` | subdirectory within the archive to serve from |
| `host_re` | `^pr-(\d+)\.(.+)$` | regex to extract PR number from hostname (capture group 1) |
| `api_url` | `https://api.github.com` | github API base URL (for GitHub Enterprise) |
| `metadata_ttl` | `120s` | how long to cache PR -> artifact ID mappings |
| `max_artifacts` | `50` | max cached artifact filesystems (LRU eviction) |
| `max_artifact_size` | `100MB` | max artifact download size |
| `stale_while_revalidate` | `false` | serve stale cache while refreshing in background |
| `api_path` | `/_preview` | path prefix for the management API |
| `refresh_token` | (none) | bearer token for the management API |
| `error_template` | (built-in) | inline HTML template for error pages |
| `error_template_file` | (none) | path to a custom error template file |

### management API

all endpoints require `Authorization: Bearer <refresh_token>`.

**POST `/_preview/refresh`** -- resolve and cache an artifact for a PR. optionally provide an `artifact_id` hint from CI.

```json
{"pr": 42, "artifact_id": 12345}
```

**DELETE `/_preview/refresh`** -- evict a PR from the cache.

```json
{"pr": 42}
```

**GET `/_preview/status`** -- list all cached PRs and cache stats.

### debug endpoint

every preview gets a debug page at `/.well-known/deployment-debug` (no auth required). it shows cache state and live GitHub API data for the PR derived from the hostname.

### CI integration

in the GitHub Actions workflow, after uploading the build artifact:

```yaml
- name: Notify preview server
  run: |
    ARTIFACT_ID=$(gh api repos/${{ github.repository }}/actions/runs/${{ github.run_id }}/artifacts \
      --jq '.artifacts[] | select(.name=="dist") | .id')
    curl -X POST https://preview.oku.trade/_preview/refresh \
      -H "Authorization: Bearer ${{ secrets.PREVIEW_REFRESH_TOKEN }}" \
      -H "Content-Type: application/json" \
      -d "{\"pr\": ${{ github.event.pull_request.number }}, \"artifact_id\": ${ARTIFACT_ID}}"
```

on PR close:

```yaml
- name: Evict preview cache
  run: |
    curl -X DELETE https://preview.oku.trade/_preview/refresh \
      -H "Authorization: Bearer ${{ secrets.PREVIEW_REFRESH_TOKEN }}" \
      -H "Content-Type: application/json" \
      -d "{\"pr\": ${{ github.event.pull_request.number }}}"
```
