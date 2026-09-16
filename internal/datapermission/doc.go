// Package datapermission builds bounded, parameterized data scopes for
// repository queries. It is deliberately separate from operation
// authorization because multiple matching scopes use union-and-exclusion
// semantics instead of a boolean deny-overrides decision.
package datapermission
