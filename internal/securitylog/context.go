package securitylog

import "context"

type clientContextKey struct{}

type clientContext struct {
	ip        string
	userAgent string
}

// WithClient adds transport metadata used by security events created below
// the handler layer. Values remain request-scoped and are never trusted for
// authentication or authorization decisions.
func WithClient(ctx context.Context, ip, userAgent string) context.Context {
	return context.WithValue(ctx, clientContextKey{}, clientContext{ip: ip, userAgent: userAgent})
}

func clientFromContext(ctx context.Context) (string, string) {
	value, _ := ctx.Value(clientContextKey{}).(clientContext)
	return value.ip, value.userAgent
}
