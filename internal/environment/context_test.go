package environment

import "testing"

func TestContextRoundTrip(t *testing.T) {
	if profile, ok := FromContext(t.Context()); ok || profile != "" {
		t.Fatalf("empty context = %q, %v", profile, ok)
	}
	ctx := WithContext(t.Context(), "test")
	if profile, ok := FromContext(ctx); !ok || profile != "test" {
		t.Fatalf("FromContext() = %q, %v", profile, ok)
	}
}
