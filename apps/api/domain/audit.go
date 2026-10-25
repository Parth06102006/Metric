package domain

import (
	"context"
	"strings"
	"time"
)

// Audit actions. Actions are stable identifiers, never free-form strings.
const (
	AuditProjectCreate   = "project.create"
	AuditProjectDelete   = "project.delete"
	AuditKeyRotate       = "api_key.rotate"
	AuditKeyRevoke       = "api_key.revoke"
	AuditThresholdUpdate = "threshold.update"
	AuditAuthDenied      = "auth.denied"
	AuditAdminDenied     = "admin.denied"
	AuditIngestDenied    = "ingest.denied"
	AuditAccessDenied    = "access.denied"
)

// Audit outcomes.
const (
	AuditOutcomeSuccess = "success"
	AuditOutcomeFailure = "failure"
)

// AuditEvent is one security-relevant occurrence. It never carries
// credentials: no API keys, tokens, cookies, or request bodies.
type AuditEvent struct {
	ID              int64             `json:"id"`
	Ts              time.Time         `json:"ts"`
	ActorEmail      string            `json:"actor_email"`
	Action          string            `json:"action"`
	TargetProjectID string            `json:"target_project_id"`
	Outcome         string            `json:"outcome"`
	RequestID       string            `json:"request_id"`
	Details         map[string]string `json:"details"`
}

// AuditRepository persists and reads audit events. There is intentionally no
// update or delete operation exposed to callers: audit records are append-only.
// Prune is the single operational exception — an out-of-band retention job may
// remove rows older than the configured retention window, and no HTTP endpoint
// can trigger it.
type AuditRepository interface {
	Record(ctx context.Context, event AuditEvent) error
	List(ctx context.Context, limit int) ([]AuditEvent, error)
	Prune(ctx context.Context, before time.Time) (int64, error)
}

// AuditSink is the minimal write surface used by middleware and handlers.
// The audit.Usecase implements it; nil sinks are skipped by callers.
type AuditSink interface {
	Record(ctx context.Context, event AuditEvent) error
}

// SanitizeActor normalizes an email for storage. Empty stays empty.
func SanitizeActor(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// sensitiveDetailMarkers are substrings whose presence in a details key marks
// the value as a credential that must never be stored.
var sensitiveDetailMarkers = []string{
	"key", "secret", "token", "password", "cookie", "authorization", "credential",
}

// RedactDetails drops any detail whose key looks credential-shaped. Audit
// callers pass curated fields, this guards against mistakes.
func RedactDetails(details map[string]string) map[string]string {
	if details == nil {
		return map[string]string{}
	}

	safe := make(map[string]string, len(details))
	for key, value := range details {
		lowered := strings.ToLower(key)
		sensitive := false
		for _, marker := range sensitiveDetailMarkers {
			if strings.Contains(lowered, marker) {
				sensitive = true
				break
			}
		}
		if sensitive {
			safe[key] = "[redacted]"
			continue
		}
		safe[key] = value
	}
	return safe
}
