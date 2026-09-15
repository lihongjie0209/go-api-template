package operationlog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type recorderStub struct{ entry Entry }

func (r *recorderStub) Enabled() bool { return true }

func (r *recorderStub) Record(_ context.Context, entry Entry) error { r.entry = entry; return nil }

func TestSanitizedJSONRedactsAndTruncates(t *testing.T) {
	t.Parallel()
	value, err := sanitizedJSON(map[string]any{
		"username": "alice",
		"password": "plain",
		"nested":   map[string]any{"access_token": "token", "value": strings.Repeat("x", 100)},
	}, 80)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(value, "plain") || strings.Contains(value, `:"token"`) {
		t.Fatalf("sanitized payload leaked secret: %s", value)
	}
	if len([]rune(value)) > 80 {
		t.Fatalf("payload length = %d", len([]rune(value)))
	}
}

func TestDoMeasuresResult(t *testing.T) {
	t.Parallel()
	recorder := &recorderStub{}
	want := errors.New("business failed")
	err := Do(t.Context(), recorder, Entry{Operation: "test", Protocol: "unit"}, func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("Do() error = %v", err)
	}
	if recorder.entry.Succeeded || recorder.entry.Duration < 0 || recorder.entry.ErrorMessage != want.Error() {
		t.Fatalf("recorded entry = %+v", recorder.entry)
	}
}

func TestSanitizedJSONPreservesOrdinaryFields(t *testing.T) {
	t.Parallel()
	value, err := sanitizedJSON(map[string]any{"id": "42", "action": "update"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(value, `"id":"42"`) {
		t.Fatalf("payload = %s", value)
	}
}

func TestSanitizedRawJSONRemainsValidAndBounded(t *testing.T) {
	t.Parallel()
	value, err := sanitizedRawJSON(map[string]any{"secret": "hidden", "menu": "users"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(value) || strings.Contains(string(value), "hidden") {
		t.Fatalf("value = %s", value)
	}
	if _, err := sanitizedRawJSON(map[string]any{"value": strings.Repeat("x", 100)}, 10); err == nil {
		t.Fatal("oversized JSON error = nil")
	}
}
