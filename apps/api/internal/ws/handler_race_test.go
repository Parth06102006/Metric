package ws_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"traffic-center/domain"
	"traffic-center/internal/ws"
	metricsmemory "traffic-center/metrics/repository/memory"
	"traffic-center/project"
	projectmemory "traffic-center/project/repository/memory"
)

// Delay only the handshake's first authorization lookup, after it has read the
// old project. This deterministically exercises mutation before registration.
type delayedProjectLookup struct {
	domain.ProjectRepository
	firstRead chan struct{}
	resume    chan struct{}
	calls     atomic.Int32
}

func (r *delayedProjectLookup) Get(ctx context.Context, id string) (domain.Project, error) {
	p, err := r.ProjectRepository.Get(ctx, id)
	if r.calls.Add(1) == 1 {
		close(r.firstRead)
		select {
		case <-r.resume:
		case <-ctx.Done():
			return domain.Project{}, ctx.Err()
		}
	}
	return p, err
}

func TestWebSocketRechecksAccessAfterConcurrentProjectChange(t *testing.T) {
	for _, change := range []string{"reassign", "delete", "remove-viewer"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			projects := projectmemory.New()
			records := metricsmemory.New(time.Minute)
			hub := ws.NewHub()
			created, err := project.NewUsecase(projects).Create(ctx, "Concurrent handover")
			if err != nil {
				t.Fatal(err)
			}
			lifecycle := project.NewLifecycle(projects, records, hub)
			owner := "alice@example.com"
			if change == "remove-viewer" {
				owner = "bob@example.com"
				if err := projects.RecordUser(ctx, "alice@example.com"); err != nil {
					t.Fatal(err)
				}
				if err := lifecycle.AddViewer(ctx, created.ID, "alice@example.com"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := lifecycle.AssignOwner(ctx, created.ID, owner); err != nil {
				t.Fatal(err)
			}
			delayed := &delayedProjectLookup{ProjectRepository: projects, firstRead: make(chan struct{}), resume: make(chan struct{})}
			handler := ws.NewHandler(delayed, records, hub, time.Minute, nil, nil, nil)
			router := chi.NewRouter()
			router.Get("/ws/{projectID}", handler.ServeHTTP)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				identity := domain.Identity{Email: "alice@example.com"}
				router.ServeHTTP(w, r.WithContext(domain.WithIdentity(r.Context(), identity)))
			}))
			defer server.Close()
			var resumeOnce sync.Once
			resume := func() { resumeOnce.Do(func() { close(delayed.resume) }) }
			defer resume()
			type dialResult struct {
				conn *websocket.Conn
				err  error
			}
			result := make(chan dialResult, 1)
			go func() {
				conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/"+created.ID, nil)
				result <- dialResult{conn, err}
			}()
			select {
			case <-delayed.firstRead:
			case <-time.After(3 * time.Second):
				t.Fatal("handshake did not reach first lookup")
			}
			switch change {
			case "reassign":
				_, err = lifecycle.AssignOwner(ctx, created.ID, "bob@example.com")
			case "remove-viewer":
				err = lifecycle.RemoveViewer(ctx, created.ID, "alice@example.com")
			default:
				err = lifecycle.DeleteProject(ctx, created.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			resume()
			select {
			case got := <-result:
				if got.err != nil {
					t.Fatalf("handshake failed before recheck: %v", got.err)
				}
				defer got.conn.Close()
				_ = got.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				_, _, err := got.conn.ReadMessage()
				if err == nil {
					t.Fatal("stale handshake received data after project access changed")
				}
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					t.Fatal("stale handshake remained open")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("handshake did not complete")
			}
			if count := hub.SubscriberCount(created.ID); count != 0 {
				t.Fatalf("stale subscriber count = %d", count)
			}
		})
	}
}
