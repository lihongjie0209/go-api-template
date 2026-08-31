package grpcclient

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestMetadataStreamInterceptorPropagatesAuthenticationAndCorrelation(t *testing.T) {
	t.Parallel()

	ctx := WithRequestID(context.Background(), "request-1")
	ctx = WithIdempotencyKey(ctx, "operation-1")
	interceptor := metadataStreamInterceptor("", "test-psk")
	var captured metadata.MD

	_, err := interceptor(ctx, &grpc.StreamDesc{}, nil, "/example.v1.Service/Stream", func(streamCtx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
		captured, _ = metadata.FromOutgoingContext(streamCtx)
		return nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"PSK test-psk"}, captured.Get("authorization"))
	require.Equal(t, []string{"request-1"}, captured.Get("x-request-id"))
	require.Equal(t, []string{"operation-1"}, captured.Get("idempotency-key"))
}
