package objectstorage

import (
	"bytes"
	"context"
	"errors"
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

func TestTelemetryErrorDoesNotExposeProviderDetails(t *testing.T) {
	t.Parallel()
	if telemetryError(nil) != nil {
		t.Fatal("telemetryError(nil) must remain nil")
	}
	err := telemetryError(errors.New("request https://bucket.example/files/tenant-secret/report.pdf failed"))
	if err == nil || err.Error() != "object storage operation failed" {
		t.Fatalf("telemetryError() = %v", err)
	}
	canceled := telemetryError(errors.Join(context.Canceled, errors.New("secret-value")))
	if !errors.Is(canceled, context.Canceled) || strings.Contains(canceled.Error(), "secret-value") {
		t.Fatalf("telemetryError() = %v, want safe cancellation", canceled)
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
func (*testStore) Presign(context.Context, PresignInput) (SignedURL, error) {
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
func (s *contextStore) Presign(ctx context.Context, _ PresignInput) (SignedURL, error) {
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

func TestDownloadDisposition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   PresignInput
		wantErr bool
	}{
		{name: "attachment filename", input: PresignInput{Operation: OperationGet, DownloadName: "报表 2026.csv"}},
		{name: "plain presign", input: PresignInput{Operation: OperationGet}},
		{name: "put cannot specify filename", input: PresignInput{Operation: OperationPut, DownloadName: "report.csv"}, wantErr: true},
		{name: "path separators rejected", input: PresignInput{Operation: OperationGet, DownloadName: `../report.csv`}, wantErr: true},
		{name: "header injection rejected", input: PresignInput{Operation: OperationGet, DownloadName: "report\r\n.csv"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disposition, err := downloadDisposition(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("downloadDisposition() = %q, %v", disposition, err)
			}
			if tt.wantErr || tt.input.DownloadName == "" {
				return
			}
			if !strings.HasPrefix(disposition, "attachment;") || !strings.Contains(disposition, "filename") {
				t.Fatalf("downloadDisposition() = %q", disposition)
			}
		})
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
