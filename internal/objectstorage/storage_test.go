package objectstorage

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/observability"
)

func TestObserveWithoutMetricsPreservesStore(t *testing.T) {
	t.Parallel()

	store := &testStore{}
	if got := Observe(store, nil, "s3"); got != store {
		t.Fatalf("Observe() = %T, want original store", got)
	}
}

func TestObservedGetMeasuresBodyLifecycle(t *testing.T) {
	t.Parallel()
	metrics := observability.NewMetrics(config.Config{Observability: config.Observability{MetricsEnabled: true}}, nil, nil)
	store := Observe(&testStore{}, metrics, "s3")
	object, err := store.Get(t.Context(), "tenant/file")
	if err != nil {
		t.Fatal(err)
	}
	if body := collectMetrics(t, metrics); strings.Contains(body, `component="object_storage"`) {
		t.Fatal("GET metric completed before response body close")
	}
	if err := object.Body.Close(); err != nil {
		t.Fatal(err)
	}
	body := collectMetrics(t, metrics)
	if !strings.Contains(body, `component="object_storage"`) || !strings.Contains(body, `operation="get"`) || !strings.Contains(body, `status="success"`) {
		t.Fatalf("object storage GET metrics missing:\n%s", body)
	}
}

func collectMetrics(t *testing.T, metrics *observability.Metrics) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 200 {
		t.Fatalf("metrics status = %d", recorder.Code)
	}
	return recorder.Body.String()
}

type testStore struct{}
type contextStore struct {
	deadline time.Time
	done     <-chan struct{}
}

func (*testStore) Put(context.Context, PutInput) (Info, error) { return Info{}, nil }
func (*testStore) Get(context.Context, string) (*Object, error) {
	return &Object{Body: io.NopCloser(nil)}, nil
}
func (*testStore) Stat(context.Context, string) (Info, error) { return Info{}, nil }
func (*testStore) Delete(context.Context, string) error       { return nil }
func (*testStore) Presign(context.Context, string, Operation, time.Duration) (SignedURL, error) {
	return SignedURL{}, nil
}

func (s *contextStore) capture(ctx context.Context) {
	s.deadline, _ = ctx.Deadline()
	s.done = ctx.Done()
}
func (s *contextStore) Put(ctx context.Context, _ PutInput) (Info, error) {
	s.capture(ctx)
	return Info{}, nil
}
func (s *contextStore) Get(ctx context.Context, _ string) (*Object, error) {
	s.capture(ctx)
	return &Object{Body: io.NopCloser(bytes.NewReader(nil))}, nil
}
func (s *contextStore) Stat(ctx context.Context, _ string) (Info, error) {
	s.capture(ctx)
	return Info{}, nil
}
func (s *contextStore) Delete(ctx context.Context, _ string) error { s.capture(ctx); return nil }
func (s *contextStore) Presign(ctx context.Context, _ string, _ Operation, _ time.Duration) (SignedURL, error) {
	s.capture(ctx)
	return SignedURL{}, nil
}

func TestValidation(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"", " ", "/absolute", "../escape", "folder/../escape", `folder\object`, "folder//object", "."} {
		if err := validateKey(key); err == nil {
			t.Fatalf("validateKey(%q) error = nil", key)
		}
	}
	if err := validateKey("tenant/document.pdf"); err != nil {
		t.Fatalf("validateKey() error = %v", err)
	}
	if _, err := effectivePresignTTL(time.Minute, 25*time.Hour); err == nil {
		t.Fatal("effectivePresignTTL() error = nil")
	}
}

func TestValidatePutInputBoundsBodyAndMetadata(t *testing.T) {
	t.Parallel()
	valid := PutInput{Key: "tenant/file.txt", Body: bytes.NewReader([]byte("x")), Size: 1, ContentType: "text/plain", Metadata: map[string]string{"sha256": "abc"}}
	if err := validatePutInput(valid); err != nil {
		t.Fatalf("validatePutInput() = %v", err)
	}
	for name, mutate := range map[string]func(*PutInput){
		"missing body":     func(input *PutInput) { input.Body = nil },
		"oversized body":   func(input *PutInput) { input.Size = maxSinglePutBytes + 1 },
		"invalid type":     func(input *PutInput) { input.ContentType = "text/plain\r\nx-evil: true" },
		"metadata newline": func(input *PutInput) { input.Metadata = map[string]string{"name": "bad\nvalue"} },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			if err := validatePutInput(input); err == nil {
				t.Fatal("validatePutInput() error = nil")
			}
		})
	}
}

func TestWithTimeoutBoundsRequestsAndKeepsGetContextUntilClose(t *testing.T) {
	t.Parallel()
	probe := &contextStore{}
	store := WithTimeout(probe, time.Second)
	started := time.Now()
	if err := store.Delete(t.Context(), "tenant/file.txt"); err != nil {
		t.Fatal(err)
	}
	if probe.deadline.Before(started.Add(900*time.Millisecond)) || probe.deadline.After(started.Add(1100*time.Millisecond)) {
		t.Fatalf("deadline = %v", probe.deadline)
	}
	object, err := store.Get(t.Context(), "tenant/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-probe.done:
		t.Fatal("Get context canceled before body close")
	default:
	}
	if err := object.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probe.done:
	default:
		t.Fatal("Get context remains active after body close")
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	store, err := New(t.Context(), config.ObjectStorage{})
	if err != nil || store != nil {
		t.Fatalf("disabled New() = %v, %v", store, err)
	}
	_, err = New(t.Context(), config.ObjectStorage{Enabled: true, Provider: "invalid", Bucket: "bucket", Region: "region", PresignTTL: time.Minute})
	if err == nil {
		t.Fatal("unsupported New() error = nil")
	}
	_, err = New(t.Context(), config.ObjectStorage{Enabled: true, Provider: "s3", Bucket: "", Region: "region", PresignTTL: time.Minute})
	if err == nil {
		t.Fatal("incomplete storage configuration error = nil")
	}
}
