package objectstorage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
)

type Operation string

const (
	OperationGet Operation = "get"
	OperationPut Operation = "put"
)

type PutInput struct {
	Key         string
	Body        io.Reader
	Size        int64
	ContentType string
	Metadata    map[string]string
}

type Object struct {
	Body io.ReadCloser
	Info Info
}

type Info struct {
	Key          string
	Size         int64
	ContentType  string
	ETag         string
	LastModified time.Time
	Metadata     map[string]string
}

type SignedURL struct {
	URL       string
	Method    string
	ExpiresAt time.Time
	Headers   map[string]string
}

type Store interface {
	Put(ctx context.Context, input PutInput) (Info, error)
	Get(ctx context.Context, key string) (*Object, error)
	Stat(ctx context.Context, key string) (Info, error)
	Delete(ctx context.Context, key string) error
	Presign(ctx context.Context, key string, operation Operation, ttl time.Duration) (SignedURL, error)
}

func New(ctx context.Context, cfg config.ObjectStorage) (Store, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	switch cfg.Provider {
	case "s3":
		return newS3(ctx, cfg)
	case "oss":
		return newOSS(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported object storage provider %q", cfg.Provider)
	}
}

func validateConfig(cfg config.ObjectStorage) error {
	if strings.TrimSpace(cfg.Bucket) == "" || strings.TrimSpace(cfg.Region) == "" || cfg.PresignTTL <= 0 || cfg.PresignTTL > 24*time.Hour {
		return errors.New("object storage requires bucket, region, and a presign ttl no greater than 24h")
	}
	if (cfg.AccessKeyID == "") != (cfg.AccessKeySecret == "") {
		return errors.New("object storage access key id and secret must be configured together")
	}
	return nil
}

func validateKey(key string) error {
	if strings.TrimSpace(key) == "" || len(key) > 1024 || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") || strings.ContainsRune(key, 0) || path.Clean(key) != key || key == "." || strings.HasPrefix(key, "../") {
		return errors.New("object key must be non-empty and relative")
	}
	return nil
}

func effectivePresignTTL(configured, requested time.Duration) (time.Duration, error) {
	if requested == 0 {
		requested = configured
	}
	if requested <= 0 || requested > 24*time.Hour {
		return 0, errors.New("presign ttl must be positive and no greater than 24h")
	}
	return requested, nil
}
