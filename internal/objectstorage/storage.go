package objectstorage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
)

const (
	maxSinglePutBytes = int64(5 << 30)
	maxMetadataItems  = 32
	maxMetadataBytes  = 8 << 10
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
	if strings.TrimSpace(cfg.Bucket) == "" || len(cfg.Bucket) > 255 || strings.TrimSpace(cfg.Region) == "" || len(cfg.Region) > 255 || cfg.PresignTTL <= 0 || cfg.PresignTTL > 24*time.Hour || cfg.Timeout < time.Second || cfg.Timeout > 5*time.Minute || cfg.UseCName && cfg.Provider != "oss" {
		return errors.New("object storage requires bounded bucket/region, a presign ttl no greater than 24h, and timeout from 1s through 5m")
	}
	if (cfg.AccessKeyID == "") != (cfg.AccessKeySecret == "") {
		return errors.New("object storage access key id and secret must be configured together")
	}
	if len(cfg.AccessKeyID) > 512 || len(cfg.AccessKeySecret) > 4096 || len(cfg.SessionToken) > 8192 {
		return errors.New("object storage credential fields exceed their bounds")
	}
	if cfg.Endpoint != "" {
		endpoint, err := url.Parse(cfg.Endpoint)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return errors.New("object storage endpoint must be an absolute HTTP(S) URL without credentials, query, or fragment")
		}
	}
	return nil
}

func validateKey(key string) error {
	if strings.TrimSpace(key) == "" || len(key) > 1024 || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") || strings.ContainsRune(key, 0) || path.Clean(key) != key || key == "." || strings.HasPrefix(key, "../") {
		return errors.New("object key must be non-empty and relative")
	}
	return nil
}

func validatePutInput(input PutInput) error {
	if err := validateKey(input.Key); err != nil {
		return err
	}
	if input.Body == nil || input.Size < 0 || input.Size > maxSinglePutBytes {
		return fmt.Errorf("object body and size from zero through %d bytes are required", maxSinglePutBytes)
	}
	if len(input.ContentType) > 255 || strings.ContainsAny(input.ContentType, "\r\n\x00") {
		return errors.New("object content type is invalid")
	}
	if input.ContentType != "" {
		if _, _, err := mime.ParseMediaType(input.ContentType); err != nil {
			return errors.New("object content type is invalid")
		}
	}
	if len(input.Metadata) > maxMetadataItems {
		return fmt.Errorf("object metadata exceeds %d items", maxMetadataItems)
	}
	total := 0
	for key, value := range input.Metadata {
		key = strings.TrimSpace(key)
		if key == "" || len(key) > 128 || len(value) > 2048 || strings.ContainsAny(key, "\r\n\x00:") || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("object metadata is invalid")
		}
		total += len(key) + len(value)
	}
	if total > maxMetadataBytes {
		return fmt.Errorf("object metadata exceeds %d bytes", maxMetadataBytes)
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
