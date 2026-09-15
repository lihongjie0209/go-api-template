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

func TestIdentifierHashNormalizesCaseAndWhitespace(t *testing.T) {
	t.Parallel()
	service := &Service{cfg: config.SecurityLog{HashKey: strings.Repeat("a", 32)}}
	if service.hashIdentifier(" Alice@Example.COM ") != service.hashIdentifier("alice@example.com") {
		t.Fatal("identifier hashes differ after normalization")
	}
	if service.hash(" Refresh-Token ") == service.hash("refresh-token") {
		t.Fatal("opaque token hashing unexpectedly normalized token")
	}
}

func TestSafeMetadataRejectsCredentials(t *testing.T) {
	t.Parallel()
	for _, metadata := range []map[string]any{{"access_token": "secret"}, {"nested": map[string]any{"client_secret": "secret"}}, {"api-key": "secret"}} {
		if _, err := safeMetadata(metadata, 1024); err == nil {
			t.Fatalf("credential metadata error = nil for %#v", metadata)
		}
	}
	if value, err := safeMetadata(map[string]any{"device": "mobile"}, 1024); err != nil || string(value) != `{"device":"mobile"}` {
		t.Fatalf("metadata = %s, %v", value, err)
	}
}
