package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors understood by the delivery layer. Every layer below HTTP
// returns one of these (wrapped is fine) so status mapping lives in one place.
var (
	ErrNotFound     = errors.New("not found")
	ErrValidation   = errors.New("validation failed")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrConflict     = errors.New("conflict")
)

// Validationf builds a validation error with a human-readable detail.
func Validationf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, args...))
}
