package objectstorage

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/stretchr/testify/require"
)

func TestS3PutSupportsNonSeekableBodyOnCompatibleHTTPEndpoint(t *testing.T) {
	t.Parallel()
	const content = "streamed object"
	type receivedRequest struct {
		method      string
		path        string
		body        string
		payloadHash string
	}
	received := make(chan receivedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(writer, "read body", http.StatusBadRequest)
			return
		}
		received <- receivedRequest{method: request.Method, path: request.URL.Path, body: string(body), payloadHash: request.Header.Get("X-Amz-Content-Sha256")}
		writer.Header().Set("ETag", `"etag-1"`)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	store, err := newS3(t.Context(), config.ObjectStorage{
		Endpoint:        server.URL,
		Region:          "us-east-1",
		Bucket:          "bucket",
		AccessKeyID:     "access",
		AccessKeySecret: "secret",
		UsePathStyle:    true,
		PresignTTL:      time.Minute,
	})
	require.NoError(t, err)

	info, err := store.Put(t.Context(), PutInput{
		Key:         "path/object.txt",
		Body:        bytes.NewBufferString(content),
		Size:        int64(len(content)),
		ContentType: "text/plain",
	})
	require.NoError(t, err)
	require.Equal(t, `"etag-1"`, info.ETag)
	request := <-received
	require.Equal(t, http.MethodPut, request.method)
	require.Equal(t, "/bucket/path/object.txt", request.path)
	require.Equal(t, content, request.body)
	require.Equal(t, "UNSIGNED-PAYLOAD", request.payloadHash)
}
