// Package dependency contains checks used when wiring interface dependencies.
package dependency

import "reflect"

// IsNil reports both nil interfaces and interfaces holding nil-capable values.
// It is intended for constructor and registry validation before a method call.
func IsNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
