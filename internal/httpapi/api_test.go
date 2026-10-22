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

type fakeRepository struct{ created int }

func (f *fakeRepository) Create(_ context.Context, i ticket.Input, _ string) (ticket.Ticket, bool, error) {
	f.created++
	return ticket.Ticket{ID: "a2d5027e-a00a-4000-8000-000000000000", Input: i, Status: "pending"}, true, nil
}
func (f *fakeRepository) Get(context.Context, string) (ticket.Ticket, error) {
	return ticket.Ticket{}, ticket.ErrNotFound
}
func (f *fakeRepository) List(context.Context, string, string, int, int) ([]ticket.Ticket, error) {
	return []ticket.Ticket{}, nil
}
func (f *fakeRepository) Review(context.Context, string, ticket.Review) (ticket.Ticket, error) {
	return ticket.Ticket{}, ticket.ErrConflict
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
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if recorder.Code != tc.status {
			t.Errorf("%s status=%d", tc.path, recorder.Code)
		}
	}
}
