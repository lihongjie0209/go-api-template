package routepolicy

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lihongjie0209/microservice-platform-go/stableid"
)

const routeNamespace = "afc952de-95c3-455a-bc5c-89fb3c2a5da5"

var routeSegment = regexp.MustCompile(`^[a-z0-9_.-]+$`)

type Route struct {
	ID            string
	Protocol      string
	Method        string
	Path          string
	Operation     string
	Description   string
	ServiceName   string
	SourceVersion string
}

func NewRoute(protocol, method, path, serviceName, sourceVersion string) (Route, error) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	method = strings.ToLower(strings.TrimSpace(method))
	path = strings.TrimSpace(path)
	serviceName = strings.ToLower(strings.TrimSpace(serviceName))
	if (protocol != "http" && protocol != "grpc") || method == "" || path == "" || serviceName == "" {
		return Route{}, fmt.Errorf("%w: incomplete route identity", ErrInvalid)
	}
	segments := []string{"route", protocol, method}
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		segment = strings.TrimPrefix(segment, ":")
		segment = strings.TrimPrefix(segment, "*")
		segment = strings.ToLower(segment)
		if segment == "" || !routeSegment.MatchString(segment) {
			return Route{}, fmt.Errorf("%w: unsupported route segment %q", ErrInvalid, segment)
		}
		segments = append(segments, segment)
	}
	generator, err := stableid.New(routeNamespace)
	if err != nil {
		return Route{}, fmt.Errorf("create route id generator: %w", err)
	}
	id, err := generator.String(strings.Join(segments, ":"))
	if err != nil {
		return Route{}, fmt.Errorf("generate route id: %w", err)
	}
	return Route{
		ID:            id,
		Protocol:      protocol,
		Method:        method,
		Path:          path,
		ServiceName:   serviceName,
		SourceVersion: sourceVersion,
	}, nil
}
