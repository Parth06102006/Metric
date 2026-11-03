package domain

import (
	"context"
	"strings"
)

// maxEmailLength bounds owner emails stored on a project.
const maxEmailLength = 254

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

// ValidateEmail checks an owner email. An empty email is valid and means the
// project has no owner, which only admins can access. This is a shape check,
// not a deliverability check: the trusted identity proxy already verified the
// address when the user signed in.
func ValidateEmail(email string) error {
	if email == "" {
		return nil
	}
	if len(email) > maxEmailLength {
		return Validationf("owner email must be %d characters or fewer", maxEmailLength)
	}
	if strings.ContainsAny(email, " \t\r\n") {
		return Validationf("owner email must not contain whitespace")
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return Validationf("owner email must be a valid email address")
	}
	return nil
}
