package presentation

import (
	"context"
	"fmt"
	"testing"
)

type actorResolverStub struct{ ids []string }

func (s *actorResolverStub) ResolveUserIDs(_ context.Context, ids []string) (map[string]string, error) {
	s.ids = append([]string(nil), ids...)
	return map[string]string{"user-1": "Alice"}, nil
}

func TestActorNamesUsesResolverOnceAndRetainsStableFallback(t *testing.T) {
	t.Parallel()
	resolver := &actorResolverStub{}
	names, err := ActorNames(t.Context(), resolver, "user-1", "system-1", "user-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if names["user-1"] != "Alice" || names["system-1"] != "system-1" || len(names) != 2 || len(resolver.ids) != 2 {
		t.Fatalf("names=%#v ids=%#v", names, resolver.ids)
	}
}

func TestActorNamesSupportsMissingResolverAndRejectsUnboundedInput(t *testing.T) {
	t.Parallel()
	names, err := ActorNames(t.Context(), nil, "system-1")
	if err != nil || names["system-1"] != "system-1" {
		t.Fatalf("names=%#v err=%v", names, err)
	}
	ids := make([]string, maxActorIDs+1)
	for index := range ids {
		ids[index] = fmt.Sprintf("actor-%d", index)
	}
	if _, err := ActorNames(t.Context(), nil, ids...); err == nil {
		t.Fatal("ActorNames() error = nil")
	}
}
