package ws

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"traffic-center/domain"
	metricsmemory "traffic-center/metrics/repository/memory"
	projectmemory "traffic-center/project/repository/memory"
)

const expiryTestProject = "expiry-test"

type expiryFixture struct {
	url        string
	hub        *Hub
	authorized atomic.Bool
	handshakes atomic.Int32
	finished   chan struct{}
}

// Simulate the edge's authentication decision on every handshake. The Go
// handler has no SSO cookie or provider credentials to validate on its own.
func newExpiryFixture(t *testing.T, lifetime time.Duration, records domain.RecordRepository) *expiryFixture {
	t.Helper()
	projects := projectmemory.New()
	if err := projects.Create(context.Background(), domain.Project{
		ID: expiryTestProject, Name: "Expiry", OwnerEmail: "alice@example.com",
	}, "unused"); err != nil {
		t.Fatal(err)
	}
	if records == nil {
		records = metricsmemory.New(time.Minute)
	}
	f := &expiryFixture{hub: NewHub(), finished: make(chan struct{}, 4)}
	f.authorized.Store(true)
	handler := NewHandler(projects, records, f.hub, time.Minute, nil, nil, nil)
	if handler.maxConnectionAge != 5*time.Minute {
		t.Fatalf("default connection lifetime = %v, want 5m", handler.maxConnectionAge)
	}
	// Shorten only this handler instance, never a global or production setting.
	handler.maxConnectionAge = lifetime
	router := chi.NewRouter()
	router.Get("/ws/{projectID}", handler.ServeHTTP)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.handshakes.Add(1)
		defer func() { f.finished <- struct{}{} }()
		if !f.authorized.Load() {
			http.Error(w, "session expired", http.StatusUnauthorized)
			return
		}
		identity := domain.Identity{Email: "alice@example.com"}
		router.ServeHTTP(w, r.WithContext(domain.WithIdentity(r.Context(), identity)))
	}))
	t.Cleanup(server.Close)
	f.url = "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/" + expiryTestProject
	return f
}

func (f *expiryFixture) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(f.url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func readInitialSnapshot(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	var envelope Envelope
	if err := conn.ReadJSON(&envelope); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	if envelope.EventType != EventMetricSnapshot {
		t.Fatalf("initial event = %q", envelope.EventType)
	}
}

func (f *expiryFixture) awaitCleanup(t *testing.T) {
	t.Helper()
	select {
	case <-f.finished:
	case <-time.After(3 * time.Second):
		t.Fatal("expired handler did not exit")
	}
	if count := f.hub.SubscriberCount(expiryTestProject); count != 0 {
		t.Fatalf("expired subscribers remaining = %d", count)
	}
}

func TestConnectionExpiryRequiresFreshAuthentication(t *testing.T) {
	for _, authorized := range []bool{true, false} {
		name := "valid session reconnects"
		if !authorized {
			name = "expired session rejected"
		}
		t.Run(name, func(t *testing.T) {
			f := newExpiryFixture(t, 150*time.Millisecond, nil)
			conn := f.dial(t)
			readInitialSnapshot(t, conn)
			f.authorized.Store(authorized)

			_, _, err := conn.ReadMessage()
			if !websocket.IsCloseError(err, websocket.CloseGoingAway) {
				t.Fatalf("expiry close = %v, want close code 1001", err)
			}
			f.awaitCleanup(t)

			if authorized {
				reconnected := f.dial(t)
				readInitialSnapshot(t, reconnected)
				_ = reconnected.Close()
			} else {
				reconnected, response, err := websocket.DefaultDialer.Dial(f.url, nil)
				if reconnected != nil {
					_ = reconnected.Close()
				}
				if response != nil {
					defer response.Body.Close()
				}
				if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
					t.Fatalf("reconnect = response %v, error %v; want 401", response, err)
				}
			}
			f.awaitCleanup(t)
			if got := f.handshakes.Load(); got != 2 {
				t.Fatalf("authentication checks = %d, want 2", got)
			}
		})
	}
}

func TestPongsAndTrafficCannotExtendConnectionLifetime(t *testing.T) {
	f := newExpiryFixture(t, 150*time.Millisecond, nil)
	conn := f.dial(t)
	readInitialSnapshot(t, conn)

	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				// Unsolicited pongs exercise the server's sliding read deadline.
				if err := conn.WriteControl(websocket.PongMessage, nil, time.Now().Add(time.Second)); err != nil {
					return
				}
				f.hub.BroadcastSnapshot(expiryTestProject, domain.Snapshot{})
			}
		}
	}()
	defer func() { close(stop); <-stopped }()

	messages := 0
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				t.Fatal("pongs or outgoing traffic kept the stream open past expiry")
			}
			break
		}
		messages++
	}
	if messages == 0 {
		t.Fatal("no live traffic exercised before expiry")
	}
	f.awaitCleanup(t)
}

type blockedSnapshotRecords struct {
	domain.RecordRepository
	cancelled chan struct{}
}

func (r *blockedSnapshotRecords) Recent(ctx context.Context, _ string, _ time.Time) ([]domain.LogRecord, error) {
	<-ctx.Done()
	close(r.cancelled)
	return nil, ctx.Err()
}

func TestConnectionExpiryCancelsInitialSnapshot(t *testing.T) {
	records := &blockedSnapshotRecords{cancelled: make(chan struct{})}
	f := newExpiryFixture(t, 150*time.Millisecond, records)
	conn := f.dial(t)
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("received an initial snapshot after expiry")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatal("blocked initial snapshot prevented expiry")
	}
	select {
	case <-records.cancelled:
	case <-time.After(time.Second):
		t.Fatal("initial snapshot context was not cancelled")
	}
	f.awaitCleanup(t)
}

func TestConnectionExpiryDoesNotWaitForAccessFence(t *testing.T) {
	f := newExpiryFixture(t, 150*time.Millisecond, nil)
	conn := f.dial(t)
	readInitialSnapshot(t, conn)
	func() {
		f.hub.LockStreamAccess()
		defer f.hub.UnlockStreamAccess()
		f.hub.BroadcastSnapshot(expiryTestProject, domain.Snapshot{})
		_, _, err := conn.ReadMessage()
		if !websocket.IsCloseError(err, websocket.CloseGoingAway) {
			t.Fatalf("expiry while access fence held = %v, want close code 1001", err)
		}
	}()
	f.awaitCleanup(t)
}

func TestStreamWriteDeadlineNeverExceedsExpiry(t *testing.T) {
	expiresAt := time.Now().Add(50 * time.Millisecond)
	if got := streamWriteDeadline(expiresAt); !got.Equal(expiresAt) {
		t.Fatalf("write deadline = %v, want expiry %v", got, expiresAt)
	}
	past := time.Now().Add(-time.Second)
	if got := streamWriteDeadline(past); !got.Equal(past) {
		t.Fatalf("expired write deadline = %v, want %v", got, past)
	}
	start := time.Now()
	got := streamWriteDeadline(start.Add(time.Hour))
	if got.Before(start.Add(writeWait)) || got.After(time.Now().Add(writeWait)) {
		t.Fatalf("write deadline = %v, want usual I/O timeout", got)
	}
}
