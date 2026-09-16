// Package presentation contains response-only transformations shared by
// domain modules. Persistence and query predicates must continue to use the
// original timezone-aware values.
package presentation

import "time"

var location = time.FixedZone("Asia/Shanghai", 8*60*60)

// Time converts a persisted instant to the platform display timezone. The
// zero value remains zero so optional database values are not fabricated.
func Time(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.In(location)
}
