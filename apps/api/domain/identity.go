package domain

import "context"

// Identity is the authenticated caller as asserted by the trusted edge proxy.
type Identity struct {
	Email string
	User  string
	Admin bool
}

type identityKey struct{}

// WithIdentity stores the caller identity on the request context.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom extracts the caller identity, if the middleware ran.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}
