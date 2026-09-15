package objectstorage

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
)

func TestObserveWithoutMetricsPreservesStore(t *testing.T) {
	t.Parallel()

	store := &testStore{}
	if got := Observe(store, nil, "s3"); got != store {
		t.Fatalf("Observe() = %T, want original store", got)
	}
}

type testStore struct{}

func (*testStore) Put(context.Context, PutInput) (Info, error) { return Info{}, nil }
func (*testStore) Get(context.Context, string) (*Object, error) {
	return &Object{Body: io.NopCloser(nil)}, nil
}
func (*testStore) Stat(context.Context, string) (Info, error) { return Info{}, nil }
func (*testStore) Delete(context.Context, string) error       { return nil }
func (*testStore) Presign(context.Context, string, Operation, time.Duration) (SignedURL, error) {
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
