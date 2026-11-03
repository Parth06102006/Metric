package domain

import (
	"context"
	"time"
)

// ProjectRepository persists projects and verifies ingest credentials.
type ProjectRepository interface {
	Create(ctx context.Context, project Project, apiKeyHash string) error
	Get(ctx context.Context, projectID string) (Project, error)
	List(ctx context.Context) ([]Project, error)
	ListByOwner(ctx context.Context, ownerEmail string) ([]Project, error)
	Authenticate(ctx context.Context, projectID, apiKey string) (bool, error)

	// Members are view-only and can only be added after a trusted sign-in.
	RecordUser(ctx context.Context, email string) error
	ListUsers(ctx context.Context) ([]string, error)
	ListMembers(ctx context.Context, projectID string) ([]string, error)
	AddMember(ctx context.Context, projectID, email string) error
	RemoveMember(ctx context.Context, projectID, email string) error

	// UpdateKeyHash stores the hash of a freshly rotated key and re-activates
	// it. The previous key's hash is overwritten, so it stops working.
	UpdateKeyHash(ctx context.Context, projectID, apiKeyHash string) error
	// RevokeKey invalidates the project's ingest key.
	RevokeKey(ctx context.Context, projectID string) error
	// SetOwner assigns the project to an owner email. An empty email leaves
	// the project ownerless, which only admins can access.
	SetOwner(ctx context.Context, projectID, ownerEmail string) error
	// Delete removes the project row and all dependent rows in one
	// transaction. It returns ErrNotFound when the project does not exist.
	Delete(ctx context.Context, projectID string) error
}

// ThresholdRepository persists per-project alert limits.
type ThresholdRepository interface {
	Get(ctx context.Context, projectID string) (Threshold, error)
	Upsert(ctx context.Context, projectID string, threshold Threshold) error
}

// RecordRepository holds the live rolling window of request logs.
type RecordRepository interface {
	Append(ctx context.Context, projectID string, records []LogRecord) error
	Recent(ctx context.Context, projectID string, since time.Time) ([]LogRecord, error)
	ActiveProjects(ctx context.Context) []string
	// Delete drops a project's live window so the worker stops computing
	// snapshots for it after deletion.
	Delete(ctx context.Context, projectID string) error
}

// SnapshotRepository persists computed snapshots for history.
type SnapshotRepository interface {
	Save(ctx context.Context, projectID string, snapshot Snapshot) error
	Range(ctx context.Context, projectID string, from, to time.Time, limit int) ([]Snapshot, error)
	// Recent returns the newest limited snapshots in chronological order.
	Recent(ctx context.Context, projectID string, limit int) ([]Snapshot, error)
	Prune(ctx context.Context, before time.Time) (int64, error)
}

// AnomalyRepository persists detected anomalies.
type AnomalyRepository interface {
	Save(ctx context.Context, projectID string, anomaly *Anomaly) error
	List(ctx context.Context, projectID string, limit int) ([]Anomaly, error)
	HasRecent(ctx context.Context, projectID, anomalyType string, since time.Time) (bool, error)
}

// Broadcaster pushes live events to subscribed clients.
type Broadcaster interface {
	BroadcastSnapshot(projectID string, snapshot Snapshot)
	BroadcastAnomaly(projectID string, anomaly Anomaly)
	// Drop disconnects every live subscriber of a project, used when the
	// project is deleted.
	Drop(projectID string)
}

// SnapshotProvider computes the current snapshot for a project.
type SnapshotProvider interface {
	Current(ctx context.Context, projectID string) (Snapshot, error)
}

// Explainer turns an anomaly into a human-readable analysis.
type Explainer interface {
	Explain(ctx context.Context, projectID string, anomaly Anomaly, snapshot Snapshot) string
}

// AuditQuery narrows an audit feed read. Every field is optional:
//   - Limit caps the number of returned events (the usecase applies a default).
//   - Action matches one audit action exactly, e.g. "project.delete".
//   - ProjectID matches the target project exactly.
//   - Before/BeforeID are an exclusive cursor for loading older events. Both
//     come from the last event on screen, so events that share a millisecond
//     are never skipped between pages.
type AuditQuery struct {
	Limit     int
	Action    string
	ProjectID string
	Before    time.Time
	BeforeID  int64
}

// AuditRepository persists and reads audit events. There is intentionally no
// update or delete operation exposed to callers: audit records are append-only.
// Prune is the single operational exception — an out-of-band retention job may
// remove rows older than the configured retention window, and no HTTP endpoint
// can trigger it.
type AuditRepository interface {
	Record(ctx context.Context, event AuditEvent) error
	List(ctx context.Context, query AuditQuery) ([]AuditEvent, error)
	Prune(ctx context.Context, before time.Time) (int64, error)
}

// AuditSink is the minimal write surface used by middleware, delivery, and the
// WebSocket handler. Nil sinks are skipped by callers.
type AuditSink interface {
	Record(ctx context.Context, event AuditEvent) error
}
