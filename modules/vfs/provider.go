package vfs

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/afero"
)

func (o *Overlay) resolvePlaceholders() {
	for k, v := range o.Headers {
		for i, x := range v {
			v[i] = rp.ReplaceAll(x, "")
		}
		o.Headers[k] = v
	}
	o.WorkDir = rp.ReplaceAll(o.WorkDir, "")
	o.Type = rp.ReplaceAll(o.Type, "")
	o.Root = rp.ReplaceAll(o.Root, "")
}
func (o *Overlay) OpenFilesystem() (afero.Fs, error) {
	fs, err := o.openRawFilesystem()
	if err != nil {
		return nil, err
	}
	wd := o.WorkDir
	if wd == "" {
		wd = "/"
	}
	ofs := afero.NewBasePathFs(fs, wd)
	ofs = afero.NewReadOnlyFs(afero.NewCacheOnReadFs(
		ofs,
		afero.NewMemMapFs(),
		0,
	))
	return ofs, nil
}

// opens the filesystem before changing the working dir
func (o *Overlay) openRawFilesystem() (afero.Fs, error) {
	u, err := url.Parse(o.Root)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "file", "":
		return o.openFile(u)
	case "http", "https":
		return o.openHttp(u)
	case "s3":
		return o.openS3(u)
	default:
		return nil, fmt.Errorf("unrecognized scheme: %s", u.Scheme)
	}
}

func (o *Overlay) openFile(u *url.URL) (afero.Fs, error) {
	// see if its a directory
	info, err := os.Stat(u.Path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return afero.NewBasePathFs(afero.NewOsFs(), u.Path), nil
	}
	ft := o.Type
	if ft == "" {
		ft = filetypeFromName(u.String())
	}
	file, err := os.Open(u.Path)
	if err != nil {
		return nil, err
	}
	fs, err := filesystemFromReader(ft, file)
	if err != nil {
		return nil, err
	}
	return fs, nil
}

func (o *Overlay) openHttp(u *url.URL) (afero.Fs, error) {
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	for k, v := range o.Headers {
		for _, vv := range v {
			req.Header.Add(k, vv)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("unable to get network resource: %s", resp.Status)
	}
	ft := o.Type
	// TODO: perhaps we can also introspect via http content type?
	if ft == "" {
		ft = filetypeFromName(u.String())
	}
	fs, err := filesystemFromReader(ft, resp.Body)
	if err != nil {
		return nil, err
	}
	return fs, nil
}

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

	// Create config with static credentials
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyId, secretAccessKey, "")),
	)
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
	oo, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(u.Path),
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
