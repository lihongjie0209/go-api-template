package datapermission

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/lihongjie0209/go-api-template/internal/accesscontrol"
)

var ErrDuplicateSchema = errors.New("data permission: duplicate resource schema")

// ValueType is the policy-visible logical type of a registered field.
type ValueType string

const ValueTypeText ValueType = "text"

// Field maps one logical Resource attribute to a code-owned SQL identifier.
type Field struct {
	Column string
	Type   ValueType
}

// Schema is an immutable allowlist of queryable fields for one Resource.
type Schema struct {
	resource string
	fields   map[string]Field
}

var (
	resourceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)*$`)
	fieldNamePattern    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	columnNamePattern   = regexp.MustCompile(`^[a-z_][a-z0-9_]*(?:\.[a-z_][a-z0-9_]*)?$`)
)

// NewSchema validates and defensively copies a Resource query schema.
func NewSchema(resource string, fields map[string]Field) (*Schema, error) {
	if !resourceNamePattern.MatchString(resource) || len(fields) == 0 {
		return nil, fmt.Errorf("%w: invalid resource or empty fields", ErrInvalidSchema)
	}
	copyFields := make(map[string]Field, len(fields))
	for name, field := range fields {
		if !fieldNamePattern.MatchString(name) || !columnNamePattern.MatchString(field.Column) || field.Type != ValueTypeText {
			return nil, fmt.Errorf("%w: invalid field %q", ErrInvalidSchema, name)
		}
		if field.Column != strings.ToLower(field.Column) {
			return nil, fmt.Errorf("%w: column for %q must be lowercase", ErrInvalidSchema, name)
		}
		copyFields[name] = field
	}
	return &Schema{resource: resource, fields: copyFields}, nil
}

func (s *Schema) field(name string) (Field, bool) {
	if s == nil {
		return Field{}, false
	}
	field, ok := s.fields[name]
	return field, ok
}

// Resource returns the immutable resource identity represented by the schema.
func (s *Schema) Resource() string {
	if s == nil {
		return ""
	}
	return s.resource
}

// SchemaRegistry is an immutable resource-to-query-schema allowlist.
type SchemaRegistry struct{ schemas map[string]*Schema }

func NewSchemaRegistry(schemas ...*Schema) (*SchemaRegistry, error) {
	registry := &SchemaRegistry{schemas: make(map[string]*Schema, len(schemas))}
	for _, schema := range schemas {
		if schema == nil || schema.Resource() == "" {
			return nil, ErrInvalidSchema
		}
		if _, exists := registry.schemas[schema.Resource()]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateSchema, schema.Resource())
		}
		registry.schemas[schema.Resource()] = schema
	}
	return registry, nil
}

func (r *SchemaRegistry) Get(resource string) (*Schema, bool) {
	if r == nil {
		return nil, false
	}
	schema, ok := r.schemas[resource]
	return schema, ok
}

// ValidateEndpoint rejects a transport descriptor that claims data-scope
// enforcement for a resource without a registered query schema. It is a
// startup/CI guard against metadata that is stronger than the implementation.
func (r *SchemaRegistry) ValidateEndpoint(endpoint accesscontrol.Endpoint) error {
	if endpoint.DataPermission == accesscontrol.DataPermissionNone {
		return nil
	}
	if _, ok := r.Get(endpoint.Resource); !ok {
		return fmt.Errorf("%w: endpoint %s resource %s", ErrInvalidSchema, endpoint.Operation, endpoint.Resource)
	}
	return nil
}
