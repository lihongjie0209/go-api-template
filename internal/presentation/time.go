// Package presentation contains response-only transformations shared by
// domain modules. Persistence and query predicates must continue to use the
// original timezone-aware values.
package presentation

import (
	"context"
	"time"
)

// ActorResolver resolves audit principal IDs through the owning identity
// service. Callers should batch all IDs for one response to avoid N+1 RPCs.
type ActorResolver interface {
	ResolveUserIDs(context.Context, []string) (map[string]string, error)
}

var location = time.FixedZone("Asia/Shanghai", 8*60*60)

// Time converts a persisted instant to the platform display timezone. The
// zero value remains zero so optional database values are not fabricated.
func Time(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.In(location)
}
