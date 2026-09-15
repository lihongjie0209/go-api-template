package objectstorage

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/lihongjie0209/go-api-template/internal/config"
)

type s3Store struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
	ttl     time.Duration
}

func newS3(ctx context.Context, cfg config.ObjectStorage) (Store, error) {
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.AccessKeyID != "" {
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.AccessKeySecret, cfg.SessionToken)))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("load S3 configuration: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.UsePathStyle = cfg.UsePathStyle
		if cfg.Endpoint != "" {
			options.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})
	return &s3Store{client: client, presign: s3.NewPresignClient(client), bucket: cfg.Bucket, ttl: cfg.PresignTTL}, nil
}

func (s *s3Store) Put(ctx context.Context, input PutInput) (Info, error) {
	if err := validateKey(input.Key); err != nil {
		return Info{}, err
	}
	request := &s3.PutObjectInput{Bucket: &s.bucket, Key: &input.Key, Body: input.Body, Metadata: input.Metadata}
	if input.Size >= 0 {
		request.ContentLength = aws.Int64(input.Size)
	}
	if input.ContentType != "" {
		request.ContentType = aws.String(input.ContentType)
	}
	result, err := s.client.PutObject(ctx, request)
	if err != nil {
		return Info{}, fmt.Errorf("put S3 object %q: %w", input.Key, err)
	}
	return Info{Key: input.Key, Size: input.Size, ContentType: input.ContentType, ETag: aws.ToString(result.ETag), LastModified: time.Now(), Metadata: input.Metadata}, nil
}

func (s *s3Store) Get(ctx context.Context, key string) (*Object, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil, fmt.Errorf("get S3 object %q: %w", key, err)
	}
	return &Object{Body: result.Body, Info: Info{Key: key, Size: aws.ToInt64(result.ContentLength), ContentType: aws.ToString(result.ContentType), ETag: aws.ToString(result.ETag), LastModified: aws.ToTime(result.LastModified), Metadata: result.Metadata}}, nil
}

func (s *s3Store) Stat(ctx context.Context, key string) (Info, error) {
	if err := validateKey(key); err != nil {
		return Info{}, err
	}
	result, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return Info{}, fmt.Errorf("stat S3 object %q: %w", key, err)
	}
	return Info{Key: key, Size: aws.ToInt64(result.ContentLength), ContentType: aws.ToString(result.ContentType), ETag: aws.ToString(result.ETag), LastModified: aws.ToTime(result.LastModified), Metadata: result.Metadata}, nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key}); err != nil {
		return fmt.Errorf("delete S3 object %q: %w", key, err)
	}
	return nil
}

func (s *s3Store) Presign(ctx context.Context, key string, operation Operation, ttl time.Duration) (SignedURL, error) {
	if err := validateKey(key); err != nil {
		return SignedURL{}, err
	}
	ttl, err := effectivePresignTTL(s.ttl, ttl)
	if err != nil {
		return SignedURL{}, err
	}
	options := func(options *s3.PresignOptions) { options.Expires = ttl }
	var url, method string
	switch operation {
	case OperationGet:
		result, signErr := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key}, options)
		if signErr != nil {
			err = signErr
		} else {
			url, method = result.URL, result.Method
		}
	case OperationPut:
		result, signErr := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &key}, options)
		if signErr != nil {
			err = signErr
		} else {
			url, method = result.URL, result.Method
		}
	default:
		return SignedURL{}, fmt.Errorf("unsupported presign operation %q", operation)
	}
	if err != nil {
		return SignedURL{}, fmt.Errorf("presign S3 object %q: %w", key, err)
	}
	return SignedURL{URL: url, Method: method, ExpiresAt: time.Now().Add(ttl)}, nil
}

var _ Store = (*s3Store)(nil)
