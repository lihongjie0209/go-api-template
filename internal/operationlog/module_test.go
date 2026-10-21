package operationlog

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/stretchr/testify/require"
)

func TestConsumerErrorDoesNotLogDependencyDetails(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	options := consumerOptions(&Service{cfg: config.OperationLog{Durable: "operation-log", Subject: "platform.operation-log.recorded.v1"}}, logger)
	require.Equal(t, "operation-log", options.Durable)
	require.NotNil(t, options.OnError)
	options.OnError(errors.New("database password=secret-value"))
	require.NotContains(t, output.String(), "secret-value")
	require.Contains(t, output.String(), "event_consumer_failed")
}
