package securitylog

import (
	"strings"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/config"
)

func TestHashIsKeyedAndStable(t *testing.T) {
	t.Parallel()
	first := (&Service{cfg: config.SecurityLog{HashKey: strings.Repeat("a", 32)}}).hash("alice@example.com")
	second := (&Service{cfg: config.SecurityLog{HashKey: strings.Repeat("b", 32)}}).hash("alice@example.com")
	if first == "" || first == second || strings.Contains(first, "alice") {
		t.Fatalf("hashes = %q %q", first, second)
	}
}

func TestSafeMetadataRejectsCredentials(t *testing.T) {
	t.Parallel()
	if _, err := safeMetadata(map[string]any{"access_token": "secret"}, 1024); err == nil {
		t.Fatal("credential metadata error = nil")
	}
	if value, err := safeMetadata(map[string]any{"device": "mobile"}, 1024); err != nil || string(value) != `{"device":"mobile"}` {
		t.Fatalf("metadata = %s, %v", value, err)
	}
}
