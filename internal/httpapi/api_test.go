package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"supportflow/internal/ticket"
)

type fakeRepository struct {
	created, reviewed, closed, feedback int
	filter                              ticket.ListFilter
}

func (f *fakeRepository) Create(_ context.Context, i ticket.Input, _ string) (ticket.Ticket, bool, error) {
	f.created++
	return ticket.Ticket{ID: "a2d5027e-a00a-4000-8000-000000000000", Input: i, Status: "pending"}, true, nil
}
func (f *fakeRepository) Get(context.Context, string) (ticket.Ticket, error) {
	return ticket.Ticket{}, ticket.ErrNotFound
}
func (f *fakeRepository) List(_ context.Context, filter ticket.ListFilter, _ int, _ int) ([]ticket.Ticket, error) {
	f.filter = filter
	return []ticket.Ticket{}, nil
}
func (f *fakeRepository) SetClosed(context.Context, string, bool, int64) (ticket.Ticket, error) {
	f.closed++
	return ticket.Ticket{}, ticket.ErrVersionConflict
}
func (f *fakeRepository) SetFeedback(context.Context, string, ticket.Feedback, int64) (ticket.Ticket, error) {
	f.feedback++
	return ticket.Ticket{}, ticket.ErrVersionConflict
}
func (f *fakeRepository) Review(context.Context, string, ticket.Review, int64) (ticket.Ticket, error) {
	f.reviewed++
	return ticket.Ticket{}, ticket.ErrReviewConflict
}
func (f *fakeRepository) History(context.Context, string, int64, int) ([]ticket.ReviewEvent, error) {
	return []ticket.ReviewEvent{}, nil
}

func TestReviewRequiresExplicitRevision(t *testing.T) {
	for _, tc := range []struct {
		name, extra   string
		status, calls int
	}{
		{"missing", "", 400, 0},
		{"null", `,"expected_revision":null`, 400, 0},
		{"negative", `,"expected_revision":-1`, 400, 0},
		{"string", `,"expected_revision":"0"`, 400, 0},
		{"stale", `,"expected_revision":0`, 409, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := &fakeRepository{}
			handler := New(repository, slog.New(slog.NewTextHandler(io.Discard, nil)), "demo")
			body := `{"category":"billing","draft_reply":"Ответ","approved":false` + tc.extra + `}`
			req := httptest.NewRequest("PATCH", "/api/v1/tickets/a2d5027e-a00a-4000-8000-000000000000/review", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.status || repository.reviewed != tc.calls {
				t.Errorf("PATCH review(%s): status=%d calls=%d, want status=%d calls=%d", tc.name, response.Code, repository.reviewed, tc.status, tc.calls)
			}
			if tc.status == 409 && !strings.Contains(response.Body.String(), "review_conflict") {
				t.Errorf("PATCH stale review body=%s, want review_conflict", response.Body.String())
			}
		})
	}
}
func (f *fakeRepository) Ping(context.Context) error { return nil }

func TestRejectInvalidRequestsBeforeCreatingTicket(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType string
		want                    int
	}{
		{"valid", `{"title":"Тест","message":"Оплатил"}`, "application/json", 202},
		{"empty", ``, "application/json", 400},
		{"null", `null`, "application/json", 400},
		{"trailing", `{"title":"Тест","message":"Тест"} {}`, "application/json", 400},
		{"duplicate", `{"title":"Тест","message":"Тест","message":"Другое"}`, "application/json", 400},
		{"case duplicate", `{"title":"Тест","Title":"Иное","message":"Тест"}`, "application/json", 400},
		{"unknown", `{"title":"Тест","message":"Тест","admin":true}`, "application/json", 400},
		{"blank title", `{"title":" ","message":"Тест"}`, "application/json", 400},
		{"nul", `{"title":"Тест","message":"Тест\u0000"}`, "application/json", 400},
		{"large body", `{"title":"Тест","message":"` + strings.Repeat("a", 33*1024) + `"}`, "application/json", 413},
		{"large message", `{"title":"Тест","message":"` + strings.Repeat("a", 17*1024) + `"}`, "application/json", 400},
		{"wrong type", `{"title":"Тест","message":"Тест"}`, "text/plain", 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := &fakeRepository{}
			handler := New(repository, slog.New(slog.NewTextHandler(io.Discard, nil)), "demo")
			req := httptest.NewRequest("POST", "/api/v1/tickets", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			wantCreated := 0
			if tc.want == 202 {
				wantCreated = 1
			}
			if repository.created != wantCreated {
				t.Fatal("invalid input reached persistence")
			}
		})
	}
}

func TestAPIReadBoundaries(t *testing.T) {
	handler := New(&fakeRepository{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "demo")
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/api/v1/tickets/invalid", 400}, {"/api/v1/tickets/a2d5027e-a00a-4000-8000-000000000000", 404},
		{"/api/v1/tickets?limit=101", 400}, {"/api/v1/tickets?offset=-1", 400}, {"/api/v1/tickets?category=unknown", 400},
		{"/api/v1/tickets", 200}, {"/healthz", 200}, {"/readyz", 200}, {"/", 200}, {"/app.js", 200},
		{"/api/v1/tickets/invalid/history", 400},
		{"/api/v1/tickets/a2d5027e-a00a-4000-8000-000000000000/history?limit=51", 400},
		{"/api/v1/tickets/a2d5027e-a00a-4000-8000-000000000000/history?before_revision=0", 400},
		{"/api/v1/tickets/a2d5027e-a00a-4000-8000-000000000000/history", 200},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if recorder.Code != tc.status {
			t.Errorf("%s status=%d", tc.path, recorder.Code)
		}
	}
}
