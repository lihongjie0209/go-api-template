package accesscontrol

import (
	"fmt"
	"testing"

	"github.com/lihongjie0209/go-api-template/internal/resourceaction"
)

func BenchmarkEndpointRegistryFind(b *testing.B) {
	const endpointCount = 300
	definitions := make([]resourceaction.ResourceDefinition, endpointCount)
	endpoints := make([]Endpoint, endpointCount)
	for index := range definitions {
		resource := fmt.Sprintf("bench.resource%d", index)
		definitions[index] = resourceaction.ResourceDefinition{
			Key: resource, Name: resource, Scope: resourceaction.ResourceScopePlatform,
			Actions: []resourceaction.ActionDefinition{{Key: "read", Name: "Read"}},
		}
		endpoints[index] = Endpoint{
			Transport: TransportHTTP, Operation: fmt.Sprintf("POST /api/v1/bench/%d", index),
			Authentication: AuthenticationJWT, Resource: resource, Action: "read",
			DataPermission: DataPermissionNone, DataPermissionReason: "platform resource",
		}
	}
	resources, err := resourceaction.NewRegistry(definitions)
	if err != nil {
		b.Fatal(err)
	}
	registry, err := NewEndpointRegistry(resources, endpoints)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if len(registry.Find("bench.resource299", "read")) != 1 {
			b.Fatal("endpoint was not found")
		}
	}
}
