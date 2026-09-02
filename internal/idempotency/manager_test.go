package idempotency

import (
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/config"
)

func TestManagerStorageKeyIsServiceScoped(t *testing.T) {
	t.Parallel()
	manager := New(nil, config.Config{App: config.App{Name: "billing-service"}})
	if got := manager.storageKey("operation-1"); got != "idempotency:billing-service:operation-1" {
		t.Fatalf("storage key = %q", got)
	}
}
