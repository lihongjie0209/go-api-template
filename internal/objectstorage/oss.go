package objectstorage

import (
	"context"
	"fmt"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
	"github.com/lihongjie0209/go-api-template/internal/config"
)

type ossStore struct {
	client *oss.Client
	bucket string
	ttl    time.Duration
}

func newOSS(cfg config.ObjectStorage) Store {
	ossCfg := oss.LoadDefaultConfig().WithRegion(cfg.Region).WithUsePathStyle(cfg.UsePathStyle).WithUseCName(cfg.UseCName)
	if cfg.Endpoint != "" {
		ossCfg.WithEndpoint(cfg.Endpoint)
	}
	if cfg.AccessKeyID != "" {
		ossCfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.AccessKeySecret, cfg.SessionToken))
	}
	return &ossStore{client: oss.NewClient(ossCfg), bucket: cfg.Bucket, ttl: cfg.PresignTTL}
}

func (s *ossStore) Put(ctx context.Context, input PutInput) (Info, error) {
	if err := validatePutInput(input); err != nil {
		return Info{}, err
	}
	request := &oss.PutObjectRequest{Bucket: &s.bucket, Key: &input.Key, Body: input.Body, Metadata: input.Metadata}
	if input.Size >= 0 {
		request.ContentLength = &input.Size
	}
	if input.ContentType != "" {
		request.ContentType = &input.ContentType
	}
	result, err := s.client.PutObject(ctx, request)
	if err != nil {
		return Info{}, fmt.Errorf("put OSS object: %w", err)
	}
	return Info{Key: input.Key, Size: input.Size, ContentType: input.ContentType, ETag: oss.ToString(result.ETag), LastModified: time.Now(), Metadata: input.Metadata}, nil
}

func (s *ossStore) Get(ctx context.Context, key string) (*Object, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	result, err := s.client.GetObject(ctx, &oss.GetObjectRequest{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil, fmt.Errorf("get OSS object: %w", err)
	}
	return &Object{Body: result.Body, Info: Info{Key: key, Size: result.ContentLength, ContentType: oss.ToString(result.ContentType), ETag: oss.ToString(result.ETag), LastModified: timeValue(result.LastModified), Metadata: result.Metadata}}, nil
}

func (s *ossStore) Stat(ctx context.Context, key string) (Info, error) {
	if err := validateKey(key); err != nil {
		return Info{}, err
	}
	result, err := s.client.HeadObject(ctx, &oss.HeadObjectRequest{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return Info{}, fmt.Errorf("stat OSS object: %w", err)
	}
	return Info{Key: key, Size: result.ContentLength, ContentType: oss.ToString(result.ContentType), ETag: oss.ToString(result.ETag), LastModified: timeValue(result.LastModified), Metadata: result.Metadata}, nil
}

func (s *ossStore) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &oss.DeleteObjectRequest{Bucket: &s.bucket, Key: &key}); err != nil {
		return fmt.Errorf("delete OSS object: %w", err)
	}
	return nil
}

func (s *ossStore) Presign(ctx context.Context, key string, operation Operation, ttl time.Duration) (SignedURL, error) {
	if err := validateKey(key); err != nil {
		return SignedURL{}, err
	}
	ttl, err := effectivePresignTTL(s.ttl, ttl)
	if err != nil {
		return SignedURL{}, err
	}
	var request any
	switch operation {
	case OperationGet:
		request = &oss.GetObjectRequest{Bucket: &s.bucket, Key: &key}
	case OperationPut:
		request = &oss.PutObjectRequest{Bucket: &s.bucket, Key: &key}
	default:
		return SignedURL{}, fmt.Errorf("unsupported presign operation %q", operation)
	}
	result, err := s.client.Presign(ctx, request, func(options *oss.PresignOptions) { options.Expires = ttl })
	if err != nil {
		return SignedURL{}, fmt.Errorf("presign OSS object: %w", err)
	}
	return SignedURL{URL: result.URL, Method: result.Method, ExpiresAt: result.Expiration, Headers: result.SignedHeaders}, nil
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

var _ Store = (*ossStore)(nil)
