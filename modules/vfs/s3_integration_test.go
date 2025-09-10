package vfs_test

import (
	"net/http"
	"testing"

	"github.com/gfx-labs/swim/modules/vfs"
	"github.com/stretchr/testify/require"
)

func TestS3Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tests := []struct {
		name    string
		url     string
		headers map[string]string
		wantErr bool
	}{
		{
			name: "DigitalOcean Spaces - Virtual Hosted Style",
			url:  "s3://sfo3.digitaloceanspaces.com/swim-integration-bucket/archive.tar.gz",
			headers: map[string]string{
				"AWS_USE_PATH_STYLE":    "false",
				"AWS_ACCESS_KEY_ID":     "DO801BHZGZZE2ZTBAHVH",
				"AWS_SECRET_ACCESS_KEY": "tN3fiz+r/4fuVXwwnYjPa0tED4yXyLLlAMV29WyXQd8",
			},
			wantErr: false,
		},
		{
			name: "DigitalOcean Spaces - Path Style with bucket in URL (no AWS_BUCKET_NAME)",
			url:  "s3://sfo3.digitaloceanspaces.com/swim-integration-bucket/archive.tar.gz",
			headers: map[string]string{
				"AWS_USE_PATH_STYLE":    "true",
				// No AWS_BUCKET_NAME - should parse from URL
				"AWS_ACCESS_KEY_ID":     "DO801BHZGZZE2ZTBAHVH",
				"AWS_SECRET_ACCESS_KEY": "tN3fiz+r/4fuVXwwnYjPa0tED4yXyLLlAMV29WyXQd8",
			},
			wantErr: false,
		},
		{
			name: "DigitalOcean Spaces - Path Style with AWS_BUCKET_NAME",
			url:  "s3://sfo3.digitaloceanspaces.com/archive.tar.gz",
			headers: map[string]string{
				"AWS_USE_PATH_STYLE":    "true",
				"AWS_BUCKET_NAME":       "swim-integration-bucket",
				"AWS_ACCESS_KEY_ID":     "DO801BHZGZZE2ZTBAHVH",
				"AWS_SECRET_ACCESS_KEY": "tN3fiz+r/4fuVXwwnYjPa0tED4yXyLLlAMV29WyXQd8",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create overlay with headers
			overlay := &vfs.Overlay{
				Root:    tt.url,
				Headers: http.Header{},
			}
			
			// Set headers from test case
			for k, v := range tt.headers {
				overlay.Headers.Set(k, v)
			}

			// Attempt to open the S3 URL
			fs, err := overlay.OpenFilesystem()
			
			if tt.wantErr {
				require.Error(t, err, "Expected error but got none")
				return
			}
			
			require.NoError(t, err, "Failed to open S3 URL")
			require.NotNil(t, fs, "Filesystem should not be nil")
			
			// Try to stat the root to verify the filesystem works
			_, err = fs.Stat("/")
			require.NoError(t, err, "Failed to stat root of filesystem")
		})
	}
}

