package httpapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"supportflow/internal/ticket"
)

type unavailableRepository struct{ Repository }

func (unavailableRepository) Create(context.Context, ticket.Input, string) (ticket.Ticket, bool, error) {
	return ticket.Ticket{}, false, &pgconn.PgError{Code: "42501", Message: "private-database-detail", Detail: "private-ticket-text"}
}

func TestRepositoryFailureHasSafeDiagnosticsAndGenericResponse(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	handler := New(unavailableRepository{}, slog.New(slog.NewJSONHandler(&output, nil)), "demo")
	request := httptest.NewRequest("POST", "/api/v1/tickets", strings.NewReader(`{"title":"Тест","message":"Оплата"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 500 || !strings.Contains(response.Body.String(), "internal_error") {
		t.Errorf("POST with database failure = %d %s, want 500 internal_error", response.Code, response.Body)
	}
	for _, private := range []string{"private-database-detail", "private-ticket-text"} {
		if strings.Contains(output.String()+response.Body.String(), private) {
			t.Errorf("database failure exposed %q", private)
		}
	}
	if !strings.Contains(output.String(), `"sqlstate":"42501"`) || !strings.Contains(output.String(), `"route":"POST /api/v1/tickets"`) {
		t.Errorf("database failure log = %s, want SQLSTATE and matched route", &output)
	}
}
