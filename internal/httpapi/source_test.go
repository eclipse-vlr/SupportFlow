package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"supportflow/internal/ticket"
)

type snapshotRepository struct {
	fakeRepository
	item ticket.Ticket
}

func (s *snapshotRepository) Get(context.Context, string) (ticket.Ticket, error) { return s.item, nil }

func TestSourceReturnsSavedSnapshot(t *testing.T) {
	// Deliberately differs from the embedded article and contains markup: the
	// endpoint must read the stored version and serve it as plain text.
	repo := &snapshotRepository{item: ticket.Ticket{Analysis: &ticket.Analysis{Sources: []ticket.Source{
		{ID: "technical-export", Title: "Старая памятка", Content: "<script>old version</script>", Version: strings.Repeat("a", 64)},
	}}}}
	handler := New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), "demo")
	base := "/api/v1/tickets/a2d5027e-a00a-4000-8000-000000000000/sources/"
	for _, tc := range []struct {
		path   string
		status int
	}{
		{base + "technical-export", 200}, {base + "missing", 404}, {base + "BAD", 400},
		{"/api/v1/tickets/invalid/sources/technical-export", 400},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", tc.path, nil))
		if response.Code != tc.status {
			t.Fatalf("%s: status=%d", tc.path, response.Code)
		}
		if tc.status == 200 && (response.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
			!strings.Contains(response.Body.String(), "<script>old version</script>") ||
			!strings.Contains(response.Body.String(), strings.Repeat("a", 64))) {
			t.Fatalf("snapshot changed or served as HTML: %s", response.Body.String())
		}
	}
	for _, analysis := range []*ticket.Analysis{nil, {}} {
		repo.item.Analysis = analysis
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", base+"technical-export", nil))
		if response.Code != 404 {
			t.Errorf("legacy ticket: status=%d, want 404", response.Code)
		}
	}
}
