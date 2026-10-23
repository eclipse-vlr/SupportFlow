package httpapi

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"supportflow/internal/ticket"
)

func TestWorkflowRejectsInvalidCommandsBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		status, calls      int
	}{
		{"PATCH", "closure", `{"closed":true}`, 400, 0},
		{"PATCH", "closure", `{"closed":true,"expected_version":null}`, 400, 0},
		{"PATCH", "closure", `{"closed":true,"expected_version":-1}`, 400, 0},
		{"PATCH", "closure", `{"expected_version":0}`, 400, 0},
		{"PATCH", "closure", `{"closed":null,"expected_version":0}`, 400, 0},
		{"PATCH", "closure", `{"closed":"true","expected_version":0}`, 400, 0},
		{"PATCH", "closure", `{"closed":true,"closed":false,"expected_version":0}`, 400, 0},
		{"PATCH", "closure", `{"closed":true,"expected_version":0,"admin":true}`, 400, 0},
		{"PATCH", "closure", `{"closed":false,"expected_version":0}`, 409, 1},
		{"PUT", "feedback", `{"rating":"useful","reason":""}`, 400, 0},
		{"PUT", "feedback", `{"rating":"other","reason":"","expected_version":0}`, 400, 0},
		{"PUT", "feedback", `{"rating":"rewritten","reason":"  ","expected_version":0}`, 400, 0},
		{"PUT", "feedback", `{"rating":"rewritten","reason":"\u0000","expected_version":0}`, 400, 0},
		{"PUT", "feedback", `{"rating":"useful","reason":"` + strings.Repeat("я", 1001) + `","expected_version":0}`, 400, 0},
		{"PUT", "feedback", `{"rating":"useful","reason":"","expected_version":0,"extra":true}`, 400, 0},
		{"PUT", "feedback", `{"rating":"useful","reason":"","expected_version":0}`, 409, 1},
		{"PUT", "feedback", `{"rating":"rewritten","reason":"Лишние обещания","expected_version":0}`, 409, 1},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			repo := &fakeRepository{}
			handler := New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), "demo")
			req := httptest.NewRequest(tc.method, "/api/v1/tickets/a2d5027e-a00a-4000-8000-000000000000/"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tc.status || repo.closed+repo.feedback != tc.calls {
				t.Fatalf("status=%d writes=%d body=%s", rr.Code, repo.closed+repo.feedback, rr.Body.String())
			}
		})
	}
}

func TestListSearchAndViewBoundary(t *testing.T) {
	repo := &fakeRepository{}
	handler := New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), "demo")
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"q=" + url.QueryEscape("  Ошибка CSV  ") + "&view=needs_review&category=technical&status=completed", 200},
		{"q=" + strings.Repeat("a", 201), 400}, {"q=%00", 400}, {"q=%FF", 400}, {"view=unknown", 400},
	} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/tickets?"+tc.query, nil))
		if rr.Code != tc.status {
			t.Errorf("%s: %d", tc.query, rr.Code)
		}
		if tc.status == 200 && repo.filter != (ticket.ListFilter{Category: "technical", Status: "completed", Search: "Ошибка CSV", View: "needs_review"}) {
			t.Fatalf("filter not forwarded: %+v", repo.filter)
		}
	}
}
