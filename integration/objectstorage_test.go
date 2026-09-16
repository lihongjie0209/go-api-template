//go:build integration

package integration

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/objectstorage"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestS3AdapterAgainstMinIO(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	const accessKey, secretKey, bucket = "integration-access", "integration-secret-key", "integration-bucket"
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "quay.io/minio/minio@sha256:a1ea29fa28355559ef137d71fc570e508a214ec84ff8083e39bc5428980b015e",
			ExposedPorts: []string{"9000/tcp"},
			Env:          map[string]string{"MINIO_ROOT_USER": accessKey, "MINIO_ROOT_PASSWORD": secretKey},
			Cmd:          []string{"server", "/data", "--address", ":9000"},
			WaitingFor:   wait.ForHTTP("/minio/health/ready").WithPort("9000/tcp").WithStartupTimeout(time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + host + ":" + port.Port()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")))
	if err != nil {
		t.Fatal(err)
	}
	admin := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	})
	if _, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	store, err := objectstorage.New(ctx, config.ObjectStorage{Enabled: true, Provider: "s3", Bucket: bucket, Region: "us-east-1", Endpoint: endpoint, AccessKeyID: accessKey, AccessKeySecret: secretKey, UsePathStyle: true, PresignTTL: time.Minute, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	const key, content = "files/tenant-1/file-1/report.txt", "hello object storage"
	if _, err := store.Put(ctx, objectstorage.PutInput{Key: key, Body: bytes.NewBufferString(content), Size: int64(len(content)), ContentType: "text/plain", Metadata: map[string]string{"sha256": "test"}}); err != nil {
		t.Fatal(err)
	}
	info, err := store.Stat(ctx, key)
	if err != nil || info.Size != int64(len(content)) || info.ContentType != "text/plain" {
		t.Fatalf("Stat() = %+v, %v", info, err)
	}
	object, err := store.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(object.Body)
	closeErr := object.Body.Close()
	if readErr != nil || closeErr != nil || string(data) != content {
		t.Fatalf("Get() body=%q read=%v close=%v", data, readErr, closeErr)
	}
	signed, err := store.Presign(ctx, key, objectstorage.OperationGet, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, signed.Method, signed.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("presigned GET status = %d", response.StatusCode)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(ctx, key); err == nil {
		t.Fatal("Stat() after Delete error = nil")
	}
}
