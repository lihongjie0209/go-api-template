package pbac

import (
	"fmt"

	"github.com/lihongjie0209/microservice-platform-go/stableid"
)

const policySeedNamespace = "076d4d4e-13d2-4a1d-a29e-9b8a2e109994"

// SeedID generates stable identifiers for reviewed built-in PBAC policy data.
func SeedID(canonicalKey string) (string, error) {
	generator, err := stableid.New(policySeedNamespace)
	if err != nil {
		return "", fmt.Errorf("configure PBAC seed namespace: %w", err)
	}
	id, err := generator.String(canonicalKey)
	if err != nil {
		return "", fmt.Errorf("generate PBAC seed ID: %w", err)
	}
	return id, nil
}
