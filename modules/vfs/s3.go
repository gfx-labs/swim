package vfs

import (
	"context"
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/afero"
)

func (o *Overlay) headerOrEnv(key string) string {
	x := o.Headers.Get(key)
	if x == "" {
		x = os.Getenv(key)
	}
	return x
}

func (o *Overlay) openS3(u *url.URL) (afero.Fs, error) {
	ctx := context.Background()
	accessKeyId := o.headerOrEnv("AWS_ACCESS_KEY_ID")
	secretAccessKey := o.headerOrEnv("AWS_SECRET_ACCESS_KEY")
	usePathStyle := o.headerOrEnv("AWS_USE_PATH_STYLE")
	bucketName := o.headerOrEnv("AWS_BUCKET_NAME")
	endpointUrl := o.headerOrEnv("AWS_ENDPOINT_URL")
	if endpointUrl == "" {
		endpointUrl = "https://" + u.Host
	}
	region := o.headerOrEnv("AWS_DEFAULT_REGION")
	if region == "" {
		region = "us-east-1"
	}

	// Create config with appropriate credentials
	var cfg aws.Config
	var err error
	
	// If no credentials provided or they are explicitly empty, use anonymous access
	if (accessKeyId == "" && secretAccessKey == "") {
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(aws.AnonymousCredentials{}),
		)
	} else {
		// Use provided credentials
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyId, secretAccessKey, "")),
		)
	}
	if err != nil {
		return nil, err
	}

	// Create S3 client with options including custom endpoint resolver
	opts := []func(*s3.Options){
		func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpointUrl)
		},
	}
	
	switch strings.ToLower(usePathStyle) {
	case "true", "t", "yes":
		opts = append(opts, func(o *s3.Options) {
			o.UsePathStyle = true
		})
	case "false", "f", "no":
		opts = append(opts, func(o *s3.Options) {
			o.UsePathStyle = false
		})
	}

	s3Client := s3.NewFromConfig(cfg, opts...)
	
	// Parse bucket and key
	var bucket, key string
	path := strings.TrimPrefix(u.Path, "/")
	
	// If bucket name is explicitly provided, use it
	if bucketName != "" {
		bucket = bucketName
		key = path
	} else {
		// Parse bucket from the first segment of the path
		parts := strings.SplitN(path, "/", 2)
		if len(parts) > 0 {
			bucket = parts[0]
			if len(parts) > 1 {
				key = parts[1]
			}
		}
	}
	
	oo, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer oo.Body.Close()
	ft := o.Type
	// TODO: perhaps we can also introspect via http content type?
	if ft == "" {
		ft = filetypeFromName(u.String())
	}
	fs, err := filesystemFromReader(ft, oo.Body)
	if err != nil {
		return nil, err
	}
	return fs, nil
}