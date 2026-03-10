package vfs

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/spf13/afero"
	"google.golang.org/api/option"
)

func (o *Overlay) openGcs(u *url.URL) (afero.Fs, error) {
	ctx := context.Background()

	bucket := u.Host
	key := strings.TrimPrefix(u.Path, "/")
	if bucket == "" {
		return nil, fmt.Errorf("gcs: bucket name is required (gs://bucket/key)")
	}
	if key == "" {
		return nil, fmt.Errorf("gcs: object key is required (gs://bucket/key)")
	}

	var opts []option.ClientOption

	// credentials priority: base64-encoded JSON > file path > anonymous
	credsB64 := o.headerOrEnv("GCS_CREDENTIALS_BASE64")
	credsFile := o.headerOrEnv("GOOGLE_APPLICATION_CREDENTIALS")
	switch {
	case credsB64 != "":
		jsonBytes, err := base64.StdEncoding.DecodeString(credsB64)
		if err != nil {
			return nil, fmt.Errorf("gcs: decode GCS_CREDENTIALS_BASE64: %w", err)
		}
		opts = append(opts, option.WithCredentialsJSON(jsonBytes))
	case credsFile != "":
		opts = append(opts, option.WithCredentialsFile(credsFile))
	default:
		opts = append(opts, option.WithoutAuthentication())
	}

	// allow overriding the endpoint (useful for emulators)
	if endpoint := o.headerOrEnv("GCS_ENDPOINT_URL"); endpoint != "" {
		opts = append(opts, option.WithEndpoint(endpoint))
	}

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gcs: create client: %w", err)
	}
	defer client.Close()

	rc, err := client.Bucket(bucket).Object(key).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcs: read gs://%s/%s: %w", bucket, key, err)
	}
	defer rc.Close()

	ft := o.Type
	if ft == "" {
		ft = filetypeFromName(u.String())
	}
	fs, err := filesystemFromReader(ft, rc)
	if err != nil {
		return nil, err
	}
	return fs, nil
}
