package ws_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"traffic-center/domain"
	"traffic-center/internal/ws"
	"traffic-center/metrics/repository/memory"
	projectmemory "traffic-center/project/repository/memory"
)

func TestHandlerRejectsMissingIdentityEvenWithoutMiddleware(t *testing.T) {
	projects := projectmemory.New()
	if err := projects.Create(context.Background(), domain.Project{ID: "p", Name: "p"}, "hash"); err != nil {
		t.Fatal(err)
	}
	handler := ws.NewHandler(projects, memory.New(time.Minute), ws.NewHub(), time.Minute, nil, nil, nil)
	router := chi.NewRouter()
	router.Get("/ws/{projectID}", handler.ServeHTTP)
	for _, ctx := range []context.Context{context.Background(), domain.WithIdentity(context.Background(), domain.Identity{Admin: true})} {
		req := httptest.NewRequest(http.MethodGet, "/ws/p", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("missing identity status=%d", w.Code)
		}
	}
}
