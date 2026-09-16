package objectstorage

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
)

func TestOSSAdapterHTTPContract(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		key := strings.TrimPrefix(request.URL.Path, "/contract-bucket/")
		mu.Lock()
		defer mu.Unlock()
		switch request.Method {
		case http.MethodPut:
			data, err := io.ReadAll(request.Body)
			if err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			objects[key] = data
			response.Header().Set("ETag", `"contract-etag"`)
			response.WriteHeader(http.StatusOK)
		case http.MethodGet, http.MethodHead:
			data, ok := objects[key]
			if !ok {
				http.NotFound(response, request)
				return
			}
			response.Header().Set("Content-Length", strconv.Itoa(len(data)))
			response.Header().Set("Content-Type", "text/plain")
			response.Header().Set("ETag", `"contract-etag"`)
			response.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
			if request.Method == http.MethodGet {
				_, _ = response.Write(data)
			}
		case http.MethodDelete:
			delete(objects, key)
			response.WriteHeader(http.StatusNoContent)
		default:
			http.Error(response, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	store := newOSS(config.ObjectStorage{Bucket: "contract-bucket", Region: "cn-hangzhou", Endpoint: server.URL, AccessKeyID: "contract-access", AccessKeySecret: "contract-secret", UsePathStyle: true, PresignTTL: time.Minute})
	const key, content = "files/tenant-1/file-1/report.txt", "hello oss"
	info, err := store.Put(t.Context(), PutInput{Key: key, Body: bytes.NewBufferString(content), Size: int64(len(content)), ContentType: "text/plain"})
	if err != nil || info.ETag == "" {
		t.Fatalf("Put() = %+v, %v", info, err)
	}
	info, err = store.Stat(t.Context(), key)
	if err != nil || info.Size != int64(len(content)) || info.ContentType != "text/plain" {
		t.Fatalf("Stat() = %+v, %v", info, err)
	}
	object, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(object.Body)
	closeErr := object.Body.Close()
	if readErr != nil || closeErr != nil || string(data) != content {
		t.Fatalf("Get() body=%q read=%v close=%v", data, readErr, closeErr)
	}
	signed, err := store.Presign(t.Context(), key, OperationGet, time.Minute)
	if err != nil || signed.Method != http.MethodGet || !strings.Contains(signed.URL, key) {
		t.Fatalf("Presign() = %+v, %v", signed, err)
	}
	if err := store.Delete(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(t.Context(), key); err == nil {
		t.Fatal("Stat() after Delete error = nil")
	}
}
