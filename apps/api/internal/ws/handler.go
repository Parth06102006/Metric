package ws

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"traffic-center/domain"
	"traffic-center/internal/httpx"
	"traffic-center/internal/reqid"
)

const (
	writeWait        = 5 * time.Second
	pongWait         = 60 * time.Second
	pingPeriod       = 30 * time.Second
	readLimit        = 512
	maxConnectionAge = 5 * time.Minute
)

// Handler upgrades a request and streams snapshots and anomalies.
type Handler struct {
	projects     domain.ProjectRepository
	records      domain.RecordRepository
	snapshots    domain.SnapshotProvider
	initialSlots chan struct{}
	hub          *Hub
	window       time.Duration
	// Fixed authentication lifetime, independent of the sliding pong deadline.
	maxConnectionAge time.Duration
	allowedOrigins   map[string]struct{}
	logger           *slog.Logger
	audit            domain.AuditSink
	upgrader         websocket.Upgrader
}

// NewHandler wires the WebSocket delivery layer. The audit sink is nil-safe:
// when absent, authorization denials are not recorded.
func NewHandler(
	projects domain.ProjectRepository,
	records domain.RecordRepository,
	hub *Hub,
	window time.Duration,
	allowedOrigins map[string]struct{},
	logger *slog.Logger,
	audit domain.AuditSink,
	snapshots ...domain.SnapshotProvider,
) *Handler {
	if window <= 0 {
		window = 60 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}

	handler := &Handler{
		projects:         projects,
		records:          records,
		initialSlots:     make(chan struct{}, 8),
		hub:              hub,
		window:           window,
		maxConnectionAge: maxConnectionAge,
		allowedOrigins:   allowedOrigins,
		logger:           logger,
		audit:            audit,
	}
	if len(snapshots) > 0 {
		handler.snapshots = snapshots[0]
	}
	handler.upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		CheckOrigin:     handler.checkOrigin,
	}
	return handler
}

// checkOrigin allows same-host handshakes (the proxy preserves Host) plus any
// explicitly configured origin.
func (h *Handler) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(parsed.Host, r.Host) {
		return true
	}
	_, allowed := h.allowedOrigins[strings.ToLower(parsed.Host)]
	return allowed
}

// recordAccessDenied stores one access.denied event for an authenticated
// caller who targeted a project they do not own. Only the actor, target,
// outcome, request ID, and path are recorded; nothing credential-shaped and
// nothing that would change the client-visible response. The denial itself
// still returns 404, so the audit trail never leaks project existence.
func (h *Handler) recordAccessDenied(r *http.Request, actorEmail, projectID string) {
	if h.audit == nil {
		return
	}
	_ = h.audit.Record(r.Context(), domain.AuditEvent{
		Ts:              time.Now().UTC(),
		ActorEmail:      domain.SanitizeActor(actorEmail),
		Action:          domain.AuditAccessDenied,
		TargetProjectID: projectID,
		Outcome:         domain.AuditOutcomeFailure,
		RequestID:       reqid.From(r.Context()),
		Details:         map[string]string{"reason": "project_access_denied", "path": r.URL.Path},
	})
}

// ServeHTTP handles GET /ws/{projectID}.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Start at the authenticated request, not after snapshot loading. Reconnecting
	// must pass through the edge's SSO check and project authorization again.
	expiresAt := time.Now().Add(h.maxConnectionAge)
	ctx, cancel := context.WithDeadline(r.Context(), expiresAt)
	defer cancel()
	r = r.WithContext(ctx)

	identity, err := domain.RequireIdentity(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	projectID := chi.URLParam(r, "projectID")
	if projectID == "" {
		httpx.Error(w, r, domain.Validationf("project id is required"))
		return
	}

	p, err := h.projects.Get(r.Context(), projectID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !p.CanAccess(identity) {
		h.recordAccessDenied(r, identity.Email, projectID)
		httpx.Error(w, r, domain.ErrNotFound)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote a response.
		h.logger.Warn("websocket upgrade failed", "project_id", projectID, "error", err)
		return
	}
	defer func() { _ = conn.Close() }()

	// Run outside the writer loop so a slow snapshot, access fence, full socket,
	// or continuous traffic cannot postpone reauthentication. WriteControl and
	// Close may run concurrently with the reader/writer. Application writes are
	// also capped at expiresAt; only the close frame gets a bounded grace period.
	expiryTimer := time.AfterFunc(time.Until(expiresAt), func() {
		defer func() { _ = conn.Close() }()
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "authentication refresh required"),
			time.Now().Add(writeWait))
	})
	defer expiryTimer.Stop()

	// Ownership may change after the first lookup but before registration. Recheck
	// under the same fence used by ownership changes and project deletion.
	h.hub.accessMu.RLock()
	current, err := h.projects.Get(r.Context(), projectID)
	if err != nil || ctx.Err() != nil || !current.CanAccess(identity) {
		h.hub.accessMu.RUnlock()
		if err == nil && !current.CanAccess(identity) {
			h.recordAccessDenied(r, identity.Email, projectID)
		}
		return
	}
	client := h.hub.Register(projectID)
	defer h.hub.Unregister(projectID, client)

	conn.SetReadLimit(readLimit)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	// Reader: detects disconnects and keeps control frames flowing.
	go func() {
		defer client.close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	if !h.sendInitialSnapshot(ctx, conn, projectID, expiresAt) {
		h.hub.accessMu.RUnlock()
		return
	}
	h.hub.accessMu.RUnlock()

	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-client.Done():
			return

		case message := <-client.Send():
			h.hub.accessMu.RLock()
			// A queued message can race a closed client; do not write it after
			// ownership changes or deletion have returned.
			select {
			case <-client.Done():
				h.hub.accessMu.RUnlock()
				return
			default:
			}
			_ = conn.SetWriteDeadline(streamWriteDeadline(expiresAt))
			err := conn.WriteMessage(websocket.TextMessage, message)
			h.hub.accessMu.RUnlock()
			if err != nil {
				return
			}

		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, streamWriteDeadline(expiresAt)); err != nil {
				return
			}
		}
	}
}

// streamWriteDeadline prevents data writes (including an in-flight write) from
// extending the authentication lifetime while retaining the usual I/O timeout.
func streamWriteDeadline(expiresAt time.Time) time.Time {
	deadline := time.Now().Add(writeWait)
	if expiresAt.Before(deadline) {
		return expiresAt
	}
	return deadline
}

func (h *Handler) sendInitialSnapshot(ctx context.Context, conn *websocket.Conn, projectID string, expiresAt time.Time) bool {
	// Include socket serialization/write in the admission interval: slow new
	// subscribers must not hold unbounded full snapshots concurrently.
	select {
	case h.initialSlots <- struct{}{}:
		defer func() { <-h.initialSlots }()
	case <-ctx.Done():
		return false
	default:
		return false
	}
	var snapshot domain.Snapshot
	var err error
	if h.snapshots != nil {
		snapshot, err = h.snapshots.Current(ctx, projectID)
	} else {
		var records []domain.LogRecord
		records, err = h.records.Recent(ctx, projectID, time.Now().Add(-h.window))
		if err == nil {
			snapshot = domain.Compute(records, int(h.window.Seconds()))
		}
	}
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		h.logger.Warn("initial snapshot unavailable", "project_id", projectID, "error", err)
		return false
	}
	envelope := Envelope{
		EventType: EventMetricSnapshot,
		ProjectID: projectID,
		Timestamp: time.Now().UTC(),
		Data:      snapshot,
	}

	_ = conn.SetWriteDeadline(streamWriteDeadline(expiresAt))
	return conn.WriteJSON(envelope) == nil
}
