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
	// ErrAccessDenied means the caller is authenticated but not allowed to
	// reach this resource. Delivery maps it to the same 404 as ErrNotFound so
	// the API never reveals whether the resource exists, while handlers can
	// still audit the denial.
	ErrAccessDenied = errors.New("access denied")
)

// Validationf builds a validation error with a human-readable detail.
func Validationf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, args...))
}
