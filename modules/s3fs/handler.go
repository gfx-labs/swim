package s3fs

import (
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	s3fs "github.com/fclairamb/afero-s3"
	"github.com/spf13/afero"
	"go.uber.org/zap"
)

var _ fs.FS = (*S3fs)(nil)

type S3fs struct {
	Bucket    string `json:"bucket"`
	Region    string `json:"region,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty"`
	PathStyle string `json:"path_style,omitempty"`
	Root      string `json:"root,omitempty"`

	a   afero.Fs
	log *zap.Logger
	fs.FS
}

func (s *S3fs) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID: "caddy.fs.s3",
		New: func() caddy.Module {
			return new(S3fs)
		},
	}
}

func headerOrEnv(val, envKey string) string {
	if val != "" {
		return val
	}
	return os.Getenv(envKey)
}

func (s *S3fs) Provision(ctx caddy.Context) error {
	s.log = ctx.Logger()
	s.log.Debug("initializing s3fs")

	rp := caddy.NewReplacer()
	s.Bucket = rp.ReplaceAll(s.Bucket, "")
	s.Region = rp.ReplaceAll(s.Region, "")
	s.Endpoint = rp.ReplaceAll(s.Endpoint, "")
	s.AccessKey = rp.ReplaceAll(s.AccessKey, "")
	s.SecretKey = rp.ReplaceAll(s.SecretKey, "")
	s.Root = rp.ReplaceAll(s.Root, "")

	if s.Bucket == "" {
		return fmt.Errorf("s3fs: bucket is required")
	}

	region := headerOrEnv(s.Region, "AWS_DEFAULT_REGION")
	if region == "" {
		region = "us-east-1"
	}

	accessKey := headerOrEnv(s.AccessKey, "AWS_ACCESS_KEY_ID")
	secretKey := headerOrEnv(s.SecretKey, "AWS_SECRET_ACCESS_KEY")
	endpoint := headerOrEnv(s.Endpoint, "AWS_ENDPOINT_URL")

	var cfg aws.Config
	var err error

	if accessKey == "" && secretKey == "" {
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(aws.AnonymousCredentials{}),
		)
	} else {
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		)
	}
	if err != nil {
		return fmt.Errorf("s3fs: load config: %w", err)
	}

	var s3Opts []func(*s3.Options)
	if endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
		})
	}

	switch strings.ToLower(headerOrEnv(s.PathStyle, "AWS_USE_PATH_STYLE")) {
	case "true", "t", "yes":
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.UsePathStyle = true
		})
	case "false", "f", "no":
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.UsePathStyle = false
		})
	}

	client := s3.NewFromConfig(cfg, s3Opts...)
	base := s3fs.NewFsFromClient(s.Bucket, client)

	var a afero.Fs = base
	if s.Root != "" {
		a = afero.NewBasePathFs(a, s.Root)
	}
	a = afero.NewReadOnlyFs(a)

	s.a = a
	s.FS = afero.NewIOFS(s.a)
	s.log.Debug("initialized s3fs", zap.String("bucket", s.Bucket), zap.String("root", s.Root))
	return nil
}

func (s *S3fs) Open(name string) (fs.File, error) {
	name = strings.Trim(name, "/")
	return s.FS.Open(name)
}

func (s *S3fs) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		if d.NextArg() {
			s.Bucket = d.Val()
		}
		for nesting := d.Nesting(); d.NextBlock(nesting); {
			switch strings.ToLower(d.Val()) {
			case "bucket":
				if !d.NextArg() {
					return d.ArgErr()
				}
				s.Bucket = d.Val()
			case "region":
				if !d.NextArg() {
					return d.ArgErr()
				}
				s.Region = d.Val()
			case "endpoint":
				if !d.NextArg() {
					return d.ArgErr()
				}
				s.Endpoint = d.Val()
			case "access_key":
				if !d.NextArg() {
					return d.ArgErr()
				}
				s.AccessKey = d.Val()
			case "secret_key":
				if !d.NextArg() {
					return d.ArgErr()
				}
				s.SecretKey = d.Val()
			case "path_style":
				if !d.NextArg() {
					return d.ArgErr()
				}
				s.PathStyle = d.Val()
			case "root":
				if !d.NextArg() {
					return d.ArgErr()
				}
				s.Root = d.Val()
			default:
				return d.SyntaxErr("expected bucket, region, endpoint, access_key, secret_key, path_style, or root")
			}
		}
	}
	return nil
}

func (s *S3fs) Cleanup() error {
	return nil
}
