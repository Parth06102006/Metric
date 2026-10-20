package reqid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type key struct{}

// New returns a random request identifier.
func New() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}

// With stores the identifier on the context.
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, key{}, id)
}

// From returns the identifier, or an empty string when absent.
func From(ctx context.Context) string {
	id, _ := ctx.Value(key{}).(string)
	return id
}
