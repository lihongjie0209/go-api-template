package httptransport

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/dictionary"
	"github.com/stretchr/testify/require"
)

func TestDictionaryResultTooLargeUsesGlobalConflictCode(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/public/dictionaries/query", nil)
	handler := NewDictionaryHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	handler.fail(ctx, dictionary.ErrResultTooLarge)

	require.Equal(t, http.StatusConflict, recorder.Code)
	var response Response
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, apperror.CodeConflict, response.Code)
	require.Contains(t, response.Message, "reduce page size")
}
