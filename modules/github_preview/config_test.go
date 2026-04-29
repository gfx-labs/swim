package github_preview

import (
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/stretchr/testify/require"
)

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{
			name:  "megabytes",
			input: "100MB",
			want:  104857600,
		},
		{
			name:  "gigabytes",
			input: "1GB",
			want:  1073741824,
		},
		{
			name:  "kilobytes",
			input: "512KB",
			want:  524288,
		},
		{
			name:  "bytes with suffix",
			input: "1024B",
			want:  1024,
		},
		{
			name:  "bare number",
			input: "100",
			want:  100,
		},
		{
			name:  "lowercase suffix",
			input: "10mb",
			want:  10 * 1024 * 1024,
		},
		{
			name:  "whitespace padding",
			input: "  50KB  ",
			want:  50 * 1024,
		},
		{
			name:    "invalid characters",
			input:   "notanumber",
			wantErr: true,
		},
		{
			name:    "negative-like input",
			input:   "-10MB",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseByteSize(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestStrconvAtoi(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{
			name:  "zero",
			input: "0",
			want:  0,
		},
		{
			name:  "positive integer",
			input: "42",
			want:  42,
		},
		{
			name:  "large number",
			input: "123456789",
			want:  123456789,
		},
		{
			name:  "single digit",
			input: "7",
			want:  7,
		},
		{
			name:    "non-numeric string",
			input:   "abc",
			wantErr: true,
		},
		{
			name:    "mixed digits and letters",
			input:   "12abc",
			wantErr: true,
		},
		{
			name:    "negative number",
			input:   "-5",
			wantErr: true,
		},
		{
			name:    "decimal number",
			input:   "3.14",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := strconvAtoi(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestUnmarshalCaddyfile(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		check   func(t *testing.T, g *GithubPreview)
		wantErr bool
	}{
		{
			name: "minimal config",
			input: `github_preview {
				repo "owner/repo"
				token "test-token"
			}`,
			check: func(t *testing.T, g *GithubPreview) {
				require.Equal(t, "owner/repo", g.Repo)
				require.Equal(t, "test-token", g.Token)
			},
		},
		{
			name: "full config",
			input: `github_preview {
				repo "owner/repo"
				token "ghp_xxxx"
				workflow "build.yml"
				artifact_name "build-output"
				artifact_type ".tar.gz"
				workdir "/public"
				host_re "^(?P<pr>\d+)\.preview\.example\.com$"
				metadata_ttl 5m
				max_artifacts 100
				max_artifact_size 500MB
				api_path "/_api"
				api_key "secret123"
				api_url "https://github.example.com/api/v3"
				stale_while_revalidate true
				error_template "<h1>Error</h1>"
				error_template_file "/etc/error.html"
			}`,
			check: func(t *testing.T, g *GithubPreview) {
				require.Equal(t, "owner/repo", g.Repo)
				require.Equal(t, "ghp_xxxx", g.Token)
				require.Equal(t, "build.yml", g.Workflow)
				require.Equal(t, "build-output", g.ArtifactName)
				require.Equal(t, ".tar.gz", g.ArtifactType)
				require.Equal(t, "/public", g.WorkDir)
				require.Equal(t, "^(?P<pr>\\d+)\\.preview\\.example\\.com$", g.HostRe)
				require.Equal(t, Duration(5*time.Minute), g.MetadataTTL)
				require.Equal(t, 100, g.MaxArtifacts)
				require.Equal(t, int64(500*1024*1024), g.MaxArtifactSize)
				require.Equal(t, "/_api", g.ApiPath)
				require.Equal(t, "secret123", g.ApiKey)
				require.Equal(t, "https://github.example.com/api/v3", g.ApiURL)
				require.True(t, g.StaleWhileRevalidate)
				require.Equal(t, "<h1>Error</h1>", g.ErrorTemplate)
				require.Equal(t, "/etc/error.html", g.ErrorTemplateFile)
			},
		},
		{
			name: "invalid option returns error",
			input: `github_preview {
				repo "owner/repo"
				bogus_option "value"
			}`,
			wantErr: true,
		},
		{
			name: "missing repo arg returns error",
			input: `github_preview {
				repo
			}`,
			wantErr: true,
		},
		{
			name: "missing token arg returns error",
			input: `github_preview {
				token
			}`,
			wantErr: true,
		},
		{
			name: "invalid metadata_ttl returns error",
			input: `github_preview {
				metadata_ttl notaduration
			}`,
			wantErr: true,
		},
		{
			name: "invalid max_artifact_size returns error",
			input: `github_preview {
				max_artifact_size xyz
			}`,
			wantErr: true,
		},
		{
			name: "invalid stale_while_revalidate is treated as false",
			input: `github_preview {
				stale_while_revalidate "notbool"
			}`,
			check: func(t *testing.T, g *GithubPreview) {
				require.False(t, g.StaleWhileRevalidate)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := caddyfile.NewTestDispenser(tt.input)
			var g GithubPreview
			err := g.UnmarshalCaddyfile(d)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.check != nil {
				tt.check(t, &g)
			}
		})
	}
}
