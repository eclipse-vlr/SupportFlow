package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"supportflow/internal/ticket"
)

const analysisJSON = `{"category":"billing","summary":"Оплата без доступа","priority":"medium","priority_reason":"Один пользователь","draft_reply":"Уточните дату оплаты."}`

func envelope(status, text string) string {
	data, _ := json.Marshal(map[string]any{"status": status, "model": "test-model", "usage": map[string]int{"input_tokens": 12, "output_tokens": 9}, "output": []any{map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": text}}}}})
	return string(data)
}

func TestResponsesContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("invalid API request")
		}
		var payload struct {
			Model     string `json:"model"`
			Store     bool   `json:"store"`
			MaxTokens int    `json:"max_output_tokens"`
			Input     string `json:"input"`
			Text      struct {
				Format struct {
					Type   string `json:"type"`
					Strict bool   `json:"strict"`
				} `json:"format"`
			} `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Store || payload.MaxTokens != 1000 || payload.Model != "test-model" || payload.Text.Format.Type != "json_schema" || !payload.Text.Format.Strict || !strings.Contains(payload.Input, "Оплата") {
			t.Errorf("invalid payload: %+v", payload)
		}
		fmt.Fprint(w, envelope("completed", analysisJSON))
	}))
	defer server.Close()
	client := New(server.URL, "test-secret", "test-model", 1000, time.Second)
	defer client.Close()
	a, u, err := client.Analyze(t.Context(), ticket.Input{Title: "Оплата", Message: "Тест"})
	if err != nil || a.Category != "billing" || u.InputTokens != 12 || u.OutputTokens != 9 {
		t.Fatalf("response: %+v %+v %v", a, u, err)
	}
}

func TestProviderFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, code string
		retry      bool
	}{
		{"rate limit", 429, `{"error":"test-secret private customer text"}`, "provider_http_429", true},
		{"auth", 401, `{"error":"test-secret"}`, "provider_http_401", false},
		{"server", 503, `{}`, "provider_http_503", true},
		{"invalid json", 200, `{`, "provider_invalid_json", false},
		{"incomplete", 200, envelope("incomplete", analysisJSON), "provider_incomplete", false},
		{"invalid category", 200, envelope("completed", strings.Replace(analysisJSON, "billing", "invented", 1)), "provider_invalid_analysis", false},
		{"extra json", 200, envelope("completed", analysisJSON+` {}`), "provider_invalid_analysis", false},
		{"refusal", 200, `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal"}]}]}`, "provider_refusal", false},
		{"oversized", 200, strings.Repeat("x", 256*1024+1), "provider_response_too_large", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client := New(server.URL, "test-secret", "test-model", 1000, time.Second)
			defer client.Close()
			_, _, err := client.Analyze(t.Context(), ticket.Input{})
			var providerErr *Error
			if !errors.As(err, &providerErr) || providerErr.Code != tc.code || providerErr.Retryable != tc.retry {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "customer") {
				t.Fatal("sensitive upstream body leaked")
			}
			if tc.status == 429 && providerErr.RetryAfter != 7*time.Second {
				t.Fatal("Retry-After ignored")
			}
		})
	}
}

func TestCancellationStopsProviderRequest(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Consume request data so the server observes the peer disconnect.
		var body any
		_ = json.NewDecoder(r.Body).Decode(&body)
		close(started)
		<-r.Context().Done()
		close(finished)
	}))
	defer server.Close()
	client := New(server.URL, "key", "model", 1000, time.Second)
	defer client.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := client.Analyze(ctx, ticket.Input{}); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not stop")
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request remained active")
	}
}
