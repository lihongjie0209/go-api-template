package routepolicy

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/lihongjie0209/microservice-platform-go/stableid"
)

const routeNamespace = "afc952de-95c3-455a-bc5c-89fb3c2a5da5"

var routeSegment = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

const (
	parameterSegmentPrefix = "p-"
	wildcardSegmentPrefix  = "w-"
	staticEscapePrefix     = "s-"
	hexSegmentPrefix       = "h-"
)

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
		encoded, err := canonicalRouteSegment(segment)
		if err != nil {
			return Route{}, err
		}
		segments = append(segments, encoded)
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

func canonicalRouteSegment(segment string) (string, error) {
	if segment == "" {
		return "", fmt.Errorf("%w: empty route segment", ErrInvalid)
	}
	kind := byte(0)
	if segment[0] == ':' || segment[0] == '*' {
		kind = segment[0]
		segment = segment[1:]
	}
	if segment == "" {
		return "", fmt.Errorf("%w: empty route parameter", ErrInvalid)
	}
	encoded := segment
	hexEncoded := false
	if segment != strings.ToLower(segment) || !routeSegment.MatchString(segment) {
		encoded = hexSegmentPrefix + hex.EncodeToString([]byte(segment))
		hexEncoded = true
	}
	switch kind {
	case ':':
		return parameterSegmentPrefix + encoded, nil
	case '*':
		return wildcardSegmentPrefix + encoded, nil
	default:
		if hexEncoded {
			return encoded, nil
		}
		for _, prefix := range []string{parameterSegmentPrefix, wildcardSegmentPrefix, staticEscapePrefix, hexSegmentPrefix} {
			if strings.HasPrefix(encoded, prefix) {
				return staticEscapePrefix + encoded, nil
			}
		}
		return encoded, nil
	}
}
